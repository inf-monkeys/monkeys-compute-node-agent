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

func TestExecuteActionPlansRuntimeApplyWithoutAllowFlag(t *testing.T) {
	cfg := Config{DryRun: true}
	result := executeAction(t.Context(), cfg, PlanAction{
		Type: "k8s.apply-runtime",
		Payload: map[string]any{
			"runtimeId":    "runtime-1",
			"namespace":    "default",
			"manifestYaml": "apiVersion: v1\nkind: Secret\nmetadata:\n  name: runtime-env\n",
		},
	})
	if !result.Success || !result.Planned || !result.DryRun {
		t.Fatalf("expected dry-run planned success, got: %+v", result)
	}
	if result.Details["runtimeId"] != "runtime-1" {
		t.Fatalf("expected sanitized runtime details, got: %+v", result.Details)
	}
	if _, ok := result.Details["manifestYaml"]; ok {
		t.Fatalf("manifest yaml must not be echoed in event details: %+v", result.Details)
	}
}

func TestExecuteActionRejectsRuntimeApplyWithoutManifest(t *testing.T) {
	result := executeAction(t.Context(), Config{DryRun: true}, PlanAction{Type: "k8s.apply-runtime"})
	if result.Success {
		t.Fatalf("expected missing manifest to fail: %+v", result)
	}
}

func TestRewriteKubeconfigServer(t *testing.T) {
	input := "apiVersion: v1\nclusters:\n- cluster:\n    certificate-authority-data: abc\n    server: https://127.0.0.1:6443\n  name: default\n"
	output := rewriteKubeconfigServer(input, "https://10.0.0.10:6443")
	if !strings.Contains(output, "server: https://10.0.0.10:6443") {
		t.Fatalf("expected server endpoint to be rewritten: %s", output)
	}
	if strings.Contains(output, "127.0.0.1") {
		t.Fatalf("local endpoint leaked into kubeconfig: %s", output)
	}
}

func TestResolveKubeconfigEndpointPrefersExplicitEndpoint(t *testing.T) {
	endpoint := resolveKubeconfigEndpoint(map[string]any{"apiServerEndpoint": "10.0.0.10:6443"})
	if endpoint != "https://10.0.0.10:6443" {
		t.Fatalf("unexpected endpoint: %s", endpoint)
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
