package agent

import (
	"strings"
	"testing"
)

func TestExecuteActionSupportsReadOnlyPreflight(t *testing.T) {
	cfg := Config{DryRun: true}
	for _, actionType := range []string{"noop", "inspect", "agent.register", "k3s.preflight", "hami.preflight"} {
		result := executeAction(t.Context(), cfg, PlanAction{Type: actionType})
		if result.ActionType != actionType {
			t.Fatalf("unexpected action type: %+v", result)
		}
		if result.Message == "" {
			t.Fatalf("expected message for %s", actionType)
		}
	}
}

func TestExecuteActionPlansInstallActionsWithoutAllowFlag(t *testing.T) {
	cfg := Config{DryRun: true}
	result := executeAction(t.Context(), cfg, PlanAction{
		Type:    "k3s.install-server",
		Payload: map[string]any{"token": "secret", "nodeName": "gpu-01"},
	})
	if !result.Success || !result.Planned || !result.DryRun {
		t.Fatalf("expected dry-run planned success, got: %+v", result)
	}
	if len(result.Command) == 0 {
		t.Fatalf("expected command payload")
	}
	if result.Command[0] != "sh" {
		t.Fatalf("unexpected command prefix: %+v", result.Command)
	}
}

func TestExecuteActionPlansAgentUpdateWithoutAllowFlag(t *testing.T) {
	cfg := Config{ServerURL: "http://control-plane.local", DryRun: true}
	result := executeAction(t.Context(), cfg, PlanAction{Type: "agent.update"})
	if !result.Success || !result.Planned || !result.DryRun {
		t.Fatalf("expected dry-run planned success, got: %+v", result)
	}
	if len(result.Command) != 3 || result.Command[0] != "sh" {
		t.Fatalf("unexpected command: %+v", result.Command)
	}
	command := result.Command[2]
	for _, expected := range []string{
		"http://control-plane.local/api/compute/node-agent/download/monkeys-compute-node-agent_linux_",
		"install -m 0755",
		"systemctl restart",
	} {
		if !strings.Contains(command, expected) {
			t.Fatalf("expected update command to contain %q: %s", expected, command)
		}
	}
}

func TestExecuteActionReportsUnsupportedActions(t *testing.T) {
	result := executeAction(t.Context(), Config{}, PlanAction{Type: "k3s.install"})
	if result.Success {
		t.Fatalf("unsupported action must not succeed: %+v", result)
	}
	event := eventForActionResult(result)
	if event.EventType != "node.plan.action.unsupported" {
		t.Fatalf("unexpected event: %+v", event)
	}
	if event.Severity != "warning" {
		t.Fatalf("unexpected severity: %+v", event)
	}
}
