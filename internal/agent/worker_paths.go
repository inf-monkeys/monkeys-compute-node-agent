package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type workerPaths struct {
	root string
}

func newWorkerPaths(workspace string) (workerPaths, error) {
	value := strings.TrimSpace(workspace)
	if value == "" {
		value = "."
	}
	root, err := filepath.Abs(value)
	if err != nil {
		return workerPaths{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return workerPaths{}, err
		}
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		return workerPaths{}, err
	}
	return workerPaths{root: filepath.Clean(root)}, nil
}

func (p workerPaths) resolve(value string) (string, error) {
	selected := strings.TrimSpace(value)
	if selected == "" || selected == "." {
		return p.root, nil
	}
	if filepath.IsAbs(selected) {
		return "", fmt.Errorf("absolute paths are not accepted inside Worker workspace")
	}
	candidate := filepath.Clean(filepath.Join(p.root, selected))
	return confinedWorkerPath(p.root, candidate)
}

func (p workerPaths) runtimeWorkspace(runtimeID string, requested any) (string, error) {
	relative := filepath.Join(".monkeys", "runtimes", safeIdentifier(runtimeID))
	if text := strings.TrimSpace(workerString(requested)); text != "" {
		if filepath.IsAbs(text) {
			return "", fmt.Errorf("workspacePath must be relative to the Agent workspace")
		}
		clean := filepath.Clean(text)
		if clean != relative {
			return "", fmt.Errorf("workspacePath must equal %s", filepath.ToSlash(relative))
		}
	}
	return p.resolve(relative)
}

func mapLogicalWorkspace(value string, logicalRoot string, actualRoot string) string {
	logical := strings.TrimRight(strings.TrimSpace(logicalRoot), "/")
	if logical == "" || !strings.HasPrefix(logical, "/") {
		return value
	}
	var result strings.Builder
	for index := 0; index < len(value); {
		offset := strings.Index(value[index:], logical)
		if offset < 0 {
			result.WriteString(value[index:])
			break
		}
		offset += index
		result.WriteString(value[index:offset])
		end := offset + len(logical)
		validBefore := offset == 0 || !isPathWord(value[offset-1])
		validAfter := end == len(value) || value[end] == '/' || !isPathWord(value[end])
		if validBefore && validAfter {
			result.WriteString(actualRoot)
		} else {
			result.WriteString(logical)
		}
		index = end
	}
	return result.String()
}

func pathWithin(candidate string, root string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func safeIdentifier(value string) string {
	selected := strings.TrimSpace(value)
	if selected == "" || selected == "." || selected == ".." || len(selected) > 255 {
		return ""
	}
	for _, character := range selected {
		if character != '-' && character != '_' && character != '.' && (character < '0' || character > '9') && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') {
			return ""
		}
	}
	return selected
}

func confinedWorkerPath(root string, candidate string) (string, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(candidate)
	existing := clean
	suffix := []string{}
	for {
		_, statErr := os.Lstat(existing)
		if statErr == nil {
			break
		}
		if !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", fmt.Errorf("path has no existing ancestor")
		}
		suffix = append(suffix, filepath.Base(existing))
		existing = parent
	}
	resolvedExisting, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	resolved := resolvedExisting
	for index := len(suffix) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, suffix[index])
	}
	resolved = filepath.Clean(resolved)
	if !pathWithin(resolved, resolvedRoot) {
		return "", fmt.Errorf("path must stay inside Worker workspace")
	}
	return resolved, nil
}

func isPathWord(value byte) bool {
	return value == '_' || value == '-' || value == '.' || value >= '0' && value <= '9' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}
