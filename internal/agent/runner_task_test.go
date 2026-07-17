package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestExecuteClaimedTaskReusesCachedResultWithoutReexecution(t *testing.T) {
	var mutex sync.Mutex
	completions := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		if request.URL.Path == "/api/compute/node-agent/tasks/task-1/complete" {
			completions++
		}
		mutex.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":0,"msg":"ok","data":{}}`))
	}))
	defer server.Close()
	client, _ := newClient(server.URL)
	workspace := t.TempDir()
	runtimeID := "runtime-1"
	task := ClaimedTask{ID: "task-1", TaskType: "process.exec", RuntimeID: &runtimeID, LeaseOwner: "lease-1", Payload: map[string]any{"command": []any{"sh", "-c", "printf first > /workspace/count"}, "workspacePath": ".monkeys/runtimes/runtime-1", "logicalWorkspacePath": "/workspace"}}
	cfg := Config{Mode: "worker", Workspace: workspace}
	if err := executeClaimedTask(t.Context(), client, cfg, "token", task); err != nil {
		t.Fatal(err)
	}
	task.LeaseOwner = "lease-2"
	task.Payload["command"] = []any{"sh", "-c", "printf second > /workspace/count"}
	if err := executeClaimedTask(t.Context(), client, cfg, "token", task); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(filepath.Join(workspace, ".monkeys", "runtimes", "runtime-1", "count"))
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != "first" {
		t.Fatalf("task was reexecuted: %q", value)
	}
	if completions != 2 {
		t.Fatalf("expected both leases acknowledged, got %d", completions)
	}
}

func TestRegisterReusesAgentInstanceIDAfterLostResponse(t *testing.T) {
	t.Setenv("MONKEYS_DISCOVER_PUBLIC_IP", "false")
	instanceIDs := make([]string, 0, 2)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body RegisterRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		instanceIDs = append(instanceIDs, body.AgentInstanceID)
		requests++
		writer.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = writer.Write([]byte(`{"message":"response lost"}`))
			return
		}
		_, _ = writer.Write([]byte(`{"code":0,"msg":"ok","data":{"node":{"id":"node-1","name":"worker-1"},"agentToken":"mnag_token","plan":{}}}`))
	}))
	defer server.Close()
	statePath := filepath.Join(t.TempDir(), "agent-state.json")
	cfg := Config{ServerURL: server.URL, BootstrapToken: "mnbt_token", StatePath: statePath, Mode: "worker", Workspace: t.TempDir()}
	if _, err := Register(t.Context(), cfg); err == nil {
		t.Fatal("first registration unexpectedly succeeded")
	}
	if _, err := Register(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if len(instanceIDs) != 2 || instanceIDs[0] == "" || instanceIDs[0] != instanceIDs[1] {
		t.Fatalf("registration identity was not stable: %#v", instanceIDs)
	}
}

func TestExecuteClaimedTaskReexecutesCachedRetryableFailure(t *testing.T) {
	completed := 0
	failed := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/compute/node-agent/tasks/task-retry/complete":
			completed++
		case "/api/compute/node-agent/tasks/task-retry/fail":
			failed++
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":0,"msg":"ok","data":{}}`))
	}))
	defer server.Close()
	c, _ := newClient(server.URL)
	workspace := t.TempDir()
	results := newTaskResultStore(Config{Workspace: workspace})
	if err := results.put("task-retry", cachedTaskOutcome{Status: "failed", ErrorMessage: "temporary failure", Retryable: true, CompletedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	runtimeID := "runtime-retry"
	task := ClaimedTask{ID: "task-retry", TaskType: "process.exec", RuntimeID: &runtimeID, LeaseOwner: "lease-2", Payload: map[string]any{"command": []any{"sh", "-c", "printf retried > /workspace/result"}, "workspacePath": ".monkeys/runtimes/runtime-retry", "logicalWorkspacePath": "/workspace"}}
	if err := executeClaimedTask(t.Context(), c, Config{Mode: "worker", Workspace: workspace}, "token", task); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(filepath.Join(workspace, ".monkeys", "runtimes", "runtime-retry", "result"))
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != "retried" || completed != 1 || failed != 0 {
		t.Fatalf("retryable task was not reexecuted: value=%q completed=%d failed=%d", value, completed, failed)
	}
}

func TestDispatchHostUsesKubernetesTaskExecutor(t *testing.T) {
	_, err := dispatchClaimedTask(t.Context(), Config{Mode: "host"}, ClaimedTask{TaskType: "process.exec", Payload: map[string]any{}})
	if err == nil || err.Error() != "process.exec is not allowed in host mode" {
		t.Fatalf("host did not use Kubernetes allowlist: %v", err)
	}
}

func TestFailedTaskAcknowledgementIncludesExplicitRetryability(t *testing.T) {
	requestReceived := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requestReceived <- body
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":0,"msg":"ok","data":{}}`))
	}))
	defer server.Close()
	c, _ := newClient(server.URL)
	task := ClaimedTask{ID: "task-permanent", LeaseOwner: "lease-1"}
	outcome := cachedTaskOutcome{Status: "failed", ErrorMessage: "invalid manifest", Retryable: false}
	if err := acknowledgeTaskOutcome(t.Context(), c, "token", task, outcome, newTaskResultStore(Config{Workspace: t.TempDir()})); err != nil {
		t.Fatal(err)
	}
	body := <-requestReceived
	if retryable, exists := body["retryable"]; !exists || retryable != false {
		t.Fatalf("retryable must be explicitly false: %#v", body)
	}
}

func TestRunAgentLoopsPollsTasksWhileHeartbeatIsBlocked(t *testing.T) {
	t.Setenv("MONKEYS_DISCOVER_PUBLIC_IP", "false")
	originalDiscover := discoverHeartbeatFacts
	discoverHeartbeatFacts = func(context.Context, Config) Facts { return Facts{Labels: map[string]string{}} }
	t.Cleanup(func() { discoverHeartbeatFacts = originalDiscover })
	heartbeatStarted := make(chan struct{})
	heartbeatRelease := make(chan struct{})
	taskPolled := make(chan struct{}, 1)
	var heartbeatOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/compute/node-agent/heartbeat":
			heartbeatOnce.Do(func() { close(heartbeatStarted) })
			<-heartbeatRelease
			_, _ = writer.Write([]byte(`{"code":0,"msg":"ok","data":{"plan":{}}}`))
		case "/api/compute/node-agent/tasks/claim":
			select {
			case taskPolled <- struct{}{}:
			default:
			}
			_, _ = writer.Write([]byte(`{"code":0,"msg":"ok","data":{"items":[]}}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c, _ := newClient(server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runAgentLoops(ctx, c, Config{Mode: "worker", Workspace: t.TempDir(), Interval: time.Hour}, "token", 10*time.Millisecond)
	}()
	select {
	case <-heartbeatStarted:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("heartbeat did not start")
	}
	select {
	case <-taskPolled:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("task polling was blocked by heartbeat")
	}
	cancel()
	close(heartbeatRelease)
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("unexpected run result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("agent loops did not stop")
	}
}
