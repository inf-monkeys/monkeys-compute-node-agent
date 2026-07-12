package agent

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	maxSystemdServiceUnitLength = 128
	maxSystemctlOutputBytes     = 8 * 1024
	computeNodeAgentSystemdUnit = "monkeys-compute-node-agent.service"
)

var (
	systemdServiceUnitPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@:-]*\.service$`)
	lookPathForHostService    = exec.LookPath
	runHostSystemctl          = runHostSystemctlCommand
)

var hostServiceActions = map[string]string{
	"host.service.inspect": "show",
	"host.service.start":   "start",
	"host.service.stop":    "stop",
	"host.service.restart": "restart",
	"host.service.enable":  "enable",
	"host.service.disable": "disable",
}

func executeHostServiceAction(ctx context.Context, cfg Config, action PlanAction) ActionResult {
	operation, allowed := hostServiceActions[action.Type]
	unit, unitErr := normalizeSystemdServiceUnit(action.Payload["unit"])
	if !allowed || unitErr != nil {
		message := "Unsupported host service action."
		if unitErr != nil {
			message = unitErr.Error()
		}
		return ActionResult{ActionType: action.Type, Success: false, Message: message}
	}
	if normalizedMode(cfg.Mode) != "host" {
		return ActionResult{ActionType: action.Type, Success: false, Message: "Host service actions require host Agent mode."}
	}
	if unit == computeNodeAgentSystemdUnit && (operation == "stop" || operation == "restart" || operation == "disable") {
		return ActionResult{
			ActionType: action.Type,
			Success:    false,
			Message:    fmt.Sprintf("Cannot %s %s through a synchronous Agent plan; use the Agent lifecycle workflow instead.", operation, unit),
		}
	}

	details := map[string]any{
		"unit":          unit,
		"operation":     strings.TrimPrefix(action.Type, "host.service."),
		"systemctlVerb": operation,
	}
	if operation != "show" && (cfg.DryRun || !cfg.AllowInstall) {
		return ActionResult{
			ActionType: action.Type,
			Success:    true,
			Planned:    true,
			DryRun:     true,
			Message:    fmt.Sprintf("systemctl %s prepared for %s.", operation, unit),
			Command:    []string{"systemctl", operation, unit},
			Details:    details,
		}
	}
	if _, err := lookPathForHostService("systemctl"); err != nil {
		return hostServiceFailure(action.Type, details, "systemctl is unavailable; this host does not expose systemd service control")
	}

	if operation != "show" {
		commandContext, cancel := context.WithTimeout(ctx, 60*time.Second)
		output, err := runHostSystemctl(commandContext, operation, unit)
		cancel()
		if err != nil {
			return hostServiceFailure(action.Type, details, classifySystemctlError(operation, unit, output, err))
		}
		if trimmed := truncateSystemctlOutput(output); trimmed != "" {
			details["output"] = trimmed
		}
	}

	state, err := inspectSystemdService(ctx, unit)
	if err != nil {
		return hostServiceFailure(action.Type, details, err.Error())
	}
	details["systemd"] = state
	return ActionResult{
		ActionType: action.Type,
		Success:    true,
		Message:    fmt.Sprintf("systemd service %s %s completed.", unit, operation),
		Command:    []string{"systemctl", operation, unit},
		Details:    details,
		Artifacts: map[string]any{
			"hostServiceResults": []map[string]any{details},
		},
	}
}

func inspectSystemdService(ctx context.Context, unit string) (map[string]any, error) {
	inspectContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := runHostSystemctl(
		inspectContext,
		"show",
		"--no-pager",
		"--property=Id,Names,Description,LoadState,ActiveState,SubState,UnitFileState,FragmentPath",
		unit,
	)
	if err != nil {
		return nil, fmt.Errorf("%s", classifySystemctlError("inspect", unit, output, err))
	}
	state := map[string]any{}
	for _, line := range strings.Split(output, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found || key == "" {
			continue
		}
		if key == "Names" {
			state[key] = strings.Fields(value)
			continue
		}
		state[key] = value
	}
	for _, required := range []string{"LoadState", "ActiveState", "SubState", "UnitFileState"} {
		if _, ok := state[required]; !ok {
			return nil, fmt.Errorf("systemctl inspect for %s returned no %s", unit, required)
		}
	}
	return state, nil
}

func normalizeSystemdServiceUnit(value any) (string, error) {
	unit := strings.TrimSpace(asString(value))
	if len(unit) == 0 || len(unit) > maxSystemdServiceUnitLength || !systemdServiceUnitPattern.MatchString(unit) {
		return "", fmt.Errorf("unit must be an ASCII systemd service unit ending in .service and no longer than 128 bytes")
	}
	return unit, nil
}

func runHostSystemctlCommand(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "systemctl", args...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func classifySystemctlError(operation, unit, output string, commandErr error) string {
	message := truncateSystemctlOutput(output)
	lower := strings.ToLower(message + " " + commandErr.Error())
	switch {
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "access denied"), strings.Contains(lower, "authentication is required"), strings.Contains(lower, "interactive authentication required"):
		return fmt.Sprintf("systemd permission denied while running %s for %s: %s", operation, unit, firstNonEmpty(message, commandErr.Error()))
	case strings.Contains(lower, "not been booted with systemd"), strings.Contains(lower, "failed to connect to bus"), strings.Contains(lower, "system has not been booted"):
		return fmt.Sprintf("systemd is not running or unavailable while inspecting %s: %s", unit, firstNonEmpty(message, commandErr.Error()))
	default:
		return fmt.Sprintf("systemctl %s failed for %s: %s", operation, unit, firstNonEmpty(message, commandErr.Error()))
	}
}

func truncateSystemctlOutput(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxSystemctlOutputBytes {
		return value
	}
	return value[:maxSystemctlOutputBytes] + "...<truncated>"
}

func hostServiceFailure(actionType string, details map[string]any, message string) ActionResult {
	return ActionResult{ActionType: actionType, Success: false, Message: message, Details: details}
}
