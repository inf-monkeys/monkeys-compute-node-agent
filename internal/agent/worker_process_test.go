package agent

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWorkerProcessLifecycleMapsLogicalWorkspaceAndProtectsSecrets(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process identity uses Linux /proc")
	}
	workspace := t.TempDir()
	manager, err := newWorkerProcessManager(workspace)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.create(t.Context(), "runtime-1", map[string]any{
		"runtimeId": "runtime-1", "processId": "runtime-1", "command": []any{"sh", "-c", "printf %s \"$TOKEN\" > /workspace/value.txt; sleep 30"},
		"logicalWorkspacePath": "/workspace", "workspacePath": ".monkeys/runtimes/runtime-1", "workingDir": "/workspace",
		"secretEnv": map[string]any{"TOKEN": "secret-value"}, "secretFiles": []any{map[string]any{"path": ".monkeys/run/token", "content": "file-secret"}}, "autoStart": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["running"] != true {
		t.Fatalf("expected process running: %#v", result)
	}
	runtimeWorkspace := filepath.Join(workspace, ".monkeys", "runtimes", "runtime-1")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		value, readErr := os.ReadFile(filepath.Join(runtimeWorkspace, "value.txt"))
		if readErr == nil && string(value) == "secret-value" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if value, err := os.ReadFile(filepath.Join(runtimeWorkspace, "value.txt")); err != nil || string(value) != "secret-value" {
		t.Fatalf("logical workspace not mapped: %q %v", value, err)
	}
	secretPath := filepath.Join(runtimeWorkspace, ".monkeys", "run", "token")
	info, err := os.Stat(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secret mode %o", info.Mode().Perm())
	}
	stateBytes, err := os.ReadFile(filepath.Join(workspace, ".monkeys", "worker-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stateBytes), "file-secret") {
		t.Fatal("secret file content persisted in worker state")
	}
	if strings.Contains(string(stateBytes), "secret-value") {
		t.Fatal("secret environment persisted in worker state")
	}
	stopped, err := manager.stop("runtime-1", map[string]any{"processId": "runtime-1", "timeoutSeconds": 1})
	if err != nil {
		t.Fatal(err)
	}
	if stopped["running"] == true {
		t.Fatal("process still running")
	}
	deleted, err := manager.delete("runtime-1", map[string]any{"processId": "runtime-1"})
	if err != nil {
		t.Fatal(err)
	}
	if deleted["deleted"] != true {
		t.Fatalf("unexpected delete: %#v", deleted)
	}
}

func TestWorkerInspectIncludesBoundedProcessAndWorkspaceMetrics(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux /proc metrics")
	}
	workspace := t.TempDir()
	manager, err := newWorkerProcessManager(workspace)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.create(t.Context(), "metrics-1", map[string]any{
		"command":              []any{"sh", "-c", "dd if=/dev/zero of=/workspace/data.bin bs=1024 count=64 >/dev/null 2>&1; sleep 30"},
		"logicalWorkspacePath": "/workspace", "workspacePath": ".monkeys/runtimes/metrics-1", "autoStart": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.delete("metrics-1", map[string]any{})
	metrics, ok := result["metrics"].(map[string]any)
	if !ok {
		t.Fatalf("metrics missing from inspect: %#v", result)
	}
	process, ok := metrics["process"].(map[string]any)
	if !ok || process["rssBytes"] == nil || process["cpuTimeMillis"] == nil || process["readBytes"] == nil || process["writeBytes"] == nil {
		t.Fatalf("process metrics missing: %#v", process)
	}
	workspaceMetrics, ok := metrics["workspace"].(map[string]any)
	if !ok || workspaceMetrics["filesystem"] == nil || workspaceMetrics["scannedEntries"] == nil {
		t.Fatalf("workspace metrics missing: %#v", workspaceMetrics)
	}
	if scanned := workspaceMetrics["scannedEntries"].(int); scanned > maxWorkerWorkspaceEntries {
		t.Fatalf("workspace scan exceeded cap: %d", scanned)
	}
}

func TestWorkerWorkspaceMetricsCapsDirectoryScan(t *testing.T) {
	workspace := t.TempDir()
	for index := 0; index < maxWorkerWorkspaceEntries+100; index++ {
		path := filepath.Join(workspace, strconv.Itoa(index))
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	metrics := collectWorkerWorkspaceMetrics(workspace)
	if metrics["scanTruncated"] != true || metrics["scannedEntries"].(int) > maxWorkerWorkspaceEntries {
		t.Fatalf("workspace scan was not bounded: %#v", metrics)
	}
}

func TestWorkerExecBoundsOutputAndMapsWorkspace(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux process groups")
	}
	workspace := t.TempDir()
	manager, _ := newWorkerProcessManager(workspace)
	result, err := manager.exec(context.Background(), "exec-1", map[string]any{"command": []any{"sh", "-c", "pwd; head -c 4096 /dev/zero | tr '\\0' x"}, "logicalWorkspacePath": "/workspace", "workspacePath": ".monkeys/runtimes/exec-1", "workingDir": "/workspace", "outputLimitBytes": 1024})
	if err != nil {
		t.Fatal(err)
	}
	if result["exitCode"] != 0 {
		t.Fatalf("%#v", result)
	}
	if result["stdoutTruncated"] != true {
		t.Fatal("expected truncation")
	}
	if !strings.HasPrefix(result["stdout"].(string), "/workspace") {
		t.Fatalf("workspace not redacted: %q", result["stdout"])
	}
}

func TestWorkerFilesystemRejectsTraversalAndSymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	filesystem, _ := newWorkerFilesystem(workspace)
	runtimeID := "fs-1"
	if _, err := filesystem.execute("filesystem.write", runtimeID, map[string]any{"path": "../escape", "contentBase64": base64.StdEncoding.EncodeToString([]byte("no"))}); err == nil {
		t.Fatal("expected traversal rejection")
	}
	root := filepath.Join(workspace, ".monkeys", "runtimes", runtimeID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := filesystem.execute("filesystem.write", runtimeID, map[string]any{"path": "link/escape", "contentBase64": base64.StdEncoding.EncodeToString([]byte("no"))}); err == nil {
		t.Fatal("expected symlink escape rejection")
	}
}

func TestWorkerHTTPRequiresRunningDeclaredProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux process identity")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })}
	go server.Serve(listener)
	defer server.Close()
	workspace := t.TempDir()
	manager, _ := newWorkerProcessManager(workspace)
	_, err = manager.create(t.Context(), "http-1", map[string]any{"command": []any{"sleep", "30"}, "servicePort": port, "autoStart": true})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.delete("http-1", map[string]any{})
	result, err := executeWorkerHTTPRequest(t.Context(), manager, "http-1", map[string]any{"request": map[string]any{"port": port, "method": "GET", "path": "/", "maxResponseBytes": 1024}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := base64.StdEncoding.DecodeString(result["bodyBase64"].(string))
	if string(body) != "ok" {
		t.Fatalf("body %q", body)
	}
	if _, err := executeWorkerHTTPRequest(t.Context(), manager, "http-1", map[string]any{"request": map[string]any{"port": port + 1, "path": "/"}}); err == nil {
		t.Fatal("expected undeclared port rejection")
	}
	_ = strconv.Itoa(port)
}

func TestWorkerModeStrictTaskAllowlist(t *testing.T) {
	runtimeID := "runtime-1"
	_, err := executeWorkerTask(t.Context(), Config{Mode: "worker", Workspace: t.TempDir()}, ClaimedTask{TaskType: "k8s.inspect-runtime", RuntimeID: &runtimeID, Payload: map[string]any{}})
	if err == nil {
		t.Fatal("expected cluster task rejection")
	}
}

func TestWorkerProcessImmediateFailureIsReportedByAutoStartAndStart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process identity uses Linux /proc")
	}
	for _, test := range []struct {
		name      string
		autoStart bool
		exitCode  int
	}{
		{name: "auto start", autoStart: true, exitCode: 7},
		{name: "explicit start", autoStart: false, exitCode: 9},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, err := newWorkerProcessManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			result, createErr := manager.create(t.Context(), "exit-1", map[string]any{
				"command":   []any{"sh", "-c", "printf 'token=%s' \"$TOKEN\" >&2; exit " + strconv.Itoa(test.exitCode)},
				"secretEnv": map[string]any{"TOKEN": "must-not-leak"}, "autoStart": test.autoStart,
			})
			if !test.autoStart {
				if createErr != nil {
					t.Fatal(createErr)
				}
				result, createErr = manager.start("exit-1", map[string]any{})
			}
			if createErr == nil || !strings.Contains(createErr.Error(), "exited during startup with code "+strconv.Itoa(test.exitCode)) {
				t.Fatalf("expected startup failure, got result=%#v err=%v", result, createErr)
			}
			if strings.Contains(createErr.Error(), "must-not-leak") || !strings.Contains(createErr.Error(), "[REDACTED]") {
				t.Fatalf("startup diagnostic was not safely redacted: %v", createErr)
			}
			if result["startupFailed"] != true {
				t.Fatalf("startup failure missing from result: %#v", result)
			}
			lastExitCode, ok := result["lastExitCode"].(*int)
			if !ok || lastExitCode == nil || *lastExitCode != test.exitCode {
				t.Fatalf("exit code missing from result: %#v", result)
			}
			state, loadErr := manager.store.load()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			record := state.Processes["exit-1"]
			if record.LastExitCode == nil || *record.LastExitCode != test.exitCode || record.PID != 0 || record.PIDStartMarker != "" || record.Status != "stopped" || record.StoppedAt == 0 {
				t.Fatalf("unexpected exit state: %#v", record)
			}
		})
	}
}

func TestCappedWorkerLogHasBoundedCurrentAndBackupFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "process.log")
	logFile, err := openCappedWorkerLog(path, 32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := logFile.Write([]byte(strings.Repeat("a", 128))); err != nil {
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 32 {
		t.Fatalf("current log must be capped at 32 bytes: %v %v", info, err)
	}
	logFile, err = openCappedWorkerLog(path, 32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := logFile.Write([]byte("next")); err != nil {
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path + ".1"); err != nil || info.Size() != 32 {
		t.Fatalf("backup log must be capped at 32 bytes: %v %v", info, err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 4 {
		t.Fatalf("new current log is unexpected: %v %v", info, err)
	}
}
