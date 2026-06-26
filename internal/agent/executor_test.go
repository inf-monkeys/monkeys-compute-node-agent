package agent

import "testing"

func TestExecuteActionSupportsReadOnlyPreflight(t *testing.T) {
	for _, actionType := range []string{"noop", "inspect", "agent.register", "k3s.preflight", "hami.preflight"} {
		result := executeAction(t.Context(), PlanAction{Type: actionType})
		if result.ActionType != actionType {
			t.Fatalf("unexpected action type: %+v", result)
		}
		if result.Message == "" {
			t.Fatalf("expected message for %s", actionType)
		}
	}
}

func TestExecuteActionReportsUnsupportedActions(t *testing.T) {
	result := executeAction(t.Context(), PlanAction{Type: "k3s.install"})
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
