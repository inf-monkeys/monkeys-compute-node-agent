package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestKubernetesApplyRequiresRuntimeOwnershipAndNamespace(t *testing.T) {
	client := &kubernetesClient{}
	base := map[string]any{
		"runtimeId": "runtime-1",
		"namespace": "models",
	}

	t.Run("ownership", func(t *testing.T) {
		payload := cloneMap(base)
		payload["manifests"] = []any{map[string]any{
			"apiVersion": "v1", "kind": "Secret",
			"metadata": map[string]any{"name": "runtime-secret", "namespace": "models"},
		}}
		_, err := client.applyRuntime(context.Background(), payload)
		if err == nil || !strings.Contains(err.Error(), "must be owned by runtime") {
			t.Fatalf("expected ownership error, got %v", err)
		}
	})

	t.Run("namespace", func(t *testing.T) {
		payload := cloneMap(base)
		payload["manifests"] = []any{map[string]any{
			"apiVersion": "v1", "kind": "Secret",
			"metadata": map[string]any{
				"name": "runtime-secret", "namespace": "other",
				"labels": map[string]any{monkeysRuntimeLabel: "runtime-1"},
			},
		}}
		_, err := client.applyRuntime(context.Background(), payload)
		if err == nil || !strings.Contains(err.Error(), "does not match runtime namespace") {
			t.Fatalf("expected namespace error, got %v", err)
		}
	})
}

func TestKubernetesApplyCreatesAndReplacesRuntimeResources(t *testing.T) {
	var mutex sync.Mutex
	methods := make([]string, 0)
	client, _ := newTestKubernetesClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		methods = append(methods, request.Method+" "+request.URL.Path)
		mutex.Unlock()
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/secrets/runtime-secret"):
			response.WriteHeader(http.StatusNotFound)
			_, _ = response.Write([]byte(`{"kind":"Status","reason":"NotFound"}`))
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/secrets"):
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(`{"metadata":{"resourceVersion":"1"}}`))
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/services/runtime-service"):
			_, _ = response.Write([]byte(`{"metadata":{"resourceVersion":"5"},"spec":{"clusterIP":"10.0.0.8","ports":[{"name":"http","port":80,"nodePort":30100}]}}`))
		case request.Method == http.MethodPut && strings.HasSuffix(request.URL.Path, "/services/runtime-service"):
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if stringValue(objectValue(body["metadata"])["resourceVersion"]) != "5" {
				t.Errorf("resourceVersion was not preserved: %#v", body)
			}
			spec := objectValue(body["spec"])
			if stringValue(spec["clusterIP"]) != "10.0.0.8" {
				t.Errorf("clusterIP was not preserved: %#v", spec)
			}
			ports := objectSlice(spec["ports"])
			if len(ports) != 1 || intValue(ports[0]["nodePort"]) != 30100 {
				t.Errorf("nodePort was not preserved: %#v", ports)
			}
			_, _ = response.Write([]byte(`{"metadata":{"resourceVersion":"6"}}`))
		default:
			t.Errorf("unexpected Kubernetes request %s %s", request.Method, request.URL.Path)
			response.WriteHeader(http.StatusInternalServerError)
			_, _ = response.Write([]byte(`{"message":"unexpected"}`))
		}
	}))

	payload := map[string]any{
		"runtimeId": "runtime-1", "namespace": "models",
		"manifests": []any{
			map[string]any{
				"apiVersion": "v1", "kind": "Secret",
				"metadata": map[string]any{"name": "runtime-secret", "namespace": "models", "labels": map[string]any{monkeysRuntimeLabel: "runtime-1"}},
			},
			map[string]any{
				"apiVersion": "v1", "kind": "Service",
				"metadata": map[string]any{"name": "runtime-service", "namespace": "models", "labels": map[string]any{monkeysRuntimeLabel: "runtime-1"}},
				"spec":     map[string]any{"ports": []any{map[string]any{"name": "http", "port": 80}}},
			},
		},
	}
	result, err := client.applyRuntime(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	results := objectSlice(result["results"])
	if len(results) != 2 || results[0]["operation"] != "created" || results[1]["operation"] != "updated" {
		t.Fatalf("unexpected apply result: %#v", result)
	}
	if len(methods) != 5 {
		t.Fatalf("unexpected request count and methods: %#v", methods)
	}
}

func TestClusterTaskModeAllowlistAndExecRequirement(t *testing.T) {
	if _, err := executeClusterTask(context.Background(), "process.exec", nil); err == nil || !strings.Contains(err.Error(), "not allowed in cluster mode") {
		t.Fatalf("expected strict cluster-mode allowlist, got %v", err)
	}
	if commandExists("kubectl") {
		t.Skip("test environment has kubectl")
	}
	_, err := executeClusterTask(context.Background(), "k8s.exec-runtime", map[string]any{
		"runtimeId": "runtime-1", "namespace": "default", "command": []any{"echo", "ok"},
	})
	if err == nil || !strings.Contains(err.Error(), "requires kubectl") {
		t.Fatalf("expected explicit kubectl requirement, got %v", err)
	}
}
