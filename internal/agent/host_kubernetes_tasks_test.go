package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostKubernetesApplyDoesNotRequireExistingDeployment(t *testing.T) {
	logPath := installFakeKubectl(t, `#!/bin/sh
printf '%s\n' "$*" >> "$MONKEYS_TEST_KUBECTL_LOG"
printf 'runtime resources applied\n'
`)
	payload := validHostApplyPayload()
	result, err := executeHostKubernetesTask(t.Context(), "k8s.apply-runtime", payload)
	if err != nil {
		t.Fatal(err)
	}
	if result["success"] != true {
		t.Fatalf("unexpected result: %#v", result)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), "get deployment") || !strings.Contains(string(commands), "apply -f") {
		t.Fatalf("apply executed unexpected commands: %s", commands)
	}
}

func TestHostKubernetesApplyValidatesOwnershipBeforeKubectl(t *testing.T) {
	logPath := installFakeKubectl(t, "#!/bin/sh\nprintf invoked >> \"$MONKEYS_TEST_KUBECTL_LOG\"\n")
	payload := validHostApplyPayload()
	metadata := objectValue(objectSlice(payload["manifests"])[0]["metadata"])
	metadata["labels"] = map[string]any{}
	_, err := executeHostKubernetesTask(t.Context(), "k8s.apply-runtime", payload)
	if err == nil || !strings.Contains(err.Error(), "must be owned by runtime") {
		t.Fatalf("expected ownership rejection, got %v", err)
	}
	if _, statErr := os.Stat(logPath); !os.IsNotExist(statErr) {
		t.Fatalf("kubectl ran before validation: %v", statErr)
	}
}

func TestClusterModeFallsBackToKubectlOutsideCluster(t *testing.T) {
	installFakeKubectl(t, `#!/bin/sh
case "$*" in
  *"get deployment,pod"*) printf '{"items":[]}' ;;
  *) printf '{}';;
esac
`)
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	runtimeID := "runtime-1"
	result, err := dispatchClaimedTask(t.Context(), Config{Mode: "cluster"}, ClaimedTask{
		TaskType: "k8s.inspect-runtime", RuntimeID: &runtimeID,
		Payload: map[string]any{"runtimeId": runtimeID, "namespace": "default"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["exists"] != false {
		t.Fatalf("cluster kubectl fallback returned unexpected result: %#v", result)
	}
}

func TestClusterDiscoveryFallsBackToKubectlOutsideCluster(t *testing.T) {
	installFakeKubectl(t, `#!/bin/sh
case "$*" in
  "version -o json") printf '{"serverVersion":{"gitVersion":"v1.32.1"}}' ;;
  "get node -o jsonpath={.items[0].metadata.name}") printf 'node-a' ;;
  *"get deploy -A"*) printf '{"items":[]}' ;;
  *) printf '' ;;
esac
`)
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("MONKEYS_DISCOVER_PUBLIC_IP", "false")
	facts := discoverForMode(t.Context(), Config{Mode: "cluster"})
	if facts.Kubernetes["ready"] != true || facts.Kubernetes["accessMethod"] != "kubectl" {
		t.Fatalf("unexpected kubectl discovery: %#v", facts.Kubernetes)
	}
	capabilities := capabilitiesForFacts("cluster", "", facts)
	kubernetes := objectValue(capabilities["kubernetes"])
	if kubernetes["observe"] != true || kubernetes["clusterWide"] != true {
		t.Fatalf("unexpected cluster capabilities: %#v", capabilities)
	}
}

func validHostApplyPayload() map[string]any {
	return map[string]any{
		"runtimeId": "runtime-1", "namespace": "models", "manifestYaml": "apiVersion: apps/v1\nkind: Deployment",
		"manifests": []any{map[string]any{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": "runtime-1", "namespace": "models", "labels": map[string]any{monkeysRuntimeLabel: "runtime-1"}},
		}},
	}
}

func installFakeKubectl(t *testing.T, script string) string {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(directory, "kubectl.log")
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MONKEYS_TEST_KUBECTL_LOG", logPath)
	t.Setenv("KUBECONFIG", filepath.Join(directory, "config"))
	return logPath
}
