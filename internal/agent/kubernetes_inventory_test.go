package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestFullClusterSnapshotUsesBoundedConcurrentInventory(t *testing.T) {
	var mutex sync.Mutex
	active := 0
	maximum := 0
	paths := map[string]int{}
	client, _ := newTestKubernetesClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		active++
		if active > maximum {
			maximum = active
		}
		paths[request.URL.Path]++
		mutex.Unlock()
		defer func() {
			mutex.Lock()
			active--
			mutex.Unlock()
		}()
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/version" {
			_ = json.NewEncoder(response).Encode(map[string]any{"gitVersion": "v1.31.0", "platform": "linux/amd64"})
			return
		}
		time.Sleep(20 * time.Millisecond)
		items := []any{}
		if request.URL.Path == "/apis/apps/v1/deployments" {
			items = []any{map[string]any{
				"metadata": map[string]any{"name": "runtime-deploy", "namespace": "default", "labels": map[string]any{monkeysRuntimeLabel: "runtime-1"}},
				"spec":     map[string]any{"replicas": 1}, "status": map[string]any{"readyReplicas": 1, "availableReplicas": 1},
			}}
		}
		if request.URL.Path == "/api/v1/pods" {
			items = []any{map[string]any{
				"metadata": map[string]any{"name": "runtime-pod", "namespace": "default", "labels": map[string]any{monkeysRuntimeLabel: "runtime-1"}},
				"status":   map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
			}}
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"items": items, "metadata": map[string]any{}})
	}))

	snapshot, err := client.fullClusterSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Inventory["ready"] != true || stringValue(snapshot.Inventory["version"]) != "v1.31.0" {
		t.Fatalf("unexpected inventory: %#v", snapshot.Inventory)
	}
	if len(snapshot.Statuses) != 1 || snapshot.Statuses[0].Status != "running" {
		t.Fatalf("unexpected runtime statuses: %#v", snapshot.Statuses)
	}
	if maximum < 2 {
		t.Fatalf("expected inventory requests to run concurrently, max active=%d", maximum)
	}
	for _, path := range []string{"/api/v1/nodes", "/api/v1/namespaces", "/api/v1/pods", "/apis/apps/v1/deployments", "/api/v1/persistentvolumeclaims", "/api/v1/persistentvolumes"} {
		if paths[path] != 1 {
			t.Fatalf("expected one request for %s, got %d", path, paths[path])
		}
	}
}

func TestClusterCapabilitiesReflectInventoryReadiness(t *testing.T) {
	ready := clusterCapabilities(map[string]any{"ready": true, "accessMethod": "service-account"})
	kubernetes := objectValue(ready["kubernetes"])
	if kubernetes["clusterWide"] != true || kubernetes["apply"] != true || kubernetes["accessMethod"] != "service-account" {
		t.Fatalf("unexpected cluster capabilities: %#v", ready)
	}
	notReady := clusterCapabilities(map[string]any{"ready": false})
	if objectValue(notReady["kubernetes"])["clusterWide"] != false {
		t.Fatalf("unready cluster must not claim cluster-wide control: %#v", notReady)
	}
}

func TestClusterInventoryCacheCloneAndReset(t *testing.T) {
	resetClusterInventoryCacheForTest()
	clusterInventoryCache.Lock()
	clusterInventoryCache.snapshot = &clusterSnapshot{
		Inventory: map[string]any{"ready": true, "nested": map[string]any{"version": "v1"}},
		HAMi:      map[string]any{"ready": true},
		Statuses:  []RuntimeStatus{{RuntimeID: "runtime-1"}},
	}
	clusterInventoryCache.updatedAt = time.Now()
	copy := cloneClusterSnapshot(clusterInventoryCache.snapshot)
	clusterInventoryCache.Unlock()
	objectValue(copy.Inventory["nested"])["version"] = "changed"
	copy.Statuses[0].RuntimeID = "changed"

	clusterInventoryCache.Lock()
	original := clusterInventoryCache.snapshot
	if stringValue(objectValue(original.Inventory["nested"])["version"]) != "v1" || original.Statuses[0].RuntimeID != "runtime-1" {
		t.Fatalf("cache snapshot was aliased: %#v", original)
	}
	clusterInventoryCache.Unlock()
	resetClusterInventoryCacheForTest()
}
