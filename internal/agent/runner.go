package agent

import (
	"context"
	"fmt"
	"log"
	"time"
)

func Register(ctx context.Context, cfg Config) (RegisterResult, error) {
	if cfg.BootstrapToken == "" {
		return RegisterResult{}, fmt.Errorf("bootstrap token is required")
	}
	c, err := newClient(cfg.ServerURL)
	if err != nil {
		return RegisterResult{}, err
	}
	facts := Discover(ctx, cfg.Version)
	result, err := c.register(ctx, registerFromFacts(facts, cfg))
	if err != nil {
		return RegisterResult{}, err
	}
	if err := saveState(cfg.StatePath, State{
		ServerURL:  cfg.ServerURL,
		NodeID:     result.Node.ID,
		NodeName:   result.Node.Name,
		AgentToken: result.AgentToken,
	}); err != nil {
		return RegisterResult{}, err
	}
	return result, nil
}

func Once(ctx context.Context, cfg Config) error {
	state, err := loadState(cfg.StatePath)
	if err != nil {
		return err
	}
	if cfg.ServerURL == "" {
		cfg.ServerURL = state.ServerURL
	}
	c, err := newClient(cfg.ServerURL)
	if err != nil {
		return err
	}
	facts := Discover(ctx, cfg.Version)
	heartbeat, err := c.heartbeat(ctx, state.AgentToken, heartbeatFromFacts(facts, cfg.Version))
	if err != nil {
		return err
	}
	return executePlan(ctx, c, cfg, state.AgentToken, heartbeat.Plan)
}

func Run(ctx context.Context, cfg Config) error {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	state, err := loadState(cfg.StatePath)
	if err != nil {
		return err
	}
	if cfg.ServerURL == "" {
		cfg.ServerURL = state.ServerURL
	}
	c, err := newClient(cfg.ServerURL)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		if err := heartbeatAndPlan(ctx, c, cfg, state.AgentToken); err != nil {
			log.Printf("heartbeat failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func heartbeatAndPlan(ctx context.Context, c *client, cfg Config, token string) error {
	facts := Discover(ctx, cfg.Version)
	heartbeat, err := c.heartbeat(ctx, token, heartbeatFromFacts(facts, cfg.Version))
	if err != nil {
		return err
	}
	return executePlan(ctx, c, cfg, token, heartbeat.Plan)
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
		} else {
			failed = append(failed, action.Type)
		}
		if err := c.event(ctx, token, plan.ID, event); err != nil {
			return err
		}
		if !result.Success {
			break
		}
	}
	return c.complete(ctx, token, plan.ID, map[string]any{
		"completedActions": completed,
		"plannedActions":   planned,
		"failedActions":    failed,
		"completedAt":      time.Now().UnixMilli(),
	})
}
