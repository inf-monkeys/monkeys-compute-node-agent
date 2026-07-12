package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

type kubernetesDeploymentList struct {
	Items []struct {
		Metadata struct {
			Name      string            `json:"name"`
			Namespace string            `json:"namespace"`
			Labels    map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			Replicas *int `json:"replicas"`
		} `json:"spec"`
		Status struct {
			ReadyReplicas     int `json:"readyReplicas"`
			AvailableReplicas int `json:"availableReplicas"`
			Replicas          int `json:"replicas"`
		} `json:"status"`
	} `json:"items"`
}

type kubernetesPodList struct {
	Items []struct {
		Metadata struct {
			Name      string            `json:"name"`
			Namespace string            `json:"namespace"`
			Labels    map[string]string `json:"labels"`
		} `json:"metadata"`
		Status struct {
			Phase             string `json:"phase"`
			Reason            string `json:"reason"`
			Message           string `json:"message"`
			ContainerStatuses []struct {
				Ready bool `json:"ready"`
				State struct {
					Waiting *struct {
						Reason  string `json:"reason"`
						Message string `json:"message"`
					} `json:"waiting"`
					Terminated *struct {
						Reason  string `json:"reason"`
						Message string `json:"message"`
					} `json:"terminated"`
				} `json:"state"`
			} `json:"containerStatuses"`
		} `json:"status"`
	} `json:"items"`
}

func detectRuntimeStatuses(ctx context.Context) []RuntimeStatus {
	if len(resolveKubectlCommand()) == 0 {
		return nil
	}

	deploymentsOutput, err := runKubernetesCommand(ctx, "get", "deploy", "-A", "-l", "app.kubernetes.io/managed-by=monkeys-compute", "-o", "json")
	if err != nil || strings.TrimSpace(deploymentsOutput) == "" {
		return nil
	}
	var deployments kubernetesDeploymentList
	if err := json.Unmarshal([]byte(deploymentsOutput), &deployments); err != nil {
		return nil
	}
	if len(deployments.Items) == 0 {
		return nil
	}

	podsByRuntime := map[string][]RuntimePodInfo{}
	podsOutput, err := runKubernetesCommand(ctx, "get", "pods", "-A", "-l", "app.kubernetes.io/managed-by=monkeys-compute", "-o", "json")
	if err == nil && strings.TrimSpace(podsOutput) != "" {
		var pods kubernetesPodList
		if err := json.Unmarshal([]byte(podsOutput), &pods); err == nil {
			for _, pod := range pods.Items {
				runtimeID := strings.TrimSpace(pod.Metadata.Labels["monkeys/runtime-id"])
				if runtimeID == "" {
					continue
				}
				podInfo := RuntimePodInfo{
					Name:    pod.Metadata.Name,
					Phase:   pod.Status.Phase,
					Reason:  firstNonEmpty(pod.Status.Reason, firstContainerReason(pod.Status.ContainerStatuses)),
					Message: firstNonEmpty(pod.Status.Message, firstContainerMessage(pod.Status.ContainerStatuses)),
				}
				podInfo.Ready = podReady(pod.Status.ContainerStatuses)
				podsByRuntime[runtimeID] = append(podsByRuntime[runtimeID], podInfo)
			}
		}
	}

	observedAt := time.Now().UnixMilli()
	statuses := make([]RuntimeStatus, 0, len(deployments.Items))
	for _, deployment := range deployments.Items {
		runtimeID := strings.TrimSpace(deployment.Metadata.Labels["monkeys/runtime-id"])
		if runtimeID == "" {
			continue
		}
		desired := deployment.Status.Replicas
		if deployment.Spec.Replicas != nil {
			desired = *deployment.Spec.Replicas
		}
		pods := podsByRuntime[runtimeID]
		readyPodCount := 0
		for _, pod := range pods {
			if pod.Ready {
				readyPodCount++
			}
		}
		status, reason, message := deriveRuntimeStatusFromPods(desired, deployment.Status.ReadyReplicas, pods)
		statuses = append(statuses, RuntimeStatus{
			RuntimeID:         runtimeID,
			Namespace:         deployment.Metadata.Namespace,
			DeploymentName:    deployment.Metadata.Name,
			Status:            status,
			Reason:            reason,
			Message:           message,
			DesiredReplicas:   desired,
			ReadyReplicas:     deployment.Status.ReadyReplicas,
			AvailableReplicas: deployment.Status.AvailableReplicas,
			PodCount:          len(pods),
			ReadyPodCount:     readyPodCount,
			Pods:              pods,
			ObservedAt:        observedAt,
		})
	}
	return statuses
}

func deriveRuntimeStatusFromPods(desired int, readyReplicas int, pods []RuntimePodInfo) (string, string, string) {
	if desired <= 0 {
		return "stopped", "", "Runtime is scaled to zero."
	}
	if readyReplicas > 0 {
		return "running", "", "Runtime has ready replicas."
	}
	for _, pod := range pods {
		reason := strings.TrimSpace(pod.Reason)
		message := strings.TrimSpace(pod.Message)
		if isRuntimeFailureReason(reason) || strings.EqualFold(pod.Phase, "Failed") {
			return "error", reason, firstNonEmpty(message, "Runtime pod failed: "+reason)
		}
	}
	if len(pods) == 0 {
		return "starting", "", "Waiting for runtime pod to be created."
	}
	return "starting", firstPodReason(pods), firstNonEmpty(firstPodMessage(pods), "Waiting for runtime pod to become ready.")
}

func isRuntimeFailureReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "ErrImagePull", "ImagePullBackOff", "CreateContainerConfigError", "CrashLoopBackOff", "RunContainerError", "InvalidImageName", "Evicted", "Error":
		return true
	default:
		return false
	}
}

func podReady(statuses []struct {
	Ready bool `json:"ready"`
	State struct {
		Waiting *struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"waiting"`
		Terminated *struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"terminated"`
	} `json:"state"`
}) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, status := range statuses {
		if !status.Ready {
			return false
		}
	}
	return true
}

func firstContainerReason(statuses []struct {
	Ready bool `json:"ready"`
	State struct {
		Waiting *struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"waiting"`
		Terminated *struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"terminated"`
	} `json:"state"`
}) string {
	for _, status := range statuses {
		if status.State.Waiting != nil && strings.TrimSpace(status.State.Waiting.Reason) != "" {
			return strings.TrimSpace(status.State.Waiting.Reason)
		}
		if status.State.Terminated != nil && strings.TrimSpace(status.State.Terminated.Reason) != "" {
			return strings.TrimSpace(status.State.Terminated.Reason)
		}
	}
	return ""
}

func firstContainerMessage(statuses []struct {
	Ready bool `json:"ready"`
	State struct {
		Waiting *struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"waiting"`
		Terminated *struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"terminated"`
	} `json:"state"`
}) string {
	for _, status := range statuses {
		if status.State.Waiting != nil && strings.TrimSpace(status.State.Waiting.Message) != "" {
			return strings.TrimSpace(status.State.Waiting.Message)
		}
		if status.State.Terminated != nil && strings.TrimSpace(status.State.Terminated.Message) != "" {
			return strings.TrimSpace(status.State.Terminated.Message)
		}
	}
	return ""
}

func firstPodReason(pods []RuntimePodInfo) string {
	for _, pod := range pods {
		if strings.TrimSpace(pod.Reason) != "" {
			return strings.TrimSpace(pod.Reason)
		}
	}
	return ""
}

func firstPodMessage(pods []RuntimePodInfo) string {
	for _, pod := range pods {
		if strings.TrimSpace(pod.Message) != "" {
			return strings.TrimSpace(pod.Message)
		}
	}
	return ""
}
