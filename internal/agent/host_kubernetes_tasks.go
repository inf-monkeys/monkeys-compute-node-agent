package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

func executeHostKubernetesTask(ctx context.Context, taskType string, payload map[string]any) (map[string]any, error) {
	allowed := map[string]bool{
		"k8s.apply-runtime": true, "k8s.scale-runtime": true, "k8s.restart-runtime": true,
		"k8s.inspect-runtime": true, "k8s.delete-runtime": true, "k8s.exec-runtime": true,
		"k8s.cordon-node": true, "k8s.uncordon-node": true, "k8s.drain-node": true,
	}
	if !allowed[taskType] {
		return nil, newKubernetesError(fmt.Sprintf("%s is not allowed in host mode", firstNonEmpty(taskType, "<empty>")), false)
	}
	kubectl := resolveKubectlCommand()
	if len(kubectl) == 0 {
		return nil, newKubernetesError("host Kubernetes tasks require kubectl or k3s", false)
	}
	if isKubernetesNodeTask(taskType) {
		return executeHostKubernetesNodeTask(ctx, kubectl, taskType, payload)
	}
	if definition := objectValue(payload["definition"]); len(definition) > 0 && taskType == "k8s.apply-runtime" {
		payload = definition
	}
	runtimeID, namespace, deployment, err := runtimeIdentity(payload, false)
	if err != nil {
		return nil, err
	}
	switch taskType {
	case "k8s.apply-runtime":
		manifests, err := runtimeManifestObjects(payload["manifests"])
		if err != nil {
			return nil, err
		}
		if err := validateRuntimeManifests(runtimeID, namespace, manifests); err != nil {
			return nil, err
		}
		manifestDocuments := make([]string, 0, len(manifests))
		for _, manifest := range manifests {
			encoded, err := json.Marshal(manifest)
			if err != nil {
				return nil, newKubernetesError(fmt.Sprintf("encode Kubernetes manifest: %v", err), false)
			}
			manifestDocuments = append(manifestDocuments, string(encoded))
		}
		temporary, err := os.CreateTemp("", "monkeys-runtime-*.yaml")
		if err != nil {
			return nil, err
		}
		path := temporary.Name()
		defer os.Remove(path)
		if _, err := temporary.WriteString(strings.Join(manifestDocuments, "\n---\n") + "\n"); err != nil {
			temporary.Close()
			return nil, err
		}
		if err := temporary.Close(); err != nil {
			return nil, err
		}
		command := append(append([]string{}, kubectl...), "apply", "-f", path)
		output, err := runHostKubectl(ctx, command, nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "action": taskType, "runtimeId": runtimeID, "namespace": namespace, "message": strings.TrimSpace(output)}, nil
	case "k8s.scale-runtime":
		deploymentSelector, err := resolveHostRuntimeDeployment(ctx, kubectl, runtimeID, namespace, deployment)
		if err != nil {
			return nil, err
		}
		replicas := intValue(payload["replicas"])
		if replicas < 0 {
			return nil, newKubernetesError("replicas must be zero or greater", false)
		}
		command := append(append([]string{}, kubectl...), "-n", namespace, "scale", "deployment", deploymentSelector, "--replicas", fmt.Sprint(replicas))
		output, err := runHostKubectl(ctx, command, nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "action": taskType, "runtimeId": runtimeID, "namespace": namespace, "deploymentName": deploymentSelector, "desiredReplicas": replicas, "message": strings.TrimSpace(output)}, nil
	case "k8s.restart-runtime":
		deploymentSelector, err := resolveHostRuntimeDeployment(ctx, kubectl, runtimeID, namespace, deployment)
		if err != nil {
			return nil, err
		}
		command := append(append([]string{}, kubectl...), "-n", namespace, "rollout", "restart", "deployment/"+deploymentSelector)
		output, err := runHostKubectl(ctx, command, nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "action": taskType, "runtimeId": runtimeID, "namespace": namespace, "deploymentName": deploymentSelector, "restartedAt": time.Now().UnixMilli(), "message": strings.TrimSpace(output)}, nil
	case "k8s.inspect-runtime":
		command := append(append([]string{}, kubectl...), "-n", namespace, "get", "deployment,pod", "-l", monkeysRuntimeLabel+"="+runtimeID, "-o", "json")
		output, err := runHostKubectl(ctx, command, nil)
		if err != nil {
			return nil, err
		}
		return inspectHostRuntimeJSON(runtimeID, namespace, output)
	case "k8s.delete-runtime":
		command := append(append([]string{}, kubectl...), "-n", namespace, "delete", "deployment,service,secret,pvc", "-l", monkeysRuntimeLabel+"="+runtimeID, "--ignore-not-found=true")
		output, err := runHostKubectl(ctx, command, nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "action": taskType, "runtimeId": runtimeID, "namespace": namespace, "deleted": true, "message": strings.TrimSpace(output)}, nil
	case "k8s.exec-runtime":
		return executeHostRuntimeCommand(ctx, kubectl, payload, runtimeID, namespace)
	}
	return nil, newKubernetesError("unsupported host Kubernetes task", false)
}

func resolveHostRuntimeDeployment(ctx context.Context, kubectl []string, runtimeID, namespace, deployment string) (string, error) {
	if strings.TrimSpace(deployment) != "" {
		return deployment, nil
	}
	command := append(append([]string{}, kubectl...), "-n", namespace, "get", "deployment", "-l", monkeysRuntimeLabel+"="+runtimeID, "-o", "jsonpath={.items[0].metadata.name}")
	output, err := runHostKubectl(ctx, command, nil)
	if err != nil {
		return "", err
	}
	selected := strings.TrimSpace(output)
	if selected == "" {
		return "", newKubernetesError("runtime deployment was not found", true)
	}
	return selected, nil
}

func runHostKubectl(ctx context.Context, command []string, environment map[string]string) (string, error) {
	if len(command) == 0 {
		return "", newKubernetesError("kubectl command is empty", false)
	}
	if defaults := hostKubernetesEnvironment(command); len(defaults) > 0 {
		if environment == nil {
			environment = defaults
		} else {
			for key, value := range defaults {
				if environment[key] == "" {
					environment[key] = value
				}
			}
		}
	}
	output, err := runCommandWithEnv(ctx, environment, command...)
	if err != nil {
		if ctx.Err() != nil {
			return "", newKubernetesError("host Kubernetes task lease was lost", true)
		}
		return "", newKubernetesError(strings.TrimSpace(output+"\n"+err.Error()), true)
	}
	return output, nil
}

func hostKubernetesEnvironment(command []string) map[string]string {
	if len(command) == 0 || command[0] != "kubectl" || strings.TrimSpace(os.Getenv("KUBECONFIG")) != "" {
		return nil
	}
	defaultPath := "/etc/rancher/k3s/k3s.yaml"
	if _, err := os.Stat(defaultPath); err != nil {
		return nil
	}
	return map[string]string{"KUBECONFIG": defaultPath}
}

func inspectHostRuntimeJSON(runtimeID, namespace, output string) (map[string]any, error) {
	var value map[string]any
	if err := json.Unmarshal([]byte(output), &value); err != nil {
		return nil, newKubernetesError("decode kubectl runtime inspection", true)
	}
	items := objectSlice(value["items"])
	var deployment map[string]any
	pods := []map[string]any{}
	for _, item := range items {
		kind := stringValue(item["kind"])
		if kind == "Deployment" {
			deployment = item
		} else if kind == "Pod" {
			pods = append(pods, item)
		}
	}
	if len(deployment) == 0 {
		return absentRuntime(runtimeID, namespace), nil
	}
	name := stringValue(objectValue(deployment["metadata"])["name"])
	return inspectRuntimeResult(runtimeID, namespace, name, deployment, pods), nil
}

func executeHostRuntimeCommand(ctx context.Context, kubectl []string, payload map[string]any, runtimeID, namespace string) (map[string]any, error) {
	pod := stringValue(payload["podName"])
	if pod == "" {
		lookup := append(append([]string{}, kubectl...), "-n", namespace, "get", "pod", "-l", monkeysRuntimeLabel+"="+runtimeID, "-o", "jsonpath={.items[0].metadata.name}")
		output, err := runHostKubectl(ctx, lookup, nil)
		if err != nil {
			return nil, err
		}
		pod = strings.TrimSpace(output)
	}
	if pod == "" {
		return nil, newKubernetesError("no runtime pod found", true)
	}
	command := stringSlice(payload["command"])
	if len(command) == 0 {
		return nil, newKubernetesError("command must be a non-empty array", false)
	}
	arguments := append(append([]string{}, kubectl...), "-n", namespace, "exec", pod, "-c", firstNonEmpty(stringValue(payload["container"]), "runtime"), "--")
	arguments = append(arguments, command...)
	output, err := runHostKubectl(ctx, arguments, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "action": "k8s.exec-runtime", "runtimeId": runtimeID, "namespace": namespace, "podName": pod, "exitCode": 0, "stdout": output, "stderr": ""}, nil
}
