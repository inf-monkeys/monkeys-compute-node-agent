package agent

import (
	"os"
	"os/exec"
	"path/filepath"
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

func TestK3sJoinExtraArgsCannotEscapeTheInstallerArgumentList(t *testing.T) {
	directory := t.TempDir()
	installerPath := filepath.Join(directory, "installer.sh")
	curlPath := filepath.Join(directory, "curl")
	sentinelPath := filepath.Join(directory, "injected")
	capturePath := filepath.Join(directory, "arguments")
	if err := os.WriteFile(installerPath, []byte("printf '%s\\n' \"$0\" \"$@\" > \"$CAPTURE_PATH\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(curlPath, []byte("#!/bin/sh\ncat \"$FAKE_INSTALLER_PATH\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_INSTALLER_PATH", installerPath)
	t.Setenv("CAPTURE_PATH", capturePath)

	maliciousArgument := "; touch " + sentinelPath
	command := buildK3sJoinAgentCommand(map[string]any{
		"serverUrl": "https://k3s.example.test:6443",
		"token":     "test-token",
		"extraArgs": []any{"--node-label", maliciousArgument},
	})
	result, err := exec.Command(command[0], command[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("execute generated K3s join command: %v: %s", err, result)
	}
	if _, err := os.Stat(sentinelPath); !os.IsNotExist(err) {
		t.Fatalf("extraArgs escaped the installer argument list: %v", err)
	}
	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(captured), maliciousArgument) {
		t.Fatalf("expected the literal extra argument to reach the installer, got %q", captured)
	}
}

func TestExecuteActionRejectsUnsafeK3sExtraArgsBeforeExecution(t *testing.T) {
	for _, actionType := range []string{"k3s.install-server", "k3s.join-agent"} {
		result := executeAction(t.Context(), Config{AllowInstall: true}, PlanAction{
			Type: actionType,
			Payload: map[string]any{
				"extraArgs": []any{"--node-label", "gpu=true;touch=/tmp/injected"},
			},
		})
		if result.Success || !strings.Contains(result.Message, "Invalid K3s extraArgs") {
			t.Fatalf("expected %s to reject unsafe extraArgs, got %+v", actionType, result)
		}
	}

	result := executeAction(t.Context(), Config{DryRun: true}, PlanAction{
		Type: "k3s.join-agent",
		Payload: map[string]any{
			"extraArgs": []any{"--kubelet-arg", "node-labels=gpu.example/type:a100"},
		},
	})
	if !result.Success || !result.Planned {
		t.Fatalf("expected safe K3s extraArgs to remain supported, got %+v", result)
	}
}

func TestExecuteActionPlansAgentUpdateWithoutAllowFlag(t *testing.T) {
	cfg := Config{ServerURL: "http://control-plane.local", DryRun: true}
	result := executeAction(t.Context(), cfg, PlanAction{Type: "agent.update", Payload: map[string]any{
		"releaseBaseUrl": "https://agents.example.test/kernel-agent/releases",
		"version":        "v1.2.3",
	}})
	if !result.Success || !result.Planned || !result.DryRun {
		t.Fatalf("expected dry-run planned success, got: %+v", result)
	}
	if len(result.Command) != 3 || result.Command[0] != "sh" {
		t.Fatalf("unexpected command: %+v", result.Command)
	}
	command := result.Command[2]
	for _, expected := range []string{
		"https://agents.example.test/kernel-agent/releases/download/v1.2.3/monkeys-compute-node-agent_linux_",
		"https://agents.example.test/kernel-agent/releases/download/v1.2.3/SHA256SUMS",
		"sha256sum -c -",
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

func TestParseKubernetesVersions(t *testing.T) {
	if version := parseKubectlServerVersion(`{"serverVersion":{"gitVersion":"v1.30.2+k3s1"}}`); version != "v1.30.2+k3s1" {
		t.Fatalf("unexpected kubectl server version: %s", version)
	}
	if version := parseK3sVersion("k3s version v1.30.2+k3s1 (abcdef)\ngo version go1.22.5"); version != "v1.30.2+k3s1" {
		t.Fatalf("unexpected k3s version: %s", version)
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
