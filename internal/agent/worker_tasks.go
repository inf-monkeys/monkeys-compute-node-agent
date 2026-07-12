package agent

import (
	"context"
	"fmt"
	"strings"
)

var workerTaskAllowlist = map[string]bool{
	"process.create": true, "process.start": true, "process.stop": true, "process.restart": true, "process.delete": true, "process.inspect": true, "process.exec": true,
	"filesystem.list": true, "filesystem.stat": true, "filesystem.read": true, "filesystem.write": true, "filesystem.mkdir": true, "filesystem.rename": true, "filesystem.delete": true,
	"http.request": true,
}

func executeWorkerTask(ctx context.Context, cfg Config, task ClaimedTask) (map[string]any, error) {
	if normalizedMode(cfg.Mode) != "worker" {
		return nil, permanentWorkerError("Worker task received in %s mode", normalizedMode(cfg.Mode))
	}
	if !workerTaskAllowlist[task.TaskType] {
		return nil, permanentWorkerError("%s is not allowed in worker mode", task.TaskType)
	}
	runtimeID := strings.TrimSpace(pointerString(task.RuntimeID))
	if runtimeID == "" {
		runtimeID = strings.TrimSpace(workerString(task.Payload["runtimeId"]))
	}
	if safeIdentifier(runtimeID) == "" {
		return nil, permanentWorkerError("task requires a valid runtimeId")
	}
	workspace := strings.TrimSpace(cfg.Workspace)
	if workspace == "" {
		workspace = "."
	}
	processes, err := newWorkerProcessManager(workspace)
	if err != nil {
		return nil, fmt.Errorf("initialize Worker process manager: %w", err)
	}
	if strings.HasPrefix(task.TaskType, "process.") {
		return processes.execute(ctx, task.TaskType, runtimeID, task.Payload)
	}
	if strings.HasPrefix(task.TaskType, "filesystem.") {
		filesystem, err := newWorkerFilesystem(workspace)
		if err != nil {
			return nil, err
		}
		return filesystem.execute(task.TaskType, runtimeID, task.Payload)
	}
	if task.TaskType == "http.request" {
		return executeWorkerHTTPRequest(ctx, processes, runtimeID, task.Payload)
	}
	return nil, permanentWorkerError("unsupported Worker task")
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
