package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	minDrainTimeoutSeconds = 1
	maxDrainTimeoutSeconds = 30 * 60
	maxDrainGraceSeconds   = 60 * 60
	drainPollInterval      = 500 * time.Millisecond
)

var kubernetesDNS1123Subdomain = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?(?:\.[a-z0-9](?:[-a-z0-9]*[a-z0-9])?)*$`)

type kubernetesDrainOptions struct {
	ignoreDaemonSets   bool
	deleteEmptyDirData bool
	force              bool
	gracePeriodSeconds int
	timeoutSeconds     int
}

type kubernetesDrainPod struct {
	name      string
	namespace string
}

func isKubernetesNodeTask(taskType string) bool {
	switch taskType {
	case "k8s.cordon-node", "k8s.uncordon-node", "k8s.drain-node":
		return true
	default:
		return false
	}
}

func validateKubernetesNodeName(payload map[string]any) (string, error) {
	raw, ok := payload["nodeName"].(string)
	if !ok || raw == "" {
		return "", newKubernetesError("nodeName must be a non-empty DNS subdomain string", false)
	}
	if raw != strings.TrimSpace(raw) || len(raw) > 253 || !kubernetesDNS1123Subdomain.MatchString(raw) {
		return "", newKubernetesError("nodeName must be a valid lowercase Kubernetes DNS subdomain of at most 253 characters", false)
	}
	for _, label := range strings.Split(raw, ".") {
		if len(label) > 63 {
			return "", newKubernetesError("nodeName DNS labels must be at most 63 characters", false)
		}
	}
	return raw, nil
}

func parseKubernetesDrainOptions(payload map[string]any) (kubernetesDrainOptions, error) {
	ignoreDaemonSets, err := strictBooleanOption(payload, "ignoreDaemonSets", false)
	if err != nil {
		return kubernetesDrainOptions{}, err
	}
	deleteEmptyDirData, err := strictBooleanOption(payload, "deleteEmptyDirData", false)
	if err != nil {
		return kubernetesDrainOptions{}, err
	}
	force, err := strictBooleanOption(payload, "force", false)
	if err != nil {
		return kubernetesDrainOptions{}, err
	}
	gracePeriodSeconds, err := boundedIntegerOption(payload, "gracePeriodSeconds", 60, 0, maxDrainGraceSeconds)
	if err != nil {
		return kubernetesDrainOptions{}, err
	}
	timeoutSeconds, err := boundedIntegerOption(payload, "timeoutSeconds", 300, minDrainTimeoutSeconds, maxDrainTimeoutSeconds)
	if err != nil {
		return kubernetesDrainOptions{}, err
	}
	return kubernetesDrainOptions{
		ignoreDaemonSets:   ignoreDaemonSets,
		deleteEmptyDirData: deleteEmptyDirData,
		force:              force,
		gracePeriodSeconds: gracePeriodSeconds,
		timeoutSeconds:     timeoutSeconds,
	}, nil
}

func strictBooleanOption(payload map[string]any, key string, fallback bool) (bool, error) {
	value, exists := payload[key]
	if !exists || value == nil {
		return fallback, nil
	}
	selected, ok := value.(bool)
	if !ok {
		return false, newKubernetesError(fmt.Sprintf("%s must be a boolean", key), false)
	}
	return selected, nil
}

func boundedIntegerOption(payload map[string]any, key string, fallback, minimum, maximum int) (int, error) {
	value, exists := payload[key]
	if !exists || value == nil {
		return fallback, nil
	}
	selected, ok := exactInteger(value)
	if !ok || selected < int64(minimum) || selected > int64(maximum) {
		return 0, newKubernetesError(fmt.Sprintf("%s must be an integer between %d and %d", key, minimum, maximum), false)
	}
	return int(selected), nil
}

func exactInteger(value any) (int64, bool) {
	switch selected := value.(type) {
	case int:
		return int64(selected), true
	case int8:
		return int64(selected), true
	case int16:
		return int64(selected), true
	case int32:
		return int64(selected), true
	case int64:
		return selected, true
	case uint:
		if uint64(selected) > math.MaxInt64 {
			return 0, false
		}
		return int64(selected), true
	case uint8:
		return int64(selected), true
	case uint16:
		return int64(selected), true
	case uint32:
		return int64(selected), true
	case uint64:
		if selected > math.MaxInt64 {
			return 0, false
		}
		return int64(selected), true
	case float32:
		value := float64(selected)
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value < math.MinInt64 || value > math.MaxInt64 {
			return 0, false
		}
		return int64(value), true
	case float64:
		if math.IsNaN(selected) || math.IsInf(selected, 0) || math.Trunc(selected) != selected || selected < math.MinInt64 || selected > math.MaxInt64 {
			return 0, false
		}
		return int64(selected), true
	case json.Number:
		result, err := selected.Int64()
		return result, err == nil
	default:
		return 0, false
	}
}

func executeHostKubernetesNodeTask(ctx context.Context, kubectl []string, taskType string, payload map[string]any) (map[string]any, error) {
	nodeName, err := validateKubernetesNodeName(payload)
	if err != nil {
		return nil, err
	}
	command := append([]string{}, kubectl...)
	switch taskType {
	case "k8s.cordon-node":
		command = append(command, "cordon", nodeName)
	case "k8s.uncordon-node":
		command = append(command, "uncordon", nodeName)
	case "k8s.drain-node":
		options, err := parseKubernetesDrainOptions(payload)
		if err != nil {
			return nil, err
		}
		command = append(command,
			"drain", nodeName,
			"--ignore-daemonsets="+strconv.FormatBool(options.ignoreDaemonSets),
			"--delete-emptydir-data="+strconv.FormatBool(options.deleteEmptyDirData),
			"--force="+strconv.FormatBool(options.force),
			"--grace-period="+strconv.Itoa(options.gracePeriodSeconds),
			"--timeout="+strconv.Itoa(options.timeoutSeconds)+"s",
		)
		drainContext, cancel := context.WithTimeout(ctx, time.Duration(options.timeoutSeconds+5)*time.Second)
		defer cancel()
		output, err := runHostKubectl(drainContext, command, nil)
		if err != nil {
			if ctx.Err() == nil && drainContext.Err() == context.DeadlineExceeded {
				return nil, newKubernetesError(fmt.Sprintf("drain node %s timed out after %d seconds; a PodDisruptionBudget or pod termination may be blocking eviction", nodeName, options.timeoutSeconds), true)
			}
			return nil, newKubernetesError(fmt.Sprintf("drain node %s failed; a PodDisruptionBudget, unmanaged pod, local storage, or pod termination may be blocking eviction: %v", nodeName, err), true)
		}
		return map[string]any{
			"success": true, "action": taskType, "nodeName": nodeName,
			"unschedulable": true, "drained": true, "message": strings.TrimSpace(output),
		}, nil
	default:
		return nil, newKubernetesError("unsupported host Kubernetes node task", false)
	}
	output, err := runHostKubectl(ctx, command, nil)
	if err != nil {
		return nil, err
	}
	unschedulable := taskType == "k8s.cordon-node"
	return map[string]any{
		"success": true, "action": taskType, "nodeName": nodeName,
		"unschedulable": unschedulable, "message": strings.TrimSpace(output),
	}, nil
}

func executeClusterKubernetesNodeTask(ctx context.Context, taskType string, payload map[string]any) (map[string]any, error) {
	nodeName, err := validateKubernetesNodeName(payload)
	if err != nil {
		return nil, err
	}
	client, err := newInClusterKubernetesClient()
	if err != nil {
		return nil, err
	}
	return client.executeNodeTask(ctx, taskType, nodeName, payload)
}

func (c *kubernetesClient) executeNodeTask(ctx context.Context, taskType, nodeName string, payload map[string]any) (map[string]any, error) {
	switch taskType {
	case "k8s.cordon-node":
		return c.setNodeUnschedulable(ctx, nodeName, true, taskType)
	case "k8s.uncordon-node":
		return c.setNodeUnschedulable(ctx, nodeName, false, taskType)
	case "k8s.drain-node":
		options, err := parseKubernetesDrainOptions(payload)
		if err != nil {
			return nil, err
		}
		return c.drainNode(ctx, nodeName, options)
	default:
		return nil, newKubernetesError("unsupported cluster Kubernetes node task", false)
	}
}

func (c *kubernetesClient) setNodeUnschedulable(ctx context.Context, nodeName string, unschedulable bool, action string) (map[string]any, error) {
	path := "/api/v1/nodes/" + url.PathEscape(nodeName)
	node, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	current, _ := objectValue(node["spec"])["unschedulable"].(bool)
	if current == unschedulable {
		return map[string]any{
			"success": true, "action": action, "nodeName": nodeName,
			"unschedulable": unschedulable, "changed": false,
			"message": fmt.Sprintf("Node %s is already %s.", nodeName, nodeSchedulingState(unschedulable)),
		}, nil
	}
	updated, err := c.patchWithResourceVersion(ctx, path, map[string]any{
		"spec": map[string]any{"unschedulable": unschedulable},
	})
	if err != nil {
		return nil, err
	}
	updatedValue, _ := objectValue(updated["spec"])["unschedulable"].(bool)
	if updatedValue != unschedulable {
		return nil, newKubernetesError(fmt.Sprintf("Kubernetes API did not set node %s unschedulable=%t", nodeName, unschedulable), true)
	}
	return map[string]any{
		"success": true, "action": action, "nodeName": nodeName,
		"unschedulable": unschedulable, "changed": true,
		"message": fmt.Sprintf("Node %s is now %s.", nodeName, nodeSchedulingState(unschedulable)),
	}, nil
}

func nodeSchedulingState(unschedulable bool) string {
	if unschedulable {
		return "cordoned"
	}
	return "uncordoned"
}

func (c *kubernetesClient) drainNode(ctx context.Context, nodeName string, options kubernetesDrainOptions) (map[string]any, error) {
	drainContext, cancel := context.WithTimeout(ctx, time.Duration(options.timeoutSeconds)*time.Second)
	defer cancel()
	cordon, err := c.setNodeUnschedulable(drainContext, nodeName, true, "k8s.cordon-node")
	if err != nil {
		return nil, err
	}
	pods, err := c.listPodsOnNode(drainContext, nodeName)
	if err != nil {
		return nil, err
	}
	candidates, skippedDaemonSets, err := selectDrainPods(pods, options)
	if err != nil {
		return nil, err
	}
	evicted := make([]string, 0, len(candidates))
	for _, pod := range candidates {
		if err := c.evictPodUntilAccepted(drainContext, ctx, nodeName, pod, options); err != nil {
			return nil, err
		}
		evicted = append(evicted, pod.namespace+"/"+pod.name)
	}
	if err := c.waitForPodsDeleted(drainContext, ctx, nodeName, candidates, options.timeoutSeconds); err != nil {
		return nil, err
	}
	return map[string]any{
		"success": true, "action": "k8s.drain-node", "nodeName": nodeName,
		"unschedulable": true, "changed": cordon["changed"], "drained": true,
		"evictedPods": evicted, "evictedPodCount": len(evicted),
		"skippedDaemonSetPods": skippedDaemonSets,
		"message":              fmt.Sprintf("Drained node %s; evicted %d pod(s) and left it cordoned.", nodeName, len(evicted)),
	}, nil
}

func (c *kubernetesClient) listPodsOnNode(ctx context.Context, nodeName string) ([]map[string]any, error) {
	items := make([]map[string]any, 0)
	continuation := ""
	seen := map[string]struct{}{}
	for len(items) < kubernetesMaxListItems {
		remaining := kubernetesMaxListItems - len(items)
		limit := kubernetesListPageSize
		if remaining < limit {
			limit = remaining
		}
		query := url.Values{
			"fieldSelector": {"spec.nodeName=" + nodeName},
			"limit":         {strconv.Itoa(limit)},
		}
		if continuation != "" {
			query.Set("continue", continuation)
		}
		value, _, err := c.request(ctx, http.MethodGet, "/api/v1/pods", query, nil, "")
		if err != nil {
			return nil, err
		}
		items = append(items, objectSlice(value["items"])...)
		continuation = stringValue(objectValue(value["metadata"])["continue"])
		if continuation == "" {
			return items, nil
		}
		if _, exists := seen[continuation]; exists {
			return nil, newKubernetesError("Kubernetes API pagination repeated a continue token while listing node pods", false)
		}
		seen[continuation] = struct{}{}
	}
	return nil, newKubernetesError(fmt.Sprintf("node %s has more than %d pods; refusing an incomplete drain", nodeName, kubernetesMaxListItems), false)
}

func selectDrainPods(pods []map[string]any, options kubernetesDrainOptions) ([]kubernetesDrainPod, []string, error) {
	candidates := make([]kubernetesDrainPod, 0, len(pods))
	skippedDaemonSets := make([]string, 0)
	for _, pod := range pods {
		metadata := objectValue(pod["metadata"])
		spec := objectValue(pod["spec"])
		status := objectValue(pod["status"])
		name := stringValue(metadata["name"])
		namespace := stringValue(metadata["namespace"])
		if name == "" || namespace == "" {
			return nil, nil, newKubernetesError("Kubernetes returned a node pod without metadata.name or metadata.namespace", false)
		}
		qualifiedName := namespace + "/" + name
		phase := stringValue(status["phase"])
		if metadata["deletionTimestamp"] != nil || phase == "Succeeded" || phase == "Failed" {
			continue
		}
		if stringValue(objectValue(metadata["annotations"])["kubernetes.io/config.mirror"]) != "" {
			continue
		}
		ownerKind, controlled := podController(metadata)
		if ownerKind == "DaemonSet" {
			if !options.ignoreDaemonSets {
				return nil, nil, newKubernetesError(fmt.Sprintf("cannot drain %s: DaemonSet-managed pod %s requires ignoreDaemonSets=true", stringValue(spec["nodeName"]), qualifiedName), false)
			}
			skippedDaemonSets = append(skippedDaemonSets, qualifiedName)
			continue
		}
		if podUsesEmptyDir(spec) && !options.deleteEmptyDirData {
			return nil, nil, newKubernetesError(fmt.Sprintf("cannot drain node: pod %s uses emptyDir data; set deleteEmptyDirData=true to allow deletion", qualifiedName), false)
		}
		if !controlled && !options.force {
			return nil, nil, newKubernetesError(fmt.Sprintf("cannot drain node: pod %s has no controller; set force=true to allow eviction", qualifiedName), false)
		}
		candidates = append(candidates, kubernetesDrainPod{name: name, namespace: namespace})
	}
	return candidates, skippedDaemonSets, nil
}

func podController(metadata map[string]any) (string, bool) {
	for _, owner := range objectSlice(metadata["ownerReferences"]) {
		controller, _ := owner["controller"].(bool)
		if controller {
			return stringValue(owner["kind"]), true
		}
	}
	return "", false
}

func podUsesEmptyDir(spec map[string]any) bool {
	for _, volume := range objectSlice(spec["volumes"]) {
		if _, exists := volume["emptyDir"]; exists {
			return true
		}
	}
	return false
}

func (c *kubernetesClient) evictPodUntilAccepted(drainContext, parent context.Context, nodeName string, pod kubernetesDrainPod, options kubernetesDrainOptions) error {
	path := "/api/v1/namespaces/" + url.PathEscape(pod.namespace) + "/pods/" + url.PathEscape(pod.name) + "/eviction"
	body := map[string]any{
		"apiVersion": "policy/v1",
		"kind":       "Eviction",
		"metadata": map[string]any{
			"name": pod.name, "namespace": pod.namespace,
		},
		"deleteOptions": map[string]any{"gracePeriodSeconds": options.gracePeriodSeconds},
	}
	for {
		_, status, err := c.request(drainContext, http.MethodPost, path, nil, body, "", http.StatusNotFound, http.StatusTooManyRequests)
		if err != nil {
			return err
		}
		switch status {
		case http.StatusNotFound:
			return nil
		case http.StatusTooManyRequests:
			if err := waitForDrainRetry(drainContext, parent, nodeName, pod, options.timeoutSeconds); err != nil {
				return err
			}
			continue
		default:
			return nil
		}
	}
}

func (c *kubernetesClient) waitForPodsDeleted(drainContext, parent context.Context, nodeName string, pods []kubernetesDrainPod, timeoutSeconds int) error {
	remaining := append([]kubernetesDrainPod(nil), pods...)
	for len(remaining) > 0 {
		pending := remaining[:0]
		for _, pod := range remaining {
			path := "/api/v1/namespaces/" + url.PathEscape(pod.namespace) + "/pods/" + url.PathEscape(pod.name)
			_, status, err := c.request(drainContext, http.MethodGet, path, nil, nil, "", http.StatusNotFound)
			if err != nil {
				return err
			}
			if status != http.StatusNotFound {
				pending = append(pending, pod)
			}
		}
		remaining = pending
		if len(remaining) == 0 {
			return nil
		}
		if err := waitForDrainRetry(drainContext, parent, nodeName, remaining[0], timeoutSeconds); err != nil {
			return err
		}
	}
	return nil
}

func waitForDrainRetry(drainContext, parent context.Context, nodeName string, pod kubernetesDrainPod, timeoutSeconds int) error {
	timer := time.NewTimer(drainPollInterval)
	defer timer.Stop()
	select {
	case <-drainContext.Done():
		if parent.Err() != nil {
			return newKubernetesError("cluster task lease was lost while draining node", true)
		}
		return newKubernetesError(fmt.Sprintf("drain node %s timed out after %d seconds while evicting %s/%s; a PodDisruptionBudget or pod termination may be blocking eviction", nodeName, timeoutSeconds, pod.namespace, pod.name), true)
	case <-timer.C:
		return nil
	}
}
