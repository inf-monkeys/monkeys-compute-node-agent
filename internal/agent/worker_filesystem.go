package agent

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type workerFilesystem struct {
	paths workerPaths
}

func newWorkerFilesystem(workspace string) (*workerFilesystem, error) {
	paths, err := newWorkerPaths(workspace)
	if err != nil {
		return nil, err
	}
	return &workerFilesystem{paths: paths}, nil
}

func (f *workerFilesystem) execute(taskType string, runtimeID string, payload map[string]any) (map[string]any, error) {
	root, err := f.runtimeRoot(runtimeID, payload)
	if err != nil {
		return nil, err
	}
	switch taskType {
	case "filesystem.list":
		return f.list(root, payload)
	case "filesystem.stat":
		return f.stat(root, payload)
	case "filesystem.read":
		return f.read(root, payload)
	case "filesystem.write":
		return f.write(root, payload)
	case "filesystem.mkdir":
		return f.mkdir(root, payload)
	case "filesystem.rename":
		return f.rename(root, payload)
	case "filesystem.delete":
		return f.delete(root, payload)
	default:
		return nil, permanentWorkerError("%s is not allowed in worker mode", taskType)
	}
}

func (f *workerFilesystem) runtimeRoot(runtimeID string, payload map[string]any) (string, error) {
	if safeIdentifier(runtimeID) == "" {
		runtimeID = strings.TrimSpace(workerString(payload["runtimeId"]))
	}
	if safeIdentifier(runtimeID) == "" {
		return "", permanentWorkerError("a valid runtimeId is required")
	}
	root, err := f.paths.runtimeWorkspace(runtimeID, payload["workspacePath"])
	if err != nil {
		return "", permanentWorkerError("%v", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return root, nil
}

func filesystemPath(root string, value any) (string, error) {
	selected := strings.TrimSpace(workerString(value))
	if selected == "" {
		selected = "."
	}
	if filepath.IsAbs(selected) {
		logical := "/workspace"
		selected = mapLogicalWorkspace(selected, logical, root)
	}
	if !filepath.IsAbs(selected) {
		selected = filepath.Join(root, selected)
	}
	selected = filepath.Clean(selected)
	resolved, err := confinedWorkerPath(root, selected)
	if err != nil {
		return "", permanentWorkerError("filesystem path must stay inside runtime workspace")
	}
	return resolved, nil
}

func (f *workerFilesystem) list(root string, payload map[string]any) (map[string]any, error) {
	path, err := filesystemPath(root, payload["path"])
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, retryableWorkerError("list filesystem path: %v", err)
	}
	limit := workerInt(payload["limit"], 1000, 1, 5000)
	result := make([]map[string]any, 0, min(len(entries), limit))
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries[:min(len(entries), limit)] {
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		result = append(result, fileInfo(root, filepath.Join(path, entry.Name()), info))
	}
	return map[string]any{"path": relativeWorkerPath(root, path), "entries": result, "truncated": len(entries) > limit}, nil
}

func (f *workerFilesystem) stat(root string, payload map[string]any) (map[string]any, error) {
	path, err := filesystemPath(root, payload["path"])
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, retryableWorkerError("stat filesystem path: %v", err)
	}
	return fileInfo(root, path, info), nil
}

func (f *workerFilesystem) read(root string, payload map[string]any) (map[string]any, error) {
	path, err := filesystemPath(root, payload["path"])
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, retryableWorkerError("read filesystem path: %v", err)
	}
	if !info.Mode().IsRegular() {
		return nil, permanentWorkerError("read target must be a regular file")
	}
	offset := int64(workerInt(payload["offset"], 0, 0, 1<<30))
	limit := workerInt(payload["limitBytes"], 1024*1024, 1, 8*1024*1024)
	file, err := os.Open(path)
	if err != nil {
		return nil, retryableWorkerError("open filesystem path: %v", err)
	}
	defer file.Close()
	if _, err := file.Seek(offset, 0); err != nil {
		return nil, err
	}
	value := make([]byte, limit+1)
	count, readErr := file.Read(value)
	if readErr != nil && readErr.Error() != "EOF" {
		return nil, readErr
	}
	value = value[:count]
	truncated := len(value) > limit
	if truncated {
		value = value[:limit]
	}
	return map[string]any{"path": relativeWorkerPath(root, path), "offset": offset, "contentBase64": base64.StdEncoding.EncodeToString(value), "truncated": truncated}, nil
}

func (f *workerFilesystem) write(root string, payload map[string]any) (map[string]any, error) {
	path, err := filesystemPath(root, payload["path"])
	if err != nil {
		return nil, err
	}
	if info, statErr := os.Lstat(path); statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return nil, permanentWorkerError("write target must be a regular file")
	}
	encoded, ok := payload["contentBase64"].(string)
	if !ok {
		return nil, permanentWorkerError("contentBase64 is required")
	}
	content, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, permanentWorkerError("contentBase64 is invalid")
	}
	if len(content) > 16*1024*1024 {
		return nil, permanentWorkerError("filesystem.write is limited to 16 MiB")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if workerBool(payload["append"]) {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	file, err := os.OpenFile(path, flag, 0o600)
	if err != nil {
		return nil, retryableWorkerError("write filesystem path: %v", err)
	}
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	info, _ := os.Stat(path)
	return map[string]any{"path": relativeWorkerPath(root, path), "size": info.Size()}, nil
}

func (f *workerFilesystem) mkdir(root string, payload map[string]any) (map[string]any, error) {
	path, err := filesystemPath(root, payload["path"])
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, retryableWorkerError("mkdir filesystem path: %v", err)
	}
	return map[string]any{"path": relativeWorkerPath(root, path), "created": true}, nil
}

func (f *workerFilesystem) rename(root string, payload map[string]any) (map[string]any, error) {
	sourceValue := payload["path"]
	if sourceValue == nil {
		sourceValue = payload["source"]
	}
	targetValue := payload["target"]
	if targetValue == nil {
		targetValue = payload["destination"]
	}
	source, err := filesystemPath(root, sourceValue)
	if err != nil {
		return nil, err
	}
	target, err := filesystemPath(root, targetValue)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(source); err != nil {
		return nil, retryableWorkerError("rename source: %v", err)
	}
	if _, err := os.Lstat(target); err == nil && !workerBool(payload["overwrite"]) {
		return nil, permanentWorkerError("rename destination already exists")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return nil, err
	}
	if err := os.Rename(source, target); err != nil {
		return nil, retryableWorkerError("rename filesystem path: %v", err)
	}
	return map[string]any{"path": relativeWorkerPath(root, target), "renamed": true}, nil
}

func (f *workerFilesystem) delete(root string, payload map[string]any) (map[string]any, error) {
	path, err := filesystemPath(root, payload["path"])
	if err != nil {
		return nil, err
	}
	if path == root {
		return nil, permanentWorkerError("runtime workspace root cannot be deleted")
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return map[string]any{"path": relativeWorkerPath(root, path), "deleted": false}, nil
	}
	if err != nil {
		return nil, err
	}
	if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		if workerBool(payload["recursive"]) {
			err = os.RemoveAll(path)
		} else {
			err = os.Remove(path)
		}
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		return nil, retryableWorkerError("delete filesystem path: %v", err)
	}
	return map[string]any{"path": relativeWorkerPath(root, path), "deleted": true}, nil
}

func fileInfo(root, path string, info os.FileInfo) map[string]any {
	kind := "file"
	if info.IsDir() {
		kind = "directory"
	} else if info.Mode()&os.ModeSymlink != 0 {
		kind = "symlink"
	}
	return map[string]any{"name": info.Name(), "path": relativeWorkerPath(root, path), "type": kind, "size": info.Size(), "mode": fmt.Sprintf("%04o", info.Mode().Perm()), "modifiedAt": info.ModTime().UnixMilli()}
}
func relativeWorkerPath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." {
		return "."
	}
	return filepath.ToSlash(relative)
}

var _ = time.Now
