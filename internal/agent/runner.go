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
	return executePlan(ctx, c, state.AgentToken, heartbeat.Plan)
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
		if err := heartbeatAndPlan(ctx, c, state.AgentToken, cfg.Version); err != nil {
			log.Printf("heartbeat failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func heartbeatAndPlan(ctx context.Context, c *client, token string, version string) error {
	facts := Discover(ctx, version)
	heartbeat, err := c.heartbeat(ctx, token, heartbeatFromFacts(facts, version))
	if err != nil {
		return err
	}
	return executePlan(ctx, c, token, heartbeat.Plan)
}

func executePlan(ctx context.Context, c *client, token string, plan Plan) error {
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
	for _, action := range plan.Actions {
		switch action.Type {
		case "agent.register", "inspect", "noop":
			completed = append(completed, action.Type)
			if err := c.event(ctx, token, plan.ID, EventRequest{
				EventType:  "node.plan.action.completed",
				Severity:   "info",
				Message:    "Action completed: " + action.Type,
				Payload:    map[string]any{"actionType": action.Type},
				OccurredAt: time.Now().UnixMilli(),
			}); err != nil {
				return err
			}
		default:
			if err := c.event(ctx, token, plan.ID, EventRequest{
				EventType:  "node.plan.action.unsupported",
				Severity:   "warning",
				Message:    "Unsupported action: " + action.Type,
				Payload:    map[string]any{"actionType": action.Type},
				OccurredAt: time.Now().UnixMilli(),
			}); err != nil {
				return err
			}
		}
	}
	return c.complete(ctx, token, plan.ID, map[string]any{
		"completedActions": completed,
		"completedAt":      time.Now().UnixMilli(),
	})
}
