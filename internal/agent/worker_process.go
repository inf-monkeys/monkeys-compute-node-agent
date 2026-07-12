package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	workerOutputLimit     = 8 * 1024 * 1024
	workerProcessLogLimit = 8 * 1024 * 1024
	workerStartupGrace    = 350 * time.Millisecond
	workerStartupTailSize = 2048
)

type workerProcessManager struct {
	paths workerPaths
	store *workerStateStore
}

func newWorkerProcessManager(workspace string) (*workerProcessManager, error) {
	paths, err := newWorkerPaths(workspace)
	if err != nil {
		return nil, err
	}
	return &workerProcessManager{paths: paths, store: newWorkerStateStore(paths.root)}, nil
}

func (m *workerProcessManager) execute(ctx context.Context, taskType string, runtimeID string, payload map[string]any) (map[string]any, error) {
	switch taskType {
	case "process.create":
		if definition, ok := workerMap(payload["definition"]); ok {
			payload = definition
		}
		return m.create(ctx, runtimeID, payload)
	case "process.start":
		return m.start(runtimeID, payload)
	case "process.stop":
		return m.stop(runtimeID, payload)
	case "process.restart":
		if _, err := m.stop(runtimeID, payload); err != nil {
			return nil, err
		}
		return m.start(runtimeID, payload)
	case "process.delete":
		return m.delete(runtimeID, payload)
	case "process.inspect":
		return m.inspect(runtimeID, payload)
	case "process.exec":
		return m.exec(ctx, runtimeID, payload)
	default:
		return nil, permanentWorkerError("%s is not allowed in worker mode", taskType)
	}
}

func (m *workerProcessManager) create(ctx context.Context, runtimeID string, payload map[string]any) (map[string]any, error) {
	processID, err := processID(runtimeID, payload)
	if err != nil {
		return nil, err
	}
	record, secretWrites, err := m.definition(runtimeID, processID, payload)
	if err != nil {
		return nil, err
	}
	state, err := m.store.load()
	if err != nil {
		return nil, err
	}
	existing, exists := state.Processes[processID]
	wasRunning := exists && processRecordRunning(existing)
	if exists && existing.DefinitionHash == record.DefinitionHash {
		if err := writeWorkerSecrets(secretWrites); err != nil {
			return nil, err
		}
		if workerBool(payload["autoStart"]) || workerBool(payload["start"]) {
			inspection, err := m.inspect(runtimeID, map[string]any{"processId": processID})
			if err != nil || inspection["running"] == true {
				return inspection, err
			}
			return m.start(runtimeID, map[string]any{"processId": processID})
		}
		return m.inspect(runtimeID, map[string]any{"processId": processID})
	}
	if wasRunning {
		if _, err := m.stop(runtimeID, map[string]any{"processId": processID, "timeoutSeconds": payload["timeoutSeconds"]}); err != nil {
			return nil, err
		}
	}
	if err := writeWorkerSecrets(secretWrites); err != nil {
		return nil, err
	}
	record.Status = "created"
	if err := m.store.update(func(state *workerState) error {
		state.Processes[processID] = record
		return nil
	}); err != nil {
		return nil, err
	}
	removeWorkerSecrets(existing.SecretFiles, record.SecretFiles)
	if existing.SecretEnvPath != "" && existing.SecretEnvPath != record.SecretEnvPath {
		_ = os.Remove(existing.SecretEnvPath)
	}
	if wasRunning || workerBool(payload["autoStart"]) || workerBool(payload["start"]) {
		return m.start(runtimeID, map[string]any{"processId": processID})
	}
	return m.inspect(runtimeID, map[string]any{"processId": processID})
}

func (m *workerProcessManager) definition(runtimeID string, processID string, payload map[string]any) (workerProcessRecord, []workerSecretWrite, error) {
	if runtimeID == "" {
		runtimeID = strings.TrimSpace(workerString(payload["runtimeId"]))
	}
	if safeIdentifier(runtimeID) == "" {
		return workerProcessRecord{}, nil, permanentWorkerError("a valid runtimeId is required")
	}
	runtimeWorkspace, err := m.paths.runtimeWorkspace(runtimeID, payload["workspacePath"])
	if err != nil {
		return workerProcessRecord{}, nil, permanentWorkerError("%v", err)
	}
	if err := os.MkdirAll(runtimeWorkspace, 0o700); err != nil {
		return workerProcessRecord{}, nil, err
	}
	logicalRoot := strings.TrimSpace(workerString(payload["logicalWorkspacePath"]))
	if logicalRoot == "" {
		logicalRoot = "/workspace"
	}
	command, err := workerStringSlice(payload["command"])
	if err != nil || len(command) == 0 || command[0] == "" {
		return workerProcessRecord{}, nil, permanentWorkerError("command is required")
	}
	arguments, err := workerStringSlice(payload["args"])
	if err != nil {
		return workerProcessRecord{}, nil, permanentWorkerError("args must be an array")
	}
	command = append(command, arguments...)
	for index := range command {
		command[index] = mapLogicalWorkspace(command[index], logicalRoot, runtimeWorkspace)
	}
	workingDirValue := workerString(payload["workingDir"])
	if workingDirValue == "" {
		workingDirValue = logicalRoot
	}
	workingDir, err := resolveRuntimePath(runtimeWorkspace, logicalRoot, workingDirValue)
	if err != nil {
		return workerProcessRecord{}, nil, err
	}
	environment, err := workerStringMap(payload["env"])
	if err != nil {
		return workerProcessRecord{}, nil, permanentWorkerError("%v", err)
	}
	secretEnv, err := workerStringMap(payload["secretEnv"])
	if err != nil {
		return workerProcessRecord{}, nil, permanentWorkerError("%v", err)
	}
	for key, value := range environment {
		environment[key] = mapLogicalWorkspace(value, logicalRoot, runtimeWorkspace)
	}
	for key, value := range secretEnv {
		secretEnv[key] = mapLogicalWorkspace(value, logicalRoot, runtimeWorkspace)
	}
	secretEnvPath := filepath.Join(runtimeWorkspace, ".monkeys", "run", "secret-env.json")
	secretEnvData, err := json.Marshal(secretEnv)
	if err != nil {
		return workerProcessRecord{}, nil, err
	}
	secretEnvSum := sha256.Sum256(secretEnvData)
	secretRefs, secretWrites, err := parseWorkerSecretFiles(runtimeWorkspace, payload["secretFiles"])
	if err != nil {
		return workerProcessRecord{}, nil, err
	}
	servicePort := optionalPort(payload["servicePort"])
	record := workerProcessRecord{
		ProcessID: processID, RuntimeID: runtimeID, Command: command, WorkingDir: workingDir,
		WorkspacePath: runtimeWorkspace, Environment: environment, SecretEnvPath: secretEnvPath, SecretEnvSHA256: hex.EncodeToString(secretEnvSum[:]),
		SecretFiles: secretRefs, ServicePort: servicePort, HealthPath: strings.TrimSpace(workerString(payload["healthPath"])),
		Status: "created", UpdatedAt: time.Now().UnixMilli(),
	}
	hashInput := record
	hashInput.Status = ""
	hashInput.UpdatedAt = 0
	hash, _ := json.Marshal(hashInput)
	sum := sha256.Sum256(hash)
	record.DefinitionHash = hex.EncodeToString(sum[:])
	secretWrites = append(secretWrites, workerSecretWrite{path: secretEnvPath, content: secretEnvData})
	return record, secretWrites, nil
}

func (m *workerProcessManager) start(runtimeID string, payload map[string]any) (map[string]any, error) {
	processID, err := processID(runtimeID, payload)
	if err != nil {
		return nil, err
	}
	state, err := m.store.load()
	if err != nil {
		return nil, err
	}
	record, exists := state.Processes[processID]
	if !exists {
		return nil, permanentWorkerError("process %s does not exist", processID)
	}
	if processRecordRunning(record) {
		return m.inspect(runtimeID, map[string]any{"processId": processID})
	}
	if err := os.MkdirAll(record.WorkingDir, 0o700); err != nil {
		return nil, err
	}
	logDirectory := filepath.Join(m.paths.root, ".monkeys", "logs")
	if err := os.MkdirAll(logDirectory, 0o700); err != nil {
		return nil, err
	}
	stdout, err := openCappedWorkerLog(filepath.Join(logDirectory, processID+".stdout.log"), workerProcessLogLimit)
	if err != nil {
		return nil, err
	}
	stderr, err := openCappedWorkerLog(filepath.Join(logDirectory, processID+".stderr.log"), workerProcessLogLimit)
	if err != nil {
		_ = stdout.Close()
		return nil, err
	}
	command := exec.Command(record.Command[0], record.Command[1:]...)
	command.Dir = record.WorkingDir
	command.Stdin = nil
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = append(os.Environ(), environmentPairs(record.Environment)...)
	secretEnvironment, err := readWorkerSecretEnvironment(record)
	if err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}
	command.Env = append(command.Env, environmentPairs(secretEnvironment)...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, retryableWorkerError("cannot start process %s: %v", processID, err)
	}
	marker, err := pidStartMarker(command.Process.Pid)
	if err != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		_ = command.Wait()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, retryableWorkerError("cannot identify process %s: %v", processID, err)
	}
	now := time.Now().UnixMilli()
	if err := m.store.update(func(state *workerState) error {
		current, ok := state.Processes[processID]
		if !ok {
			return permanentWorkerError("process %s disappeared", processID)
		}
		current.PID = command.Process.Pid
		current.PIDStartMarker = marker
		current.Status = "running"
		current.LastExitCode = nil
		current.StartedAt = now
		current.StoppedAt = 0
		current.UpdatedAt = now
		state.Processes[processID] = current
		return nil
	}); err != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		_ = command.Wait()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}
	exited := make(chan workerProcessExit, 1)
	go m.waitForProcess(processID, command, marker, now, stdout, stderr, exited)
	timer := time.NewTimer(workerStartupGrace)
	defer timer.Stop()
	select {
	case exit := <-exited:
		result, inspectErr := m.inspect(runtimeID, map[string]any{"processId": processID})
		if inspectErr != nil {
			return nil, inspectErr
		}
		if exit.code != 0 {
			result["startupFailed"] = true
			diagnostic := m.workerStartupDiagnostic(record, filepath.Join(logDirectory, processID+".stderr.log"), stderr.startOffset)
			return result, permanentWorkerError("process %s exited during startup with code %d%s", processID, exit.code, diagnostic)
		}
		return result, nil
	case <-timer.C:
		return m.inspect(runtimeID, map[string]any{"processId": processID})
	}
}

type workerProcessExit struct {
	code int
}

func (m *workerProcessManager) waitForProcess(processID string, command *exec.Cmd, marker string, startedAt int64, stdout, stderr io.Closer, exited chan<- workerProcessExit) {
	waitErr := command.Wait()
	_ = stdout.Close()
	_ = stderr.Close()
	exitCode := 0
	if waitErr != nil {
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			exitCode = exitError.ExitCode()
		} else {
			exitCode = -1
		}
	}
	now := time.Now().UnixMilli()
	_ = m.store.update(func(state *workerState) error {
		current, exists := state.Processes[processID]
		sameRunningProcess := current.PID == command.Process.Pid && current.PIDStartMarker == marker
		stoppedBeforeWait := current.PID == 0 && current.PIDStartMarker == "" && current.Status == "stopped" && current.StartedAt == startedAt && current.LastExitCode == nil
		if !exists || (!sameRunningProcess && !stoppedBeforeWait) {
			return nil
		}
		current.PID = 0
		current.PIDStartMarker = ""
		current.Status = "stopped"
		current.LastExitCode = &exitCode
		current.StoppedAt = now
		current.UpdatedAt = now
		state.Processes[processID] = current
		return nil
	})
	exited <- workerProcessExit{code: exitCode}
}

func (m *workerProcessManager) workerStartupDiagnostic(record workerProcessRecord, stderrPath string, startOffset int64) string {
	data, err := readFileTailSince(stderrPath, startOffset, workerStartupTailSize)
	if err != nil {
		return ""
	}
	diagnostic := strings.TrimSpace(string(data))
	if diagnostic == "" {
		return ""
	}
	secrets, err := readWorkerSecretEnvironment(record)
	if err == nil {
		for _, secret := range secrets {
			if secret != "" {
				diagnostic = strings.ReplaceAll(diagnostic, secret, "[REDACTED]")
			}
		}
	}
	for _, secretFile := range record.SecretFiles {
		secret, err := os.ReadFile(secretFile.Path)
		if err == nil && len(secret) > 0 && len(secret) <= 64*1024 {
			diagnostic = strings.ReplaceAll(diagnostic, string(secret), "[REDACTED]")
		}
	}
	diagnostic = strings.ReplaceAll(diagnostic, record.WorkspacePath, "/workspace")
	return ": " + diagnostic
}

func readFileTailSince(path string, startOffset int64, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	start := info.Size() - limit
	if start < startOffset {
		start = startOffset
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(file, limit))
}

func (m *workerProcessManager) stop(runtimeID string, payload map[string]any) (map[string]any, error) {
	processID, err := processID(runtimeID, payload)
	if err != nil {
		return nil, err
	}
	state, err := m.store.load()
	if err != nil {
		return nil, err
	}
	record, exists := state.Processes[processID]
	if !exists {
		return map[string]any{"processId": processID, "status": "absent", "exists": false, "running": false, "changed": false}, nil
	}
	wasRunning := processRecordRunning(record)
	if wasRunning {
		_ = syscall.Kill(-record.PID, syscall.SIGTERM)
		deadline := time.Now().Add(time.Duration(workerInt(payload["timeoutSeconds"], 15, 1, 300)) * time.Second)
		for processRecordRunning(record) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if processRecordRunning(record) {
			_ = syscall.Kill(-record.PID, syscall.SIGKILL)
			killDeadline := time.Now().Add(5 * time.Second)
			for processRecordRunning(record) && time.Now().Before(killDeadline) {
				time.Sleep(25 * time.Millisecond)
			}
		}
	}
	now := time.Now().UnixMilli()
	if err := m.store.update(func(state *workerState) error {
		current, ok := state.Processes[processID]
		if ok {
			current.PID = 0
			current.PIDStartMarker = ""
			current.Status = "stopped"
			current.StoppedAt = now
			current.UpdatedAt = now
			state.Processes[processID] = current
		}
		return nil
	}); err != nil {
		return nil, err
	}
	result, err := m.inspect(runtimeID, map[string]any{"processId": processID})
	result["changed"] = wasRunning
	return result, err
}

func (m *workerProcessManager) delete(runtimeID string, payload map[string]any) (map[string]any, error) {
	processID, err := processID(runtimeID, payload)
	if err != nil {
		return nil, err
	}
	if _, err := m.stop(runtimeID, payload); err != nil {
		return nil, err
	}
	var existing workerProcessRecord
	var removed bool
	if err := m.store.update(func(state *workerState) error {
		existing, removed = state.Processes[processID]
		delete(state.Processes, processID)
		return nil
	}); err != nil {
		return nil, err
	}
	removeWorkerSecrets(existing.SecretFiles, nil)
	if existing.SecretEnvPath != "" {
		_ = os.Remove(existing.SecretEnvPath)
	}
	return map[string]any{"processId": processID, "status": "absent", "exists": false, "running": false, "deleted": removed}, nil
}

func (m *workerProcessManager) inspect(runtimeID string, payload map[string]any) (map[string]any, error) {
	processID, err := processID(runtimeID, payload)
	if err != nil {
		return nil, err
	}
	state, err := m.store.load()
	if err != nil {
		return nil, err
	}
	record, exists := state.Processes[processID]
	if !exists {
		return map[string]any{"processId": processID, "status": "absent", "exists": false, "running": false, "ready": false, "message": fmt.Sprintf("Process %s does not exist.", processID)}, nil
	}
	running := processRecordRunning(record)
	status := record.Status
	if running {
		status = "running"
	} else if record.PID != 0 {
		status = "stopped"
		_ = m.store.update(func(state *workerState) error {
			current := state.Processes[processID]
			current.PID = 0
			current.PIDStartMarker = ""
			current.Status = "stopped"
			current.StoppedAt = time.Now().UnixMilli()
			current.UpdatedAt = current.StoppedAt
			state.Processes[processID] = current
			return nil
		})
	}
	ready := running && processReady(record)
	result := map[string]any{
		"processId": processID, "runtimeId": record.RuntimeID, "status": status,
		"exists": true, "running": running, "ready": ready, "pid": nullablePID(record.PID, running),
		"lastExitCode": record.LastExitCode,
		"servicePort":  record.ServicePort, "startedAt": record.StartedAt, "stoppedAt": record.StoppedAt,
		"workingDir": record.WorkingDir, "workspacePath": record.WorkspacePath,
		"message": fmt.Sprintf("Process %s is %s.", processID, status),
	}
	result["metrics"] = collectWorkerInspectMetrics(record, running)
	return result, nil
}

func (m *workerProcessManager) exec(ctx context.Context, runtimeID string, payload map[string]any) (map[string]any, error) {
	if safeIdentifier(runtimeID) == "" {
		runtimeID = strings.TrimSpace(workerString(payload["runtimeId"]))
	}
	runtimeWorkspace, err := m.paths.runtimeWorkspace(runtimeID, payload["workspacePath"])
	if err != nil {
		return nil, permanentWorkerError("%v", err)
	}
	if err := os.MkdirAll(runtimeWorkspace, 0o700); err != nil {
		return nil, err
	}
	logicalRoot := strings.TrimSpace(workerString(payload["logicalWorkspacePath"]))
	if logicalRoot == "" {
		logicalRoot = "/workspace"
	}
	command, err := workerStringSlice(payload["command"])
	if err != nil || len(command) == 0 {
		return nil, permanentWorkerError("command is required")
	}
	for index := range command {
		command[index] = mapLogicalWorkspace(command[index], logicalRoot, runtimeWorkspace)
	}
	workingDir, err := resolveRuntimePath(runtimeWorkspace, logicalRoot, workerFirstNonEmpty(workerString(payload["workingDir"]), logicalRoot))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(workingDir, 0o700); err != nil {
		return nil, err
	}
	timeout := time.Duration(workerInt(payload["timeoutSeconds"], 60, 1, 3600)) * time.Second
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, command[0], command[1:]...)
	cmd.Dir = workingDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	environment, err := workerStringMap(payload["env"])
	if err != nil {
		return nil, permanentWorkerError("%v", err)
	}
	for key, value := range environment {
		environment[key] = mapLogicalWorkspace(value, logicalRoot, runtimeWorkspace)
	}
	cmd.Env = append(os.Environ(), environmentPairs(environment)...)
	limit := workerInt(payload["outputLimitBytes"], 1024*1024, 1024, workerOutputLimit)
	stdout := &boundedBuffer{limit: limit}
	stderr := &boundedBuffer{limit: limit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, retryableWorkerError("cannot execute command: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-commandCtx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		return nil, retryableWorkerError("command timed out or was cancelled")
	}
	exitCode := 0
	if waitErr != nil {
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			exitCode = exitError.ExitCode()
		} else {
			return nil, retryableWorkerError("command failed: %v", waitErr)
		}
	}
	return map[string]any{
		"status": statusForExit(exitCode), "exitCode": exitCode,
		"stdout":          strings.ReplaceAll(stdout.String(), runtimeWorkspace, logicalRoot),
		"stderr":          strings.ReplaceAll(stderr.String(), runtimeWorkspace, logicalRoot),
		"stdoutTruncated": stdout.truncated, "stderrTruncated": stderr.truncated,
	}, nil
}

type workerSecretWrite struct {
	path    string
	content []byte
}

func parseWorkerSecretFiles(runtimeWorkspace string, value any) ([]workerSecretRef, []workerSecretWrite, error) {
	if value == nil {
		return nil, nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, nil, permanentWorkerError("secretFiles must be an array")
	}
	if len(items) > 100 {
		return nil, nil, permanentWorkerError("secretFiles must contain at most 100 entries")
	}
	refs := make([]workerSecretRef, 0, len(items))
	writes := make([]workerSecretWrite, 0, len(items))
	seen := map[string]bool{}
	for _, raw := range items {
		item, ok := workerMap(raw)
		if !ok {
			return nil, nil, permanentWorkerError("secretFiles entries must be objects")
		}
		relative := strings.TrimSpace(workerString(item["path"]))
		if relative == "" || filepath.IsAbs(relative) {
			return nil, nil, permanentWorkerError("secret file path must be relative")
		}
		path := filepath.Clean(filepath.Join(runtimeWorkspace, relative))
		path, pathErr := confinedWorkerPath(runtimeWorkspace, path)
		if pathErr != nil {
			return nil, nil, permanentWorkerError("secret file path must stay inside runtime workspace")
		}
		if seen[path] {
			return nil, nil, permanentWorkerError("duplicate secret file path")
		}
		seen[path] = true
		content, ok := item["content"].(string)
		if !ok || len(content) > 8*1024*1024 {
			return nil, nil, permanentWorkerError("secret file content is invalid or exceeds 8 MiB")
		}
		sum := sha256.Sum256([]byte(content))
		refs = append(refs, workerSecretRef{Path: path, SHA256: hex.EncodeToString(sum[:])})
		writes = append(writes, workerSecretWrite{path: path, content: []byte(content)})
	}
	return refs, writes, nil
}

func writeWorkerSecrets(writes []workerSecretWrite) error {
	for _, write := range writes {
		directory := filepath.Dir(write.path)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return err
		}
		temporary, err := os.CreateTemp(directory, ".secret-*")
		if err != nil {
			return err
		}
		temporaryPath := temporary.Name()
		if err := temporary.Chmod(0o600); err != nil {
			temporary.Close()
			os.Remove(temporaryPath)
			return err
		}
		if _, err := temporary.Write(write.content); err != nil {
			temporary.Close()
			os.Remove(temporaryPath)
			return err
		}
		if err := temporary.Sync(); err != nil {
			temporary.Close()
			os.Remove(temporaryPath)
			return err
		}
		if err := temporary.Close(); err != nil {
			os.Remove(temporaryPath)
			return err
		}
		if err := os.Rename(temporaryPath, write.path); err != nil {
			os.Remove(temporaryPath)
			return err
		}
	}
	return nil
}

func removeWorkerSecrets(existing []workerSecretRef, keep []workerSecretRef) {
	kept := map[string]bool{}
	for _, item := range keep {
		kept[item.Path] = true
	}
	for _, item := range existing {
		if !kept[item.Path] {
			_ = os.Remove(item.Path)
		}
	}
}

func readWorkerSecretEnvironment(record workerProcessRecord) (map[string]string, error) {
	if strings.TrimSpace(record.SecretEnvPath) == "" {
		return map[string]string{}, nil
	}
	data, err := os.ReadFile(record.SecretEnvPath)
	if err != nil {
		return nil, retryableWorkerError("read process secret environment: %v", err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != record.SecretEnvSHA256 {
		return nil, permanentWorkerError("process secret environment integrity check failed")
	}
	result := map[string]string{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, permanentWorkerError("decode process secret environment: %v", err)
	}
	return result, nil
}

func resolveRuntimePath(runtimeWorkspace string, logicalRoot string, value string) (string, error) {
	mapped := mapLogicalWorkspace(value, logicalRoot, runtimeWorkspace)
	if !filepath.IsAbs(mapped) {
		mapped = filepath.Join(runtimeWorkspace, mapped)
	}
	mapped = filepath.Clean(mapped)
	resolved, err := confinedWorkerPath(runtimeWorkspace, mapped)
	if err != nil {
		return "", permanentWorkerError("working directory must stay inside runtime workspace")
	}
	return resolved, nil
}

func processID(runtimeID string, payload map[string]any) (string, error) {
	selected := workerFirstNonEmpty(workerString(payload["processId"]), workerString(payload["id"]), runtimeID, workerString(payload["runtimeId"]))
	if safeIdentifier(selected) == "" {
		return "", permanentWorkerError("a valid processId is required")
	}
	if runtimeID != "" && selected != runtimeID {
		return "", permanentWorkerError("processId must match task runtimeId")
	}
	return selected, nil
}

func processRecordRunning(record workerProcessRecord) bool {
	if record.PID <= 1 || record.PIDStartMarker == "" {
		return false
	}
	marker, err := pidStartMarker(record.PID)
	return err == nil && marker == record.PIDStartMarker
}

func pidStartMarker(pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", err
	}
	closing := bytes.LastIndexByte(data, ')')
	if closing < 0 {
		return "", fmt.Errorf("invalid proc stat")
	}
	fields := strings.Fields(string(data[closing+1:]))
	if len(fields) <= 19 {
		return "", fmt.Errorf("invalid proc stat")
	}
	return fields[19], nil
}

func processReady(record workerProcessRecord) bool {
	if record.ServicePort == 0 {
		return true
	}
	connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(record.ServicePort)), time.Second)
	if err != nil {
		return false
	}
	connection.Close()
	return true
}

func optionalPort(value any) int {
	if value == nil || workerString(value) == "" {
		return 0
	}
	return workerInt(value, 0, 0, 65535)
}

func nullablePID(pid int, running bool) any {
	if running {
		return pid
	}
	return nil
}
func statusForExit(code int) string {
	if code == 0 {
		return "completed"
	}
	return "failed"
}
func environmentPairs(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}
func workerFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

type cappedWorkerLog struct {
	file        *os.File
	remaining   int64
	startOffset int64
	mu          sync.Mutex
}

func openCappedWorkerLog(path string, limit int64) (*cappedWorkerLog, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("worker log limit must be positive")
	}
	info, err := os.Stat(path)
	if err == nil && info.Size() >= limit {
		backup := path + ".1"
		if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err := os.Rename(path, backup); err != nil {
			return nil, err
		}
		if err := os.Truncate(backup, limit); err != nil {
			return nil, err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	info, err = file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	return &cappedWorkerLog{file: file, remaining: limit - info.Size(), startOffset: info.Size()}, nil
}

func (w *cappedWorkerLog) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	count := len(value)
	if w.remaining <= 0 {
		return count, nil
	}
	writable := int64(count)
	if writable > w.remaining {
		writable = w.remaining
	}
	written, err := w.file.Write(value[:writable])
	w.remaining -= int64(written)
	if err != nil {
		return written, err
	}
	if int64(written) != writable {
		return written, io.ErrShortWrite
	}
	return count, nil
}

func (w *cappedWorkerLog) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	count := len(value)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if remaining > count {
			remaining = count
		}
		_, _ = b.buffer.Write(value[:remaining])
	}
	if count > remaining {
		b.truncated = true
	}
	return count, nil
}
func (b *boundedBuffer) String() string {
	value, _ := io.ReadAll(bytes.NewReader(b.buffer.Bytes()))
	return string(value)
}
