package agent

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type ActionResult struct {
	ActionType string         `json:"actionType"`
	Success    bool           `json:"success"`
	Message    string         `json:"message,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
}

func executeAction(ctx context.Context, action PlanAction) ActionResult {
	switch action.Type {
	case "agent.register", "inspect", "noop":
		return ActionResult{
			ActionType: action.Type,
			Success:    true,
			Message:    "Action completed.",
		}
	case "k3s.preflight":
		return k3sPreflight(ctx, action)
	case "hami.preflight":
		return hamiPreflight(ctx, action)
	default:
		return ActionResult{
			ActionType: action.Type,
			Success:    false,
			Message:    "Unsupported action.",
		}
	}
}

func k3sPreflight(ctx context.Context, action PlanAction) ActionResult {
	details := map[string]any{
		"hasK3s":       commandExists("k3s"),
		"hasKubectl":   commandExists("kubectl"),
		"hasSystemctl": commandExists("systemctl"),
		"hasCurl":      commandExists("curl"),
		"hasWget":      commandExists("wget"),
	}
	if output, err := runCommand(ctx, "k3s", "--version"); err == nil {
		details["k3sVersion"] = firstLine(output)
	}
	if output, err := runCommand(ctx, "kubectl", "version", "--client=true", "--short"); err == nil {
		details["kubectlVersion"] = strings.TrimSpace(output)
	}
	return ActionResult{
		ActionType: action.Type,
		Success:    details["hasSystemctl"] == true && (details["hasCurl"] == true || details["hasWget"] == true),
		Message:    "K3s preflight completed.",
		Details:    details,
	}
}

func hamiPreflight(ctx context.Context, action PlanAction) ActionResult {
	gpus := detectNvidiaGPUs(ctx)
	details := map[string]any{
		"hasNvidiaSMI":      commandExists("nvidia-smi"),
		"hasKubectl":        commandExists("kubectl"),
		"nvidiaGPUCount":    totalGPUCount(gpus),
		"nvidiaGPUSummary":  gpus,
		"nodeLabeled":       false,
		"devicePluginReady": false,
	}
	hami := detectHAMi(ctx)
	for key, value := range hami {
		details[key] = value
	}
	return ActionResult{
		ActionType: action.Type,
		Success:    details["hasNvidiaSMI"] == true && details["hasKubectl"] == true,
		Message:    "HAMi preflight completed.",
		Details:    details,
	}
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(value), "\n")
	return line
}

func totalGPUCount(gpus []GPUInfo) int {
	total := 0
	for _, gpu := range gpus {
		if gpu.Count > 0 {
			total += gpu.Count
		}
	}
	return total
}

func eventForActionResult(result ActionResult) EventRequest {
	severity := "info"
	eventType := "node.plan.action.completed"
	if !result.Success {
		severity = "warning"
		eventType = "node.plan.action.failed"
		if result.Message == "Unsupported action." {
			eventType = "node.plan.action.unsupported"
		}
	}
	return EventRequest{
		EventType: eventType,
		Severity:  severity,
		Message:   fmt.Sprintf("%s: %s", result.ActionType, result.Message),
		Payload: map[string]any{
			"actionType": result.ActionType,
			"success":    result.Success,
			"details":    result.Details,
		},
	}
}
