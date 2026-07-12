package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	clusterInventoryCacheTTL = 300 * time.Second
	clusterInventoryRetryTTL = 30 * time.Second
	maxRuntimeStatuses       = 500
)

type clusterSnapshot struct {
	Inventory map[string]any
	HAMi      map[string]any
	Statuses  []RuntimeStatus
}

var clusterInventoryCache struct {
	sync.Mutex
	snapshot  *clusterSnapshot
	updatedAt time.Time
}

func collectClusterState(ctx context.Context) (map[string]any, map[string]any, []RuntimeStatus) {
	client, err := newInClusterKubernetesClient()
	if err != nil {
		message := err.Error()
		return unavailableKubernetesInventory(message), map[string]any{"installed": false, "ready": false, "reason": message}, nil
	}

	clusterInventoryCache.Lock()
	cached := cloneClusterSnapshot(clusterInventoryCache.snapshot)
	fresh := cached != nil && time.Since(clusterInventoryCache.updatedAt) < clusterInventoryCacheTTL
	clusterInventoryCache.Unlock()
	if fresh {
		statuses, err := client.runtimeStatuses(ctx)
		if err != nil {
			return cached.Inventory, cached.HAMi, cached.Statuses
		}
		return cached.Inventory, cached.HAMi, statuses
	}

	snapshot, err := client.fullClusterSnapshot(ctx)
	if err == nil {
		clusterInventoryCache.Lock()
		clusterInventoryCache.snapshot = cloneClusterSnapshot(snapshot)
		clusterInventoryCache.updatedAt = time.Now()
		clusterInventoryCache.Unlock()
		return snapshot.Inventory, snapshot.HAMi, snapshot.Statuses
	}
	if cached != nil {
		staleInventory := cloneMap(cached.Inventory)
		staleInventory["stale"] = true
		staleInventory["refreshError"] = truncateString(err.Error(), 1024)
		statuses, statusErr := client.runtimeStatuses(ctx)
		if statusErr != nil {
			statuses = cached.Statuses
		}
		clusterInventoryCache.Lock()
		clusterInventoryCache.updatedAt = time.Now().Add(-clusterInventoryCacheTTL + clusterInventoryRetryTTL)
		clusterInventoryCache.Unlock()
		return staleInventory, cached.HAMi, statuses
	}
	message := truncateString(err.Error(), 1024)
	clusterInventoryCache.Lock()
	clusterInventoryCache.updatedAt = time.Now().Add(-clusterInventoryCacheTTL + clusterInventoryRetryTTL)
	clusterInventoryCache.Unlock()
	return unavailableKubernetesInventory(message), map[string]any{"installed": false, "ready": false, "reason": message}, nil
}

func (c *kubernetesClient) fullClusterSnapshot(ctx context.Context) (*clusterSnapshot, error) {
	version, err := c.get(ctx, "/version")
	if err != nil {
		return nil, err
	}
	resources := []struct {
		name string
		path string
	}{
		{name: "nodes", path: "/api/v1/nodes"},
		{name: "namespaces", path: "/api/v1/namespaces"},
		{name: "pods", path: "/api/v1/pods"},
		{name: "deployments", path: "/apis/apps/v1/deployments"},
		{name: "persistentVolumeClaims", path: "/api/v1/persistentvolumeclaims"},
		{name: "persistentVolumes", path: "/api/v1/persistentvolumes"},
	}

	requestContext, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		name  string
		value kubernetesListResult
		err   error
	}
	results := make(chan result, len(resources))
	for _, resource := range resources {
		resource := resource
		go func() {
			value, err := c.list(requestContext, resource.path, "")
			results <- result{name: resource.name, value: value, err: err}
		}()
	}
	lists := make(map[string]kubernetesListResult, len(resources))
	for range resources {
		selected := <-results
		if selected.err != nil {
			cancel()
			return nil, selected.err
		}
		lists[selected.name] = selected.value
	}

	phaseCounts := map[string]int{}
	for _, pod := range lists["pods"].Items {
		phase := firstNonEmpty(stringValue(objectValue(pod["status"])["phase"]), "Unknown")
		phaseCounts[phase]++
	}
	truncated := make([]string, 0)
	for _, resource := range resources {
		if lists[resource.name].Truncated {
			truncated = append(truncated, resource.name)
		}
	}
	sort.Strings(truncated)
	nodes := lists["nodes"].Items
	namespaces := lists["namespaces"].Items
	inventory := map[string]any{
		"available":     true,
		"installed":     true,
		"ready":         true,
		"accessMethod":  "service-account",
		"version":       stringValue(version["gitVersion"]),
		"serverVersion": stringValue(version["gitVersion"]),
		"platform":      stringValue(version["platform"]),
		"counts": map[string]any{
			"nodes":                  len(nodes),
			"namespaces":             len(namespaces),
			"pods":                   len(lists["pods"].Items),
			"deployments":            len(lists["deployments"].Items),
			"persistentVolumeClaims": len(lists["persistentVolumeClaims"].Items),
			"persistentVolumes":      len(lists["persistentVolumes"].Items),
		},
		"podPhases":           phaseCounts,
		"nodes":               summarizeKubernetesNodes(nodes, 200),
		"namespaces":          summarizeKubernetesNamespaces(namespaces, 500),
		"truncatedResources":  truncated,
		"countsTruncated":     len(truncated) > 0,
		"maxItemsPerResource": kubernetesMaxListItems,
	}
	statuses := runtimeStatusesFromObjects(lists["deployments"].Items, lists["pods"].Items)
	return &clusterSnapshot{
		Inventory: inventory,
		HAMi:      hamiStatus(lists["pods"].Items),
		Statuses:  statuses,
	}, nil
}

func (c *kubernetesClient) runtimeStatuses(ctx context.Context) ([]RuntimeStatus, error) {
	selector := monkeysRuntimeLabel
	deployments, err := c.list(ctx, "/apis/apps/v1/deployments", selector)
	if err != nil {
		return nil, err
	}
	pods, err := c.list(ctx, "/api/v1/pods", selector)
	if err != nil {
		return nil, err
	}
	return runtimeStatusesFromObjects(deployments.Items, pods.Items), nil
}

func runtimeStatusesFromObjects(deployments, pods []map[string]any) []RuntimeStatus {
	podsByRuntime := map[string][]map[string]any{}
	for _, pod := range pods {
		labels := objectValue(objectValue(pod["metadata"])["labels"])
		runtimeID := stringValue(labels[monkeysRuntimeLabel])
		if runtimeID != "" {
			podsByRuntime[runtimeID] = append(podsByRuntime[runtimeID], pod)
		}
	}
	observedAt := time.Now().UnixMilli()
	statuses := make([]RuntimeStatus, 0, minInt(len(deployments), maxRuntimeStatuses))
	for _, deployment := range deployments {
		metadata := objectValue(deployment["metadata"])
		labels := objectValue(metadata["labels"])
		runtimeID := stringValue(labels[monkeysRuntimeLabel])
		if runtimeID == "" {
			continue
		}
		deploymentName := stringValue(metadata["name"])
		namespace := firstNonEmpty(stringValue(metadata["namespace"]), "default")
		result := inspectRuntimeResult(runtimeID, namespace, deploymentName, deployment, podsByRuntime[runtimeID])
		podValues := objectSlice(result["pods"])
		runtimePods := make([]RuntimePodInfo, 0, len(podValues))
		for _, pod := range podValues {
			runtimePods = append(runtimePods, RuntimePodInfo{
				Name:    stringValue(pod["name"]),
				Phase:   stringValue(pod["phase"]),
				Ready:   pod["ready"] == true,
				Reason:  stringValue(pod["reason"]),
				Message: stringValue(pod["message"]),
			})
		}
		statuses = append(statuses, RuntimeStatus{
			RuntimeID:         runtimeID,
			Namespace:         namespace,
			DeploymentName:    deploymentName,
			Status:            stringValue(result["status"]),
			Reason:            stringValue(result["reason"]),
			Message:           stringValue(result["message"]),
			DesiredReplicas:   intValue(result["desiredReplicas"]),
			ReadyReplicas:     intValue(result["readyReplicas"]),
			AvailableReplicas: intValue(result["availableReplicas"]),
			PodCount:          intValue(result["podCount"]),
			ReadyPodCount:     intValue(result["readyPodCount"]),
			Pods:              runtimePods,
			ObservedAt:        observedAt,
		})
		if len(statuses) == maxRuntimeStatuses {
			break
		}
	}
	sort.Slice(statuses, func(left, right int) bool {
		return statuses[left].RuntimeID < statuses[right].RuntimeID
	})
	return statuses
}

func hamiStatus(pods []map[string]any) map[string]any {
	matches := make([]map[string]any, 0)
	ready := 0
	for _, pod := range pods {
		metadata := objectValue(pod["metadata"])
		searchable := strings.ToLower(stringValue(metadata["name"]) + " " + marshalCompact(metadata["labels"]))
		if !strings.Contains(searchable, "hami") && !strings.Contains(searchable, "vgpu") {
			continue
		}
		if kubernetesPodReady(pod) {
			ready++
		}
		if len(matches) < 20 {
			matches = append(matches, kubernetesPodSummary(pod))
		}
	}
	installed := len(matches) > 0
	return map[string]any{
		"installed":         installed,
		"ready":             installed && ready == len(matches),
		"devicePluginReady": installed && ready > 0,
		"podCount":          len(matches),
		"pods":              matches,
	}
}

func clusterCapabilities(inventory map[string]any) map[string]any {
	ready := inventory["ready"] == true
	return map[string]any{
		"mode": "cluster",
		"kubernetes": map[string]any{
			"observe":      ready,
			"apply":        ready,
			"exec":         ready && len(resolveKubectlCommand()) > 0,
			"clusterWide":  ready,
			"accessMethod": inventory["accessMethod"],
		},
		"resourceBoundary": "cluster",
	}
}

func summarizeKubernetesNodes(nodes []map[string]any, limit int) []map[string]any {
	result := make([]map[string]any, 0, minInt(len(nodes), limit))
	for _, node := range nodes {
		if len(result) == limit {
			break
		}
		metadata := objectValue(node["metadata"])
		status := objectValue(node["status"])
		addresses, _ := status["addresses"].([]any)
		internalIP := ""
		for _, value := range addresses {
			address := objectValue(value)
			if stringValue(address["type"]) == "InternalIP" {
				internalIP = stringValue(address["address"])
				break
			}
		}
		ready := false
		conditions, _ := status["conditions"].([]any)
		for _, value := range conditions {
			condition := objectValue(value)
			if stringValue(condition["type"]) == "Ready" {
				ready = strings.EqualFold(stringValue(condition["status"]), "True")
				break
			}
		}
		result = append(result, map[string]any{
			"name":        stringValue(metadata["name"]),
			"internalIp":  internalIP,
			"ready":       ready,
			"capacity":    status["capacity"],
			"allocatable": status["allocatable"],
		})
	}
	return result
}

func summarizeKubernetesNamespaces(namespaces []map[string]any, limit int) []string {
	result := make([]string, 0, minInt(len(namespaces), limit))
	for _, namespace := range namespaces {
		if len(result) == limit {
			break
		}
		name := stringValue(objectValue(namespace["metadata"])["name"])
		if name != "" {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func unavailableKubernetesInventory(message string) map[string]any {
	return map[string]any{
		"available": false, "installed": true, "ready": false,
		"accessMethod": "service-account", "error": truncateString(message, 1024),
	}
}

func cloneClusterSnapshot(value *clusterSnapshot) *clusterSnapshot {
	if value == nil {
		return nil
	}
	statuses := append([]RuntimeStatus(nil), value.Statuses...)
	return &clusterSnapshot{Inventory: cloneMap(value.Inventory), HAMi: cloneMap(value.HAMi), Statuses: statuses}
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	result, err := cloneObject(value)
	if err != nil {
		return map[string]any{}
	}
	return result
}

func truncateString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func resetClusterInventoryCacheForTest() {
	clusterInventoryCache.Lock()
	clusterInventoryCache.snapshot = nil
	clusterInventoryCache.updatedAt = time.Time{}
	clusterInventoryCache.Unlock()
}

func clusterInventoryDescription(inventory map[string]any) string {
	if inventory["ready"] == true {
		return fmt.Sprintf("Kubernetes %s is ready", stringValue(inventory["version"]))
	}
	return firstNonEmpty(stringValue(inventory["error"]), "Kubernetes is unavailable")
}
