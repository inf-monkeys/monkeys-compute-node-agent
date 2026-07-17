package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	taskLeaseSeconds      = 60
	defaultTaskPollPeriod = 2 * time.Second
)

func Register(ctx context.Context, cfg Config) (RegisterResult, error) {
	if cfg.BootstrapToken == "" {
		return RegisterResult{}, fmt.Errorf("bootstrap token is required")
	}
	if strings.TrimSpace(cfg.AgentInstanceID) == "" {
		instanceID, err := loadOrCreateAgentInstanceID(cfg.StatePath)
		if err != nil {
			return RegisterResult{}, fmt.Errorf("prepare agent instance identity: %w", err)
		}
		cfg.AgentInstanceID = instanceID
	}
	c, err := newClient(cfg.ServerURL)
	if err != nil {
		return RegisterResult{}, err
	}
	facts := discoverForMode(ctx, cfg)
	result, err := c.register(ctx, registerFromFacts(facts, cfg))
	if err != nil {
		return RegisterResult{}, err
	}
	if err := saveState(cfg.StatePath, State{
		ServerURL: cfg.ServerURL, NodeID: result.Node.ID, NodeName: result.Node.Name,
		AgentToken: result.AgentToken, Mode: normalizedMode(cfg.Mode), Workspace: cfg.Workspace,
		TargetID: workerFirstNonEmpty(cfg.TargetID, result.Node.ID), AgentInstanceID: cfg.AgentInstanceID,
	}); err != nil {
		return RegisterResult{}, err
	}
	return result, nil
}

func Once(ctx context.Context, cfg Config) error {
	_, token, err := resolveRunConfig(&cfg)
	if err != nil {
		return err
	}
	c, err := newClient(cfg.ServerURL)
	if err != nil {
		return err
	}
	facts := discoverForMode(ctx, cfg)
	heartbeat, err := c.heartbeat(ctx, token, heartbeatFromFacts(facts, cfg))
	if err != nil {
		return err
	}
	if err := executePlan(ctx, c, cfg, token, heartbeat.Plan); err != nil {
		return err
	}
	return claimAndExecuteTasks(ctx, c, cfg, token, 1)
}

func Run(ctx context.Context, cfg Config) error {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	_, token, err := resolveRunConfig(&cfg)
	if err != nil {
		return err
	}
	c, err := newClient(cfg.ServerURL)
	if err != nil {
		return err
	}
	return runAgentLoops(ctx, c, cfg, token, defaultTaskPollPeriod)
}

var discoverHeartbeatFacts = discoverForMode

func heartbeatAndPlan(ctx context.Context, c *client, cfg Config, token string) error {
	facts := discoverHeartbeatFacts(ctx, cfg)
	heartbeat, err := c.heartbeat(ctx, token, heartbeatFromFacts(facts, cfg))
	if err != nil {
		return err
	}
	if err := executePlan(ctx, c, cfg, token, heartbeat.Plan); err != nil {
		return err
	}
	return nil
}

func runAgentLoops(ctx context.Context, c *client, cfg Config, token string, taskPollPeriod time.Duration) error {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	if taskPollPeriod <= 0 {
		taskPollPeriod = defaultTaskPollPeriod
	}
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		runPeriodic(ctx, cfg.Interval, func() {
			if err := heartbeatAndPlan(ctx, c, cfg, token); err != nil && ctx.Err() == nil {
				log.Printf("heartbeat failed: %v", err)
			}
		})
	}()
	go func() {
		defer workers.Done()
		runPeriodic(ctx, taskPollPeriod, func() {
			if err := claimAndExecuteTasks(ctx, c, cfg, token, 1); err != nil && ctx.Err() == nil {
				log.Printf("task poll failed: %v", err)
			}
		})
	}()
	<-ctx.Done()
	workers.Wait()
	return ctx.Err()
}

func runPeriodic(ctx context.Context, period time.Duration, operation func()) {
	operation()
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			operation()
		}
	}
}

func resolveRunConfig(cfg *Config) (State, string, error) {
	var state State
	if strings.TrimSpace(cfg.AgentToken) == "" {
		loaded, err := loadState(cfg.StatePath)
		if err != nil {
			return state, "", err
		}
		state = loaded
		cfg.AgentToken = loaded.AgentToken
		if cfg.ServerURL == "" {
			cfg.ServerURL = loaded.ServerURL
		}
		if cfg.Mode == "" {
			cfg.Mode = loaded.Mode
		}
		if cfg.Workspace == "" {
			cfg.Workspace = loaded.Workspace
		}
		if cfg.TargetID == "" {
			cfg.TargetID = loaded.TargetID
		}
		if cfg.AgentInstanceID == "" {
			cfg.AgentInstanceID = loaded.AgentInstanceID
		}
	}
	if strings.TrimSpace(cfg.AgentToken) == "" {
		return state, "", fmt.Errorf("agent token is required")
	}
	cfg.Mode = normalizedMode(cfg.Mode)
	if cfg.Workspace == "" {
		cfg.Workspace = "."
	}
	return state, cfg.AgentToken, nil
}

func discoverForMode(ctx context.Context, cfg Config) Facts {
	facts := Discover(ctx, cfg.Version)
	if normalizedMode(cfg.Mode) == "cluster" {
		if inClusterKubernetesConfigured() {
			facts.Kubernetes, facts.HAMi, facts.RuntimeStatuses = collectClusterState(ctx)
		} else {
			facts.Kubernetes["available"] = facts.Kubernetes["ready"] == true
			facts.Kubernetes["accessMethod"] = "kubectl"
		}
	}
	if normalizedMode(cfg.Mode) == "worker" {
		facts.Kubernetes = nil
		facts.HAMi = nil
		facts.RuntimeStatuses = workerRuntimeStatuses(cfg.Workspace)
	}
	return facts
}

func claimAndExecuteTasks(ctx context.Context, c *client, cfg Config, token string, limit int) error {
	claimed, err := c.claimTasks(ctx, token, max(1, min(limit, 20)), taskLeaseSeconds)
	if err != nil {
		return err
	}
	for _, task := range claimed.Items {
		if err := executeClaimedTask(ctx, c, cfg, token, task); err != nil {
			log.Printf("task %s acknowledgement failed: %v", task.ID, err)
		}
	}
	return nil
}

func executeClaimedTask(parent context.Context, c *client, cfg Config, token string, task ClaimedTask) error {
	if task.ID == "" || task.LeaseOwner == "" || task.TaskType == "" {
		return fmt.Errorf("claimed task is missing id, type, or lease owner")
	}
	taskCtx, cancel := context.WithCancel(parent)
	defer cancel()
	results := newTaskResultStore(cfg)
	if cached, ok, err := results.get(task.ID); err != nil {
		return err
	} else if ok {
		if cached.Status != "failed" || !cached.Retryable {
			return acknowledgeTaskOutcome(parent, c, token, task, cached, results)
		}
		// A retryable failure must execute again after the server grants a new lease.
		if err := results.remove(task.ID); err != nil {
			return err
		}
	}
	heartbeat := newTaskLeaseHeartbeat(taskCtx, cancel, c, token, task)
	defer heartbeat.stop()
	result, executionErr := dispatchClaimedTask(taskCtx, cfg, task)
	if heartbeat.lost() {
		return fmt.Errorf("task %s lease was lost", task.ID)
	}
	if executionErr == nil {
		outcome := cachedTaskOutcome{Status: "completed", Result: result, CompletedAt: time.Now().UnixMilli()}
		if err := results.put(task.ID, outcome); err != nil {
			return err
		}
		return acknowledgeTaskOutcome(parent, c, token, task, outcome, results)
	}
	retryable := false
	var classified interface{ Retryable() bool }
	if errors.As(executionErr, &classified) {
		retryable = classified.Retryable()
	}
	outcome := cachedTaskOutcome{Status: "failed", Result: result, ErrorMessage: truncateTaskError(executionErr.Error()), Retryable: retryable, CompletedAt: time.Now().UnixMilli()}
	if err := results.put(task.ID, outcome); err != nil {
		return err
	}
	err := acknowledgeTaskOutcome(parent, c, token, task, outcome, results)
	if err == nil && retryable {
		_ = results.remove(task.ID)
	}
	return err
}

func acknowledgeTaskOutcome(ctx context.Context, c *client, token string, task ClaimedTask, outcome cachedTaskOutcome, results *taskResultStore) error {
	var err error
	if outcome.Status == "completed" {
		err = c.completeTask(ctx, token, task.ID, CompleteTaskRequest{LeaseOwner: task.LeaseOwner, Result: outcome.Result})
	} else {
		delay := 0
		if outcome.Retryable {
			delay = 5
		}
		err = c.failTask(ctx, token, task.ID, FailTaskRequest{LeaseOwner: task.LeaseOwner, ErrorMessage: outcome.ErrorMessage, Result: outcome.Result, Retryable: outcome.Retryable, RetryDelaySeconds: delay})
	}
	if err == nil {
		return nil
	}
	if taskLeaseDefinitelyLost(err) && outcome.Status == "failed" {
		_ = results.remove(task.ID)
	}
	return err
}

func dispatchClaimedTask(ctx context.Context, cfg Config, task ClaimedTask) (map[string]any, error) {
	switch normalizedMode(cfg.Mode) {
	case "worker":
		return executeWorkerTask(ctx, cfg, task)
	case "cluster":
		if inClusterKubernetesConfigured() {
			return executeClusterTask(ctx, task.TaskType, task.Payload)
		}
		return executeHostKubernetesTask(ctx, task.TaskType, task.Payload)
	case "host":
		return executeHostKubernetesTask(ctx, task.TaskType, task.Payload)
	default:
		return nil, fmt.Errorf("unsupported agent mode")
	}
}

type taskLeaseHeartbeat struct {
	cancel    context.CancelFunc
	done      chan struct{}
	mutex     sync.Mutex
	leaseLost bool
}

func newTaskLeaseHeartbeat(ctx context.Context, cancel context.CancelFunc, c *client, token string, task ClaimedTask) *taskLeaseHeartbeat {
	heartbeat := &taskLeaseHeartbeat{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(heartbeat.done)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := c.taskHeartbeat(ctx, token, task.ID, TaskLeaseRequest{LeaseOwner: task.LeaseOwner, LeaseSeconds: taskLeaseSeconds}); err != nil && taskLeaseDefinitelyLost(err) {
					heartbeat.mutex.Lock()
					heartbeat.leaseLost = true
					heartbeat.mutex.Unlock()
					cancel()
					return
				}
			}
		}
	}()
	return heartbeat
}

func taskLeaseDefinitelyLost(err error) bool {
	var responseError *httpResponseError
	return errors.As(err, &responseError) && (responseError.StatusCode == 404 || responseError.StatusCode == 409)
}
func (h *taskLeaseHeartbeat) stop()      { h.cancel(); <-h.done }
func (h *taskLeaseHeartbeat) lost() bool { h.mutex.Lock(); defer h.mutex.Unlock(); return h.leaseLost }
func truncateTaskError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 9000 {
		return value[:9000]
	}
	return value
}

func workerRuntimeStatuses(workspace string) []RuntimeStatus {
	manager, err := newWorkerProcessManager(workspace)
	if err != nil {
		return nil
	}
	state, err := manager.store.load()
	if err != nil {
		return nil
	}
	result := make([]RuntimeStatus, 0, len(state.Processes))
	now := time.Now().UnixMilli()
	for _, record := range state.Processes {
		running := processRecordRunning(record)
		status := "stopped"
		if running {
			status = "running"
		}
		ready := running && processReady(record)
		result = append(result, RuntimeStatus{RuntimeID: record.RuntimeID, Status: status, Message: fmt.Sprintf("Worker process %s is %s.", record.ProcessID, status), DesiredReplicas: boolInt(record.Status != "stopped"), ReadyReplicas: boolInt(ready), AvailableReplicas: boolInt(ready), PodCount: 1, ReadyPodCount: boolInt(ready), ObservedAt: now})
	}
	return result
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func executePlan(ctx context.Context, c *client, cfg Config, token string, plan Plan) error {
	if plan.ID == "" || plan.Status == "completed" {
		return nil
	}
	if len(plan.Actions) == 0 {
		return c.event(ctx, token, plan.ID, EventRequest{
			EventType:  "node.plan.noop",
			Severity:   "debug",
			Message:    "No executable plan actions.",
			OccurredAt: time.Now().UnixMilli(),
		})
	}
	if err := c.event(ctx, token, plan.ID, EventRequest{
		EventType:  "node.plan.started",
		Severity:   "info",
		Message:    "Plan execution started.",
		OccurredAt: time.Now().UnixMilli(),
	}); err != nil {
		return err
	}
	completed := make([]string, 0, len(plan.Actions))
	planned := make([]string, 0, len(plan.Actions))
	failed := make([]string, 0)
	artifacts := map[string]any{}
	for _, action := range plan.Actions {
		result := executeAction(ctx, cfg, action)
		event := eventForActionResult(result)
		event.OccurredAt = time.Now().UnixMilli()
		if result.Success {
			if result.Planned {
				planned = append(planned, action.Type)
			} else {
				completed = append(completed, action.Type)
			}
			for key, value := range result.Artifacts {
				if key == "hostServiceResults" {
					artifacts[key] = appendResultArtifacts(artifacts[key], value)
					continue
				}
				artifacts[key] = value
			}
		} else {
			failed = append(failed, action.Type)
			if action.Type == "k8s.apply-runtime" {
				artifacts["runtimeResults"] = appendRuntimeResultArtifact(artifacts["runtimeResults"], map[string]any{
					"runtimeId": action.Payload["runtimeId"],
					"namespace": action.Payload["namespace"],
					"action":    action.Type,
					"success":   false,
					"message":   result.Message,
				})
			}
		}
		if err := c.event(ctx, token, plan.ID, event); err != nil {
			return err
		}
		if !result.Success {
			break
		}
	}
	payload := map[string]any{
		"completedActions": completed,
		"plannedActions":   planned,
		"failedActions":    failed,
		"completedAt":      time.Now().UnixMilli(),
	}
	for key, value := range artifacts {
		payload[key] = value
	}
	return c.complete(ctx, token, plan.ID, payload)
}

func appendResultArtifacts(current, next any) []map[string]any {
	results := make([]map[string]any, 0)
	for _, value := range []any{current, next} {
		switch typed := value.(type) {
		case []map[string]any:
			results = append(results, typed...)
		case []any:
			for _, item := range typed {
				if mapped, ok := item.(map[string]any); ok {
					results = append(results, mapped)
				}
			}
		case map[string]any:
			results = append(results, typed)
		}
	}
	return results
}

func appendRuntimeResultArtifact(current any, item map[string]any) []map[string]any {
	results := make([]map[string]any, 0)
	switch typed := current.(type) {
	case []map[string]any:
		results = append(results, typed...)
	case []any:
		for _, value := range typed {
			if mapped, ok := value.(map[string]any); ok {
				results = append(results, mapped)
			}
		}
	}
	return append(results, item)
}
