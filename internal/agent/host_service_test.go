package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const systemctlServiceState = `Id=sshd.service
Names=sshd.service ssh.service
Description=OpenSSH server daemon
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled
FragmentPath=/usr/lib/systemd/system/sshd.service
`

func stubHostSystemctl(t *testing.T, runner func(context.Context, ...string) (string, error)) {
	t.Helper()
	originalLookPath := lookPathForHostService
	originalRunner := runHostSystemctl
	lookPathForHostService = func(string) (string, error) { return "/usr/bin/systemctl", nil }
	runHostSystemctl = runner
	t.Cleanup(func() {
		lookPathForHostService = originalLookPath
		runHostSystemctl = originalRunner
	})
}

func TestHostServiceInspectReturnsStructuredSystemdState(t *testing.T) {
	var calls [][]string
	stubHostSystemctl(t, func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string{}, args...))
		return systemctlServiceState, nil
	})

	result := executeAction(t.Context(), Config{Mode: "host"}, PlanAction{
		Type:    "host.service.inspect",
		Payload: map[string]any{"unit": "sshd.service"},
	})
	if !result.Success || result.Planned || result.DryRun {
		t.Fatalf("expected a completed inspection, got: %+v", result)
	}
	if len(calls) != 1 || len(calls[0]) < 2 || calls[0][0] != "show" || calls[0][len(calls[0])-1] != "sshd.service" {
		t.Fatalf("unexpected systemctl argv: %+v", calls)
	}
	state, ok := result.Details["systemd"].(map[string]any)
	if !ok {
		t.Fatalf("expected structured systemd state: %+v", result.Details)
	}
	for key, expected := range map[string]any{
		"ActiveState":   "active",
		"SubState":      "running",
		"UnitFileState": "enabled",
		"LoadState":     "loaded",
	} {
		if state[key] != expected {
			t.Fatalf("unexpected %s: %+v", key, state)
		}
	}
	if _, err := json.Marshal(result.Details); err != nil {
		t.Fatalf("service result must be JSON serializable: %v", err)
	}
	artifacts, ok := result.Artifacts["hostServiceResults"].([]map[string]any)
	if !ok || len(artifacts) != 1 {
		t.Fatalf("expected service result completion artifact: %+v", result.Artifacts)
	}
}

func TestHostServiceMutationsUseSystemctlArgumentArraysThenInspect(t *testing.T) {
	for _, actionType := range []string{"host.service.start", "host.service.stop", "host.service.restart", "host.service.enable", "host.service.disable"} {
		t.Run(actionType, func(t *testing.T) {
			var calls [][]string
			stubHostSystemctl(t, func(_ context.Context, args ...string) (string, error) {
				calls = append(calls, append([]string{}, args...))
				if args[0] == "show" {
					return systemctlServiceState, nil
				}
				return "", nil
			})
			result := executeAction(t.Context(), Config{Mode: "host", AllowInstall: true}, PlanAction{
				Type:    actionType,
				Payload: map[string]any{"unit": "worker@1.service"},
			})
			if !result.Success {
				t.Fatalf("expected mutation success, got: %+v", result)
			}
			operation := strings.TrimPrefix(actionType, "host.service.")
			expected := []string{operation, "worker@1.service"}
			if len(calls) != 2 || !reflect.DeepEqual(calls[0], expected) || calls[1][0] != "show" {
				t.Fatalf("expected exact mutation and inspection argv, got: %+v", calls)
			}
		})
	}
}

func TestHostServiceMutationHonorsPrivilegedDryRunGate(t *testing.T) {
	stubHostSystemctl(t, func(_ context.Context, args ...string) (string, error) {
		t.Fatalf("dry-run must not invoke systemctl: %+v", args)
		return "", nil
	})
	result := executeAction(t.Context(), Config{Mode: "host", DryRun: true}, PlanAction{
		Type:    "host.service.restart",
		Payload: map[string]any{"unit": "sshd.service"},
	})
	if !result.Success || !result.Planned || !result.DryRun {
		t.Fatalf("expected planned dry-run success, got: %+v", result)
	}
	if !reflect.DeepEqual(result.Command, []string{"systemctl", "restart", "sshd.service"}) {
		t.Fatalf("unexpected dry-run argv: %+v", result.Command)
	}
}

func TestHostServiceRejectsUnsafeUnitsAndNonHostModes(t *testing.T) {
	invalidUnits := []string{
		"",
		"sshd",
		"../sshd.service",
		"-sshd.service",
		"sshd.service --now",
		"sshd.service;reboot",
		"你好.service",
		strings.Repeat("a", maxSystemdServiceUnitLength-len(".service")+1) + ".service",
	}
	for _, unit := range invalidUnits {
		t.Run(fmt.Sprintf("unit=%q", unit), func(t *testing.T) {
			result := executeAction(t.Context(), Config{Mode: "host"}, PlanAction{Type: "host.service.inspect", Payload: map[string]any{"unit": unit}})
			if result.Success || !strings.Contains(result.Message, "ASCII systemd service unit") {
				t.Fatalf("expected strict unit rejection, got: %+v", result)
			}
		})
	}
	for _, mode := range []string{"worker", "cluster"} {
		result := executeAction(t.Context(), Config{Mode: mode}, PlanAction{Type: "host.service.inspect", Payload: map[string]any{"unit": "sshd.service"}})
		if result.Success || !strings.Contains(result.Message, "host Agent mode") {
			t.Fatalf("expected mode %s rejection, got: %+v", mode, result)
		}
	}
}

func TestHostServiceRejectsSynchronousSelfTermination(t *testing.T) {
	for _, actionType := range []string{"host.service.stop", "host.service.restart", "host.service.disable"} {
		result := executeAction(t.Context(), Config{Mode: "host", AllowInstall: true}, PlanAction{
			Type:    actionType,
			Payload: map[string]any{"unit": computeNodeAgentSystemdUnit},
		})
		if result.Success || !strings.Contains(result.Message, "Agent lifecycle workflow") {
			t.Fatalf("expected %s to reject self-termination, got: %+v", actionType, result)
		}
	}
	stubHostSystemctl(t, func(context.Context, ...string) (string, error) {
		return systemctlServiceState, nil
	})
	inspect := executeAction(t.Context(), Config{Mode: "host"}, PlanAction{
		Type:    "host.service.inspect",
		Payload: map[string]any{"unit": computeNodeAgentSystemdUnit},
	})
	if !inspect.Success {
		t.Fatalf("self inspection must remain available: %+v", inspect)
	}
}

func TestHostServiceReportsUnavailableSystemdAndPermissionFailures(t *testing.T) {
	t.Run("systemctl unavailable", func(t *testing.T) {
		originalLookPath := lookPathForHostService
		lookPathForHostService = func(string) (string, error) { return "", errors.New("not found") }
		t.Cleanup(func() { lookPathForHostService = originalLookPath })
		result := executeAction(t.Context(), Config{Mode: "host"}, PlanAction{Type: "host.service.inspect", Payload: map[string]any{"unit": "sshd.service"}})
		if result.Success || !strings.Contains(result.Message, "systemctl is unavailable") {
			t.Fatalf("expected unavailable systemctl error, got: %+v", result)
		}
	})

	t.Run("systemd unavailable", func(t *testing.T) {
		stubHostSystemctl(t, func(context.Context, ...string) (string, error) {
			return "System has not been booted with systemd as init system (PID 1).", errors.New("exit status 1")
		})
		result := executeAction(t.Context(), Config{Mode: "host"}, PlanAction{Type: "host.service.inspect", Payload: map[string]any{"unit": "sshd.service"}})
		if result.Success || !strings.Contains(result.Message, "systemd is not running or unavailable") {
			t.Fatalf("expected unavailable systemd error, got: %+v", result)
		}
	})

	t.Run("permission denied", func(t *testing.T) {
		stubHostSystemctl(t, func(context.Context, ...string) (string, error) {
			return "Interactive authentication required.", errors.New("exit status 1")
		})
		result := executeAction(t.Context(), Config{Mode: "host", AllowInstall: true}, PlanAction{Type: "host.service.restart", Payload: map[string]any{"unit": "sshd.service"}})
		if result.Success || !strings.Contains(result.Message, "systemd permission denied") {
			t.Fatalf("expected permission error, got: %+v", result)
		}
	})
}
