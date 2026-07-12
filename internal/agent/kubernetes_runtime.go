package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	monkeysRuntimeLabel = "monkeys/runtime-id"
	maxRuntimeManifests = 100
	maxExecOutputBytes  = 1024 * 1024
)

type kubernetesResource struct {
	apiBase    string
	plural     string
	namespaced bool
}

var kubernetesRuntimeResources = map[string]kubernetesResource{
	"Namespace":             {apiBase: "/api/v1", plural: "namespaces", namespaced: false},
	"Secret":                {apiBase: "/api/v1", plural: "secrets", namespaced: true},
	"Service":               {apiBase: "/api/v1", plural: "services", namespaced: true},
	"PersistentVolumeClaim": {apiBase: "/api/v1", plural: "persistentvolumeclaims", namespaced: true},
	"Deployment":            {apiBase: "/apis/apps/v1", plural: "deployments", namespaced: true},
}

func executeClusterTask(ctx context.Context, taskType string, payload map[string]any) (map[string]any, error) {
	allowed := map[string]bool{
		"k8s.apply-runtime":   true,
		"k8s.scale-runtime":   true,
		"k8s.restart-runtime": true,
		"k8s.inspect-runtime": true,
		"k8s.delete-runtime":  true,
		"k8s.exec-runtime":    true,
		"k8s.cordon-node":     true,
		"k8s.uncordon-node":   true,
		"k8s.drain-node":      true,
	}
	if !allowed[taskType] {
		return nil, newKubernetesError(fmt.Sprintf("%s is not allowed in cluster mode", firstNonEmpty(taskType, "<empty>")), false)
	}
	if err := ctx.Err(); err != nil {
		return nil, newKubernetesError("cluster task lease was lost before execution", true)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if isKubernetesNodeTask(taskType) {
		return executeClusterKubernetesNodeTask(ctx, taskType, payload)
	}
	if definition := objectValue(payload["definition"]); len(definition) > 0 && taskType == "k8s.apply-runtime" {
		payload = definition
	}
	if taskType == "k8s.exec-runtime" {
		return executeKubernetesRuntimeCommand(ctx, payload)
	}
	client, err := newInClusterKubernetesClient()
	if err != nil {
		return nil, err
	}
	switch taskType {
	case "k8s.apply-runtime":
		return client.applyRuntime(ctx, payload)
	case "k8s.scale-runtime":
		return client.scaleRuntime(ctx, payload)
	case "k8s.restart-runtime":
		return client.restartRuntime(ctx, payload)
	case "k8s.inspect-runtime":
		return client.inspectRuntime(ctx, payload)
	case "k8s.delete-runtime":
		return client.deleteRuntime(ctx, payload)
	default:
		return nil, newKubernetesError("unsupported cluster task", false)
	}
}

func (c *kubernetesClient) applyRuntime(ctx context.Context, payload map[string]any) (map[string]any, error) {
	runtimeID, namespace, _, err := runtimeIdentity(payload, false)
	if err != nil {
		return nil, err
	}
	manifests, err := runtimeManifestObjects(payload["manifests"])
	if err != nil {
		return nil, err
	}
	if err := validateRuntimeManifests(runtimeID, namespace, manifests); err != nil {
		return nil, err
	}
	results := make([]map[string]any, 0, len(manifests))
	for _, original := range manifests {
		if err := ctx.Err(); err != nil {
			return nil, newKubernetesError("cluster task lease was lost while applying manifests", true)
		}
		manifest, err := cloneObject(original)
		if err != nil {
			return nil, newKubernetesError(fmt.Sprintf("copy Kubernetes manifest: %v", err), false)
		}
		kind := stringValue(manifest["kind"])
		metadata := objectValue(manifest["metadata"])
		name := stringValue(metadata["name"])
		manifestNamespace := stringValue(metadata["namespace"])
		resource := kubernetesRuntimeResources[kind]
		if !resource.namespaced {
			manifestNamespace = ""
		}
		collection := kubernetesCollection(resource.apiBase, resource.plural, manifestNamespace, resource.namespaced)
		path := collection + "/" + url.PathEscape(name)
		existing, status, err := c.request(ctx, http.MethodGet, path, nil, nil, "", http.StatusNotFound)
		if err != nil {
			return nil, err
		}
		operation := "updated"
		var saved map[string]any
		if status == http.StatusNotFound {
			saved, _, err = c.request(ctx, http.MethodPost, collection, nil, manifest, "")
			operation = "created"
		} else if kind == "PersistentVolumeClaim" {
			patch := pvcUpdatePatch(manifest)
			saved, err = c.patchWithResourceVersion(ctx, path, patch)
		} else {
			if kind == "Service" {
				preserveServiceAllocations(manifest, existing)
			}
			saved, err = c.replaceWithResourceVersion(ctx, path, manifest)
		}
		if err != nil {
			return nil, err
		}
		results = append(results, map[string]any{
			"kind":            kind,
			"name":            name,
			"namespace":       nilIfEmpty(manifestNamespace),
			"operation":       operation,
			"resourceVersion": stringValue(objectValue(saved["metadata"])["resourceVersion"]),
		})
	}
	return map[string]any{
		"success":   true,
		"action":    "k8s.apply-runtime",
		"runtimeId": runtimeID,
		"namespace": firstNonEmpty(namespace, "default"),
		"results":   results,
		"message":   fmt.Sprintf("Applied %d runtime manifests.", len(results)),
	}, nil
}

func validateRuntimeManifests(runtimeID, namespace string, manifests []map[string]any) error {
	if len(manifests) == 0 {
		return newKubernetesError("payload.manifests must be a non-empty JSON object array", false)
	}
	if len(manifests) > maxRuntimeManifests {
		return newKubernetesError("at most 100 Kubernetes manifests can be applied in one task", false)
	}
	for _, manifest := range manifests {
		kind := stringValue(manifest["kind"])
		metadata := objectValue(manifest["metadata"])
		name := stringValue(metadata["name"])
		manifestNamespace := stringValue(metadata["namespace"])
		resource, supported := kubernetesRuntimeResources[kind]
		if !supported {
			return newKubernetesError(fmt.Sprintf("unsupported runtime Kubernetes manifest kind: %s", firstNonEmpty(kind, "<empty>")), false)
		}
		if name == "" {
			return newKubernetesError("Kubernetes manifest is missing metadata.name", false)
		}
		if resource.namespaced {
			if manifestNamespace == "" {
				return newKubernetesError(fmt.Sprintf("%s %s is missing metadata.namespace", kind, name), false)
			}
			if namespace != "" && manifestNamespace != namespace {
				return newKubernetesError(fmt.Sprintf("%s %s namespace does not match runtime namespace %s", kind, name, namespace), false)
			}
		}
		if kind != "Namespace" && stringValue(objectValue(metadata["labels"])[monkeysRuntimeLabel]) != runtimeID {
			return newKubernetesError(fmt.Sprintf("%s %s must be owned by runtime %s through label %s", kind, name, runtimeID, monkeysRuntimeLabel), false)
		}
	}
	return nil
}

func runtimeManifestObjects(value any) ([]map[string]any, error) {
	switch selected := value.(type) {
	case []map[string]any:
		return selected, nil
	case []any:
		result := make([]map[string]any, 0, len(selected))
		for _, item := range selected {
			manifest, ok := item.(map[string]any)
			if !ok {
				return nil, newKubernetesError("payload.manifests entries must be JSON objects", false)
			}
			result = append(result, manifest)
		}
		return result, nil
	default:
		return nil, newKubernetesError("payload.manifests must be a non-empty JSON object array", false)
	}
}

func (c *kubernetesClient) scaleRuntime(ctx context.Context, payload map[string]any) (map[string]any, error) {
	runtimeID, namespace, deployment, err := runtimeIdentity(payload, false)
	if err != nil {
		return nil, err
	}
	if deployment == "" {
		deployment, err = c.resolveRuntimeDeployment(ctx, runtimeID, namespace, false)
		if err != nil {
			return nil, err
		}
	}
	replicas := intValue(payload["replicas"])
	if payload["replicas"] == nil {
		replicas = intValue(payload["desiredReplicas"])
	}
	if replicas < 0 {
		return nil, newKubernetesError("replicas must be zero or greater", false)
	}
	path := kubernetesCollection("/apis/apps/v1", "deployments", namespace, true) + "/" + url.PathEscape(deployment)
	if _, err := c.patchWithResourceVersion(ctx, path, map[string]any{"spec": map[string]any{"replicas": replicas}}); err != nil {
		return nil, err
	}
	return map[string]any{
		"success": true, "action": "k8s.scale-runtime", "runtimeId": runtimeID,
		"namespace": namespace, "deploymentName": deployment, "desiredReplicas": replicas,
		"message": fmt.Sprintf("Deployment %s scaled to %d.", deployment, replicas),
	}, nil
}

func (c *kubernetesClient) restartRuntime(ctx context.Context, payload map[string]any) (map[string]any, error) {
	runtimeID, namespace, deployment, err := runtimeIdentity(payload, false)
	if err != nil {
		return nil, err
	}
	if deployment == "" {
		deployment, err = c.resolveRuntimeDeployment(ctx, runtimeID, namespace, false)
		if err != nil {
			return nil, err
		}
	}
	restartedAt := time.Now().UnixMilli()
	path := kubernetesCollection("/apis/apps/v1", "deployments", namespace, true) + "/" + url.PathEscape(deployment)
	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]any{"monkeys/runtime-restarted-at": strconv.FormatInt(restartedAt, 10)},
				},
			},
		},
	}
	if _, err := c.patchWithResourceVersion(ctx, path, patch); err != nil {
		return nil, err
	}
	return map[string]any{
		"success": true, "action": "k8s.restart-runtime", "runtimeId": runtimeID,
		"namespace": namespace, "deploymentName": deployment, "restartedAt": restartedAt,
		"message": fmt.Sprintf("Deployment %s restarted.", deployment),
	}, nil
}

func (c *kubernetesClient) inspectRuntime(ctx context.Context, payload map[string]any) (map[string]any, error) {
	runtimeID, namespace, deployment, err := runtimeIdentity(payload, false)
	if err != nil {
		return nil, err
	}
	if deployment == "" {
		deployment, err = c.resolveRuntimeDeployment(ctx, runtimeID, namespace, true)
		if err != nil {
			return nil, err
		}
	}
	if deployment == "" {
		return absentRuntime(runtimeID, namespace), nil
	}
	path := kubernetesCollection("/apis/apps/v1", "deployments", namespace, true) + "/" + url.PathEscape(deployment)
	selected, status, err := c.request(ctx, http.MethodGet, path, nil, nil, "", http.StatusNotFound)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return absentRuntime(runtimeID, namespace), nil
	}
	pods, err := c.list(ctx, kubernetesCollection("/api/v1", "pods", namespace, true), monkeysRuntimeLabel+"="+runtimeID)
	if err != nil {
		return nil, err
	}
	return inspectRuntimeResult(runtimeID, namespace, deployment, selected, pods.Items), nil
}

func (c *kubernetesClient) deleteRuntime(ctx context.Context, payload map[string]any) (map[string]any, error) {
	runtimeID, namespace, _, err := runtimeIdentity(payload, false)
	if err != nil {
		return nil, err
	}
	resources := []kubernetesResource{
		{apiBase: "/apis/apps/v1", plural: "deployments", namespaced: true},
		{apiBase: "/api/v1", plural: "services", namespaced: true},
		{apiBase: "/api/v1", plural: "secrets", namespaced: true},
		{apiBase: "/api/v1", plural: "persistentvolumeclaims", namespaced: true},
	}
	deleted := make([]string, 0)
	for _, resource := range resources {
		collection := kubernetesCollection(resource.apiBase, resource.plural, namespace, true)
		items, err := c.list(ctx, collection, monkeysRuntimeLabel+"="+runtimeID)
		if err != nil {
			return nil, err
		}
		for _, item := range items.Items {
			if err := ctx.Err(); err != nil {
				return nil, newKubernetesError("cluster task lease was lost while deleting runtime resources", true)
			}
			name := stringValue(objectValue(item["metadata"])["name"])
			if name == "" {
				continue
			}
			body := map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "propagationPolicy": "Background"}
			_, status, err := c.request(ctx, http.MethodDelete, collection+"/"+url.PathEscape(name), nil, body, "", http.StatusNotFound)
			if err != nil {
				return nil, err
			}
			if status != http.StatusNotFound {
				deleted = append(deleted, resource.plural+"/"+name)
			}
		}
	}
	sort.Strings(deleted)
	return map[string]any{
		"success": true, "action": "k8s.delete-runtime", "runtimeId": runtimeID,
		"namespace": namespace, "deleted": true, "deletedResources": deleted,
		"message": fmt.Sprintf("Deleted %d runtime resources.", len(deleted)),
	}, nil
}

func executeKubernetesRuntimeCommand(ctx context.Context, payload map[string]any) (map[string]any, error) {
	runtimeID, namespace, _, err := runtimeIdentity(payload, false)
	if err != nil {
		return nil, err
	}
	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		return nil, newKubernetesError("k8s.exec-runtime requires kubectl in the Cluster Agent image", false)
	}
	command := stringSlice(payload["command"])
	if len(command) == 0 {
		command = stringSlice(payload["args"])
	}
	if len(command) == 0 {
		return nil, newKubernetesError("command must be a non-empty array", false)
	}
	podName := stringValue(payload["podName"])
	if podName == "" {
		client, err := newInClusterKubernetesClient()
		if err != nil {
			return nil, err
		}
		pods, err := client.list(ctx, kubernetesCollection("/api/v1", "pods", namespace, true), monkeysRuntimeLabel+"="+runtimeID)
		if err != nil {
			return nil, err
		}
		for _, pod := range pods.Items {
			if stringValue(objectValue(pod["status"])["phase"]) == "Running" {
				podName = stringValue(objectValue(pod["metadata"])["name"])
				break
			}
		}
		if podName == "" && len(pods.Items) > 0 {
			podName = stringValue(objectValue(pods.Items[0]["metadata"])["name"])
		}
	}
	if podName == "" {
		return nil, newKubernetesError(fmt.Sprintf("no pod found for runtime %s", runtimeID), true)
	}
	container := firstNonEmpty(stringValue(payload["container"]), "runtime")
	arguments := []string{"-n", namespace, "exec", podName, "-c", container, "--"}
	arguments = append(arguments, command...)
	cmd := exec.CommandContext(ctx, kubectl, arguments...)
	var stdout, stderr cappedBuffer
	stdout.limit = maxExecOutputBytes
	stderr.limit = maxExecOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	exitCode := 0
	if err != nil {
		if ctx.Err() != nil {
			return nil, newKubernetesError("cluster task lease was lost during kubectl exec", true)
		}
		if exit, ok := err.(*exec.ExitError); ok {
			exitCode = exit.ExitCode()
		} else {
			return nil, newKubernetesError(fmt.Sprintf("execute kubectl: %v", err), true)
		}
	}
	return map[string]any{
		"success": exitCode == 0, "action": "k8s.exec-runtime", "runtimeId": runtimeID,
		"namespace": namespace, "podName": podName, "exitCode": exitCode,
		"stdout": stdout.String(), "stderr": stderr.String(),
	}, nil
}

func (c *kubernetesClient) resolveRuntimeDeployment(ctx context.Context, runtimeID, namespace string, allowAbsent bool) (string, error) {
	items, err := c.list(ctx, kubernetesCollection("/apis/apps/v1", "deployments", namespace, true), monkeysRuntimeLabel+"="+runtimeID)
	if err != nil {
		return "", err
	}
	if len(items.Items) == 0 && allowAbsent {
		return "", nil
	}
	if len(items.Items) != 1 {
		return "", newKubernetesError(fmt.Sprintf("expected one deployment for runtime %s, found %d", runtimeID, len(items.Items)), false)
	}
	name := stringValue(objectValue(items.Items[0]["metadata"])["name"])
	if name == "" {
		return "", newKubernetesError("runtime deployment is missing metadata.name", false)
	}
	return name, nil
}

func (c *kubernetesClient) replaceWithResourceVersion(ctx context.Context, path string, desired map[string]any) (map[string]any, error) {
	for attempt := 0; attempt < 3; attempt++ {
		existing, status, err := c.request(ctx, http.MethodGet, path, nil, nil, "", http.StatusNotFound)
		if err != nil {
			return nil, err
		}
		if status == http.StatusNotFound {
			return nil, newKubernetesError("Kubernetes resource disappeared before replacement: "+path, true)
		}
		body, err := cloneObject(desired)
		if err != nil {
			return nil, newKubernetesError(fmt.Sprintf("copy Kubernetes manifest: %v", err), false)
		}
		objectValue(body["metadata"])["resourceVersion"] = objectValue(existing["metadata"])["resourceVersion"]
		if stringValue(body["kind"]) == "Service" {
			preserveServiceAllocations(body, existing)
		}
		updated, responseStatus, err := c.request(ctx, http.MethodPut, path, nil, body, "", http.StatusConflict)
		if err != nil {
			return nil, err
		}
		if responseStatus != http.StatusConflict {
			return updated, nil
		}
		if err := waitConflictRetry(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, newKubernetesError("Kubernetes resource update conflicted repeatedly: "+path, true)
}

func (c *kubernetesClient) patchWithResourceVersion(ctx context.Context, path string, patch map[string]any) (map[string]any, error) {
	for attempt := 0; attempt < 3; attempt++ {
		existing, status, err := c.request(ctx, http.MethodGet, path, nil, nil, "", http.StatusNotFound)
		if err != nil {
			return nil, err
		}
		if status == http.StatusNotFound {
			return nil, newKubernetesError("Kubernetes resource not found: "+path, false)
		}
		body, err := cloneObject(patch)
		if err != nil {
			return nil, newKubernetesError(fmt.Sprintf("copy Kubernetes patch: %v", err), false)
		}
		metadata := objectValue(body["metadata"])
		if len(metadata) == 0 {
			metadata = map[string]any{}
			body["metadata"] = metadata
		}
		metadata["resourceVersion"] = objectValue(existing["metadata"])["resourceVersion"]
		updated, responseStatus, err := c.request(ctx, http.MethodPatch, path, nil, body, "application/merge-patch+json", http.StatusConflict)
		if err != nil {
			return nil, err
		}
		if responseStatus != http.StatusConflict {
			return updated, nil
		}
		if err := waitConflictRetry(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, newKubernetesError("Kubernetes resource patch conflicted repeatedly: "+path, true)
}

func waitConflictRetry(ctx context.Context, attempt int) error {
	if attempt >= 2 {
		return nil
	}
	timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return newKubernetesError("cluster task lease was lost during conflict retry", true)
	case <-timer.C:
		return nil
	}
}

func runtimeIdentity(payload map[string]any, allowEmptyRuntime bool) (runtimeID, namespace, deployment string, err error) {
	runtimeID = stringValue(payload["runtimeId"])
	if runtimeID == "" && !allowEmptyRuntime {
		return "", "", "", newKubernetesError("runtimeId is required", false)
	}
	namespace = firstNonEmpty(stringValue(payload["namespace"]), "default")
	deployment = firstNonEmpty(stringValue(payload["deploymentName"]), stringValue(payload["k8sDeploymentName"]))
	return runtimeID, namespace, deployment, nil
}

func kubernetesCollection(apiBase, plural, namespace string, namespaced bool) string {
	if !namespaced {
		return apiBase + "/" + plural
	}
	return apiBase + "/namespaces/" + url.PathEscape(namespace) + "/" + plural
}

func pvcUpdatePatch(manifest map[string]any) map[string]any {
	metadata := objectValue(manifest["metadata"])
	spec := objectValue(manifest["spec"])
	resources := objectValue(spec["resources"])
	requests := objectValue(resources["requests"])
	return map[string]any{
		"metadata": map[string]any{
			"labels":      metadata["labels"],
			"annotations": metadata["annotations"],
		},
		"spec": map[string]any{
			"resources": map[string]any{
				"requests": map[string]any{"storage": requests["storage"]},
			},
		},
	}
}

func preserveServiceAllocations(desired, existing map[string]any) {
	desiredSpec := objectValue(desired["spec"])
	existingSpec := objectValue(existing["spec"])
	for _, key := range []string{"clusterIP", "clusterIPs", "ipFamilies", "ipFamilyPolicy", "healthCheckNodePort"} {
		if desiredSpec[key] == nil && existingSpec[key] != nil {
			desiredSpec[key] = existingSpec[key]
		}
	}
	existingPorts := map[string]map[string]any{}
	for _, port := range objectSlice(existingSpec["ports"]) {
		key := firstNonEmpty(stringValue(port["name"]), stringValue(port["port"]))
		existingPorts[key] = port
	}
	ports, ok := desiredSpec["ports"].([]any)
	if !ok {
		return
	}
	for _, value := range ports {
		port := objectValue(value)
		key := firstNonEmpty(stringValue(port["name"]), stringValue(port["port"]))
		if current := existingPorts[key]; current != nil && port["nodePort"] == nil && current["nodePort"] != nil {
			port["nodePort"] = current["nodePort"]
		}
	}
}

func absentRuntime(runtimeID, namespace string) map[string]any {
	return map[string]any{
		"success": true, "action": "k8s.inspect-runtime", "runtimeId": runtimeID,
		"namespace": namespace, "deploymentName": nil, "exists": false, "status": "absent",
		"desiredReplicas": 0, "readyReplicas": 0, "availableReplicas": 0,
		"podCount": 0, "readyPodCount": 0, "phaseSummary": map[string]int{},
		"message": fmt.Sprintf("Runtime deployment for %s was not found.", runtimeID),
	}
}

func inspectRuntimeResult(runtimeID, namespace, deployment string, selected map[string]any, pods []map[string]any) map[string]any {
	spec := objectValue(selected["spec"])
	status := objectValue(selected["status"])
	desired := intValue(spec["replicas"])
	if spec["replicas"] == nil {
		desired = intValue(status["replicas"])
	}
	ready := intValue(status["readyReplicas"])
	available := intValue(status["availableReplicas"])
	phases := map[string]int{}
	readyPods := 0
	reason := ""
	podSummaries := make([]map[string]any, 0, minInt(len(pods), 50))
	for _, pod := range pods {
		podStatus := objectValue(pod["status"])
		phase := firstNonEmpty(stringValue(podStatus["phase"]), "Unknown")
		phases[phase]++
		if kubernetesPodReady(pod) {
			readyPods++
		}
		if reason == "" {
			reason = kubernetesPodFailure(pod)
		}
		if len(podSummaries) < 50 {
			podSummaries = append(podSummaries, kubernetesPodSummary(pod))
		}
	}
	runtimeStatus := "starting"
	if desired == 0 {
		runtimeStatus = "stopped"
	} else if ready >= desired {
		runtimeStatus = "running"
	}
	if reason != "" && ready == 0 {
		runtimeStatus = "error"
	}
	message := reason
	if message == "" {
		message = fmt.Sprintf("Deployment %s is %s.", deployment, runtimeStatus)
	}
	return map[string]any{
		"success": true, "action": "k8s.inspect-runtime", "runtimeId": runtimeID,
		"namespace": namespace, "deploymentName": deployment, "exists": true,
		"status": runtimeStatus, "desiredReplicas": desired, "readyReplicas": ready,
		"availableReplicas": available, "podCount": len(pods), "readyPodCount": readyPods,
		"phaseSummary": phases, "reason": nilIfEmpty(reason), "message": message,
		"pods": podSummaries, "observedAt": time.Now().UnixMilli(),
	}
}

func kubernetesPodReady(pod map[string]any) bool {
	conditions, ok := objectValue(pod["status"])["conditions"].([]any)
	if !ok {
		return false
	}
	for _, value := range conditions {
		condition := objectValue(value)
		if stringValue(condition["type"]) == "Ready" {
			return strings.EqualFold(stringValue(condition["status"]), "True")
		}
	}
	return false
}

func kubernetesPodFailure(pod map[string]any) string {
	status := objectValue(pod["status"])
	if strings.EqualFold(stringValue(status["phase"]), "Failed") {
		return firstNonEmpty(stringValue(status["reason"]), stringValue(status["message"]), "PodFailed")
	}
	containerStatuses, _ := status["containerStatuses"].([]any)
	for _, value := range containerStatuses {
		state := objectValue(objectValue(value)["state"])
		for _, key := range []string{"waiting", "terminated"} {
			selected := objectValue(state[key])
			reason := stringValue(selected["reason"])
			if runtimeFailureReason(reason) {
				return firstNonEmpty(reason, stringValue(selected["message"]))
			}
		}
	}
	return ""
}

func kubernetesPodSummary(pod map[string]any) map[string]any {
	metadata := objectValue(pod["metadata"])
	status := objectValue(pod["status"])
	return map[string]any{
		"name": stringValue(metadata["name"]), "namespace": stringValue(metadata["namespace"]),
		"phase": stringValue(status["phase"]), "ready": kubernetesPodReady(pod),
		"reason": nilIfEmpty(kubernetesPodFailure(pod)),
	}
}

func runtimeFailureReason(reason string) bool {
	switch reason {
	case "ErrImagePull", "ImagePullBackOff", "CreateContainerConfigError", "CrashLoopBackOff", "RunContainerError", "InvalidImageName", "Evicted", "Error":
		return true
	default:
		return false
	}
}

func stringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]string); ok {
			return typed
		}
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, fmt.Sprint(item))
	}
	return result
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

type cappedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *cappedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = b.buffer.Write(value)
	}
	return original, nil
}

func (b *cappedBuffer) String() string { return b.buffer.String() }

func marshalCompact(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
