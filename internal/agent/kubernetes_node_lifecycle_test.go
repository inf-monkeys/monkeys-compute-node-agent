package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestValidateKubernetesNodeName(t *testing.T) {
	valid := []string{
		"node-1",
		"gpu-01.cluster.local",
		strings.Repeat("a", 63) + ".example",
	}
	for _, nodeName := range valid {
		t.Run("valid_"+nodeName, func(t *testing.T) {
			selected, err := validateKubernetesNodeName(map[string]any{"nodeName": nodeName})
			if err != nil || selected != nodeName {
				t.Fatalf("expected valid node name %q, got %q, %v", nodeName, selected, err)
			}
		})
	}

	invalid := []any{
		nil,
		123,
		"",
		" node-1",
		"NODE-1",
		"node_1",
		"node-1;rm",
		"-node",
		"node-",
		strings.Repeat("a", 64) + ".example",
		strings.Repeat("a", 254),
	}
	for index, nodeName := range invalid {
		t.Run("invalid_"+string(rune('a'+index)), func(t *testing.T) {
			if _, err := validateKubernetesNodeName(map[string]any{"nodeName": nodeName}); err == nil {
				t.Fatalf("expected invalid node name rejection for %#v", nodeName)
			}
		})
	}
}

func TestParseKubernetesDrainOptionsIsStrictAndBounded(t *testing.T) {
	options, err := parseKubernetesDrainOptions(map[string]any{
		"ignoreDaemonSets": true, "deleteEmptyDirData": true, "force": true,
		"gracePeriodSeconds": float64(120), "timeoutSeconds": json.Number("600"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !options.ignoreDaemonSets || !options.deleteEmptyDirData || !options.force || options.gracePeriodSeconds != 120 || options.timeoutSeconds != 600 {
		t.Fatalf("unexpected drain options: %#v", options)
	}

	invalid := []map[string]any{
		{"ignoreDaemonSets": "true"},
		{"deleteEmptyDirData": 1},
		{"force": nil, "timeoutSeconds": 0},
		{"gracePeriodSeconds": -1},
		{"gracePeriodSeconds": maxDrainGraceSeconds + 1},
		{"timeoutSeconds": maxDrainTimeoutSeconds + 1},
		{"timeoutSeconds": 1.5},
		{"timeoutSeconds": "300"},
	}
	for index, payload := range invalid {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			if _, err := parseKubernetesDrainOptions(payload); err == nil {
				t.Fatalf("expected invalid options rejection: %#v", payload)
			}
		})
	}
}

func TestKubernetesCordonIsIdempotent(t *testing.T) {
	requestCount := 0
	client, _ := newTestKubernetesClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestCount++
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/nodes/node-1" {
			t.Errorf("unexpected Kubernetes request %s %s", request.Method, request.URL.Path)
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"metadata":{"resourceVersion":"7"},"spec":{"unschedulable":true}}`))
	}))

	result, err := client.executeNodeTask(t.Context(), "k8s.cordon-node", "node-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result["changed"] != false || result["unschedulable"] != true || requestCount != 1 {
		t.Fatalf("idempotent cordon produced an unexpected result: %#v, requests=%d", result, requestCount)
	}
}

func TestKubernetesUncordonPatchesWithResourceVersion(t *testing.T) {
	requestCount := 0
	client, _ := newTestKubernetesClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestCount++
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path != "/api/v1/nodes/gpu-1" {
			t.Errorf("unexpected Kubernetes path %s", request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
			return
		}
		switch request.Method {
		case http.MethodGet:
			_, _ = response.Write([]byte(`{"metadata":{"resourceVersion":"11"},"spec":{"unschedulable":true}}`))
		case http.MethodPatch:
			if request.Header.Get("Content-Type") != "application/merge-patch+json" {
				t.Errorf("unexpected patch content type: %s", request.Header.Get("Content-Type"))
			}
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if stringValue(objectValue(body["metadata"])["resourceVersion"]) != "11" || objectValue(body["spec"])["unschedulable"] != false {
				t.Errorf("unexpected uncordon patch: %#v", body)
			}
			_, _ = response.Write([]byte(`{"metadata":{"resourceVersion":"12"},"spec":{"unschedulable":false}}`))
		default:
			t.Errorf("unexpected Kubernetes method %s", request.Method)
			response.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))

	result, err := client.executeNodeTask(t.Context(), "k8s.uncordon-node", "gpu-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result["changed"] != true || result["unschedulable"] != false || requestCount != 3 {
		t.Fatalf("unexpected uncordon result: %#v, requests=%d", result, requestCount)
	}
}

func TestKubernetesDrainCordonsEvictsAndWaits(t *testing.T) {
	var mutex sync.Mutex
	requests := make([]string, 0)
	evictionBody := map[string]any{}
	client, _ := newTestKubernetesClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		requests = append(requests, request.Method+" "+request.URL.RequestURI())
		mutex.Unlock()
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/nodes/node-1":
			_, _ = response.Write([]byte(`{"metadata":{"resourceVersion":"1"},"spec":{"unschedulable":false}}`))
		case request.Method == http.MethodPatch && request.URL.Path == "/api/v1/nodes/node-1":
			_, _ = response.Write([]byte(`{"metadata":{"resourceVersion":"2"},"spec":{"unschedulable":true}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/pods":
			if request.URL.Query().Get("fieldSelector") != "spec.nodeName=node-1" {
				t.Errorf("node pod list did not use a field selector: %s", request.URL.RawQuery)
			}
			_, _ = response.Write([]byte(`{"metadata":{},"items":[
				{"metadata":{"name":"app-1","namespace":"apps","ownerReferences":[{"kind":"ReplicaSet","controller":true}]},"spec":{"nodeName":"node-1"},"status":{"phase":"Running"}},
				{"metadata":{"name":"log-agent","namespace":"monitoring","ownerReferences":[{"kind":"DaemonSet","controller":true}]},"spec":{"nodeName":"node-1"},"status":{"phase":"Running"}},
				{"metadata":{"name":"static-api","namespace":"kube-system","annotations":{"kubernetes.io/config.mirror":"mirror-id"}},"spec":{"nodeName":"node-1"},"status":{"phase":"Running"}}
			]}`))
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/namespaces/apps/pods/app-1/eviction":
			if err := json.NewDecoder(request.Body).Decode(&evictionBody); err != nil {
				t.Fatal(err)
			}
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(`{"kind":"Status","status":"Success"}`))
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/namespaces/apps/pods/app-1":
			response.WriteHeader(http.StatusNotFound)
			_, _ = response.Write([]byte(`{"kind":"Status","reason":"NotFound"}`))
		default:
			t.Errorf("unexpected Kubernetes request %s %s", request.Method, request.URL.RequestURI())
			response.WriteHeader(http.StatusInternalServerError)
			_, _ = response.Write([]byte(`{"message":"unexpected"}`))
		}
	}))

	result, err := client.executeNodeTask(t.Context(), "k8s.drain-node", "node-1", map[string]any{
		"ignoreDaemonSets": true, "deleteEmptyDirData": false, "force": false,
		"gracePeriodSeconds": 30, "timeoutSeconds": 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["drained"] != true || result["unschedulable"] != true || result["evictedPodCount"] != 1 {
		t.Fatalf("unexpected drain result: %#v", result)
	}
	if selected := result["skippedDaemonSetPods"].([]string); len(selected) != 1 || selected[0] != "monitoring/log-agent" {
		t.Fatalf("unexpected skipped DaemonSet pods: %#v", selected)
	}
	if stringValue(evictionBody["apiVersion"]) != "policy/v1" || intValue(objectValue(evictionBody["deleteOptions"])["gracePeriodSeconds"]) != 30 {
		t.Fatalf("unexpected eviction body: %#v", evictionBody)
	}
	joined := strings.Join(requests, "\n")
	if !strings.Contains(joined, "POST /api/v1/namespaces/apps/pods/app-1/eviction") {
		t.Fatalf("eviction API was not called: %s", joined)
	}
}

func TestKubernetesDrainRespectsPDBAndTimesOut(t *testing.T) {
	evictionAttempts := 0
	client, _ := newTestKubernetesClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/nodes/node-1":
			_, _ = response.Write([]byte(`{"metadata":{"resourceVersion":"1"},"spec":{"unschedulable":true}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/pods":
			_, _ = response.Write([]byte(`{"items":[{"metadata":{"name":"api-1","namespace":"apps","ownerReferences":[{"kind":"ReplicaSet","controller":true}]},"spec":{"nodeName":"node-1"},"status":{"phase":"Running"}}]}`))
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/namespaces/apps/pods/api-1/eviction":
			evictionAttempts++
			response.WriteHeader(http.StatusTooManyRequests)
			_, _ = response.Write([]byte(`{"kind":"Status","reason":"TooManyRequests","message":"Cannot evict pod as it would violate the pod's disruption budget."}`))
		default:
			t.Errorf("unexpected Kubernetes request %s %s", request.Method, request.URL.Path)
			response.WriteHeader(http.StatusInternalServerError)
		}
	}))

	_, err := client.executeNodeTask(context.Background(), "k8s.drain-node", "node-1", map[string]any{"timeoutSeconds": 1})
	if err == nil || !strings.Contains(err.Error(), "PodDisruptionBudget") || !strings.Contains(err.Error(), "timed out after 1 seconds") {
		t.Fatalf("expected explicit PDB timeout, got %v", err)
	}
	kubernetesErr, ok := err.(*kubernetesError)
	if !ok || !kubernetesErr.Retryable() || evictionAttempts < 2 {
		t.Fatalf("expected a retryable PDB timeout after retries, got %T %#v, attempts=%d", err, err, evictionAttempts)
	}
}

func TestSelectDrainPodsEnforcesSafetyOptions(t *testing.T) {
	pod := func(name, ownerKind string, emptyDir bool) map[string]any {
		metadata := map[string]any{"name": name, "namespace": "apps"}
		if ownerKind != "" {
			metadata["ownerReferences"] = []any{map[string]any{"kind": ownerKind, "controller": true}}
		}
		spec := map[string]any{"nodeName": "node-1"}
		if emptyDir {
			spec["volumes"] = []any{map[string]any{"name": "scratch", "emptyDir": map[string]any{}}}
		}
		return map[string]any{"metadata": metadata, "spec": spec, "status": map[string]any{"phase": "Running"}}
	}

	if _, _, err := selectDrainPods([]map[string]any{pod("daemon", "DaemonSet", false)}, kubernetesDrainOptions{}); err == nil || !strings.Contains(err.Error(), "ignoreDaemonSets=true") {
		t.Fatalf("expected DaemonSet protection, got %v", err)
	}
	if _, _, err := selectDrainPods([]map[string]any{pod("scratch", "ReplicaSet", true)}, kubernetesDrainOptions{ignoreDaemonSets: true}); err == nil || !strings.Contains(err.Error(), "deleteEmptyDirData=true") {
		t.Fatalf("expected emptyDir protection, got %v", err)
	}
	if _, _, err := selectDrainPods([]map[string]any{pod("bare", "", false)}, kubernetesDrainOptions{ignoreDaemonSets: true}); err == nil || !strings.Contains(err.Error(), "force=true") {
		t.Fatalf("expected unmanaged pod protection, got %v", err)
	}
	candidates, _, err := selectDrainPods([]map[string]any{pod("scratch", "ReplicaSet", true), pod("bare", "", false)}, kubernetesDrainOptions{
		ignoreDaemonSets: true, deleteEmptyDirData: true, force: true,
	})
	if err != nil || len(candidates) != 2 {
		t.Fatalf("expected explicitly allowed pods, got %#v, %v", candidates, err)
	}
}

func TestHostKubernetesNodeLifecycleUsesArgumentSafeKubectl(t *testing.T) {
	logPath := installFakeKubectl(t, `#!/bin/sh
printf '%s\n' "$*" >> "$MONKEYS_TEST_KUBECTL_LOG"
printf 'ok\n'
`)

	cordon, err := executeHostKubernetesTask(t.Context(), "k8s.cordon-node", map[string]any{"nodeName": "gpu-1"})
	if err != nil {
		t.Fatal(err)
	}
	if cordon["unschedulable"] != true {
		t.Fatalf("unexpected host cordon result: %#v", cordon)
	}
	_, err = executeHostKubernetesTask(t.Context(), "k8s.drain-node", map[string]any{
		"nodeName": "gpu-1", "ignoreDaemonSets": true, "deleteEmptyDirData": true,
		"force": false, "gracePeriodSeconds": 45, "timeoutSeconds": 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(commands)), "\n")
	if len(lines) != 2 || lines[0] != "cordon gpu-1" {
		t.Fatalf("unexpected host node commands: %q", lines)
	}
	wantDrain := "drain gpu-1 --ignore-daemonsets=true --delete-emptydir-data=true --force=false --grace-period=45 --timeout=120s"
	if lines[1] != wantDrain {
		t.Fatalf("unexpected drain command: %q, want %q", lines[1], wantDrain)
	}
}

func TestHostKubernetesNodeLifecycleFallsBackToK3sKubectl(t *testing.T) {
	directory := t.TempDir()
	logPath := directory + "/k3s.log"
	scriptPath := directory + "/k3s"
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$MONKEYS_TEST_K3S_LOG\"\nprintf 'ok\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("MONKEYS_TEST_K3S_LOG", logPath)

	result, err := executeHostKubernetesTask(t.Context(), "k8s.uncordon-node", map[string]any{"nodeName": "gpu-2"})
	if err != nil {
		t.Fatal(err)
	}
	if result["unschedulable"] != false {
		t.Fatalf("unexpected k3s uncordon result: %#v", result)
	}
	command, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(command)) != "kubectl uncordon gpu-2" {
		t.Fatalf("unexpected k3s command: %q", command)
	}
}

func TestHostKubernetesNodeLifecycleRejectsInvalidInputBeforeKubectl(t *testing.T) {
	logPath := installFakeKubectl(t, "#!/bin/sh\nprintf invoked >> \"$MONKEYS_TEST_KUBECTL_LOG\"\n")
	_, err := executeHostKubernetesTask(t.Context(), "k8s.cordon-node", map[string]any{"nodeName": "node-1; shutdown"})
	if err == nil {
		t.Fatal("expected unsafe node name rejection")
	}
	_, err = executeHostKubernetesTask(t.Context(), "k8s.drain-node", map[string]any{"nodeName": "node-1", "timeoutSeconds": 0})
	if err == nil {
		t.Fatal("expected out-of-range timeout rejection")
	}
	if _, statErr := os.Stat(logPath); !os.IsNotExist(statErr) {
		t.Fatalf("kubectl ran before validation: %v", statErr)
	}
}

func TestClusterNodeTaskAllowlistValidatesBeforeInClusterSetup(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	_, err := executeClusterTask(t.Context(), "k8s.cordon-node", map[string]any{"nodeName": "invalid/name"})
	if err == nil || !strings.Contains(err.Error(), "valid lowercase Kubernetes DNS") {
		t.Fatalf("expected node name validation, got %v", err)
	}
	_, err = executeClusterTask(t.Context(), "k8s.not-a-node-operation", nil)
	if err == nil || !strings.Contains(err.Error(), "not allowed in cluster mode") {
		t.Fatalf("expected strict allowlist rejection, got %v", err)
	}
}

func TestListPodsOnNodeEncodesFieldSelector(t *testing.T) {
	client, _ := newTestKubernetesClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		parsed, err := url.QueryUnescape(request.URL.RawQuery)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(parsed, "fieldSelector=spec.nodeName=node-1") {
			t.Errorf("unexpected node pod query: %s", request.URL.RawQuery)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"metadata":{},"items":[]}`))
	}))
	items, err := client.listPodsOnNode(t.Context(), "node-1")
	if err != nil || len(items) != 0 {
		t.Fatalf("unexpected node pod list result: %#v, %v", items, err)
	}
}
