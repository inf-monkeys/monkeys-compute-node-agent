package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type workerState struct {
	Processes map[string]workerProcessRecord `json:"processes"`
}

type workerProcessRecord struct {
	ProcessID       string            `json:"processId"`
	RuntimeID       string            `json:"runtimeId"`
	Command         []string          `json:"command"`
	WorkingDir      string            `json:"workingDir"`
	WorkspacePath   string            `json:"workspacePath"`
	Environment     map[string]string `json:"environment,omitempty"`
	SecretEnv       map[string]string `json:"-"`
	SecretEnvPath   string            `json:"secretEnvPath,omitempty"`
	SecretEnvSHA256 string            `json:"secretEnvSha256,omitempty"`
	SecretFiles     []workerSecretRef `json:"secretFiles,omitempty"`
	ServicePort     int               `json:"servicePort,omitempty"`
	HealthPath      string            `json:"healthPath,omitempty"`
	DefinitionHash  string            `json:"definitionHash"`
	PID             int               `json:"pid,omitempty"`
	PIDStartMarker  string            `json:"pidStartMarker,omitempty"`
	Status          string            `json:"status"`
	LastExitCode    *int              `json:"lastExitCode,omitempty"`
	StartedAt       int64             `json:"startedAt,omitempty"`
	StoppedAt       int64             `json:"stoppedAt,omitempty"`
	UpdatedAt       int64             `json:"updatedAt"`
}

type workerSecretRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type workerStateStore struct {
	path string
	mu   sync.Mutex
}

func newWorkerStateStore(workspace string) *workerStateStore {
	return &workerStateStore{path: filepath.Join(workspace, ".monkeys", "worker-state.json")}
}

func (s *workerStateStore) load() (workerState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUnlocked()
}

func (s *workerStateStore) update(update func(*workerState) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	if err := update(&state); err != nil {
		return err
	}
	return writeJSONAtomic(s.path, state)
}

func (s *workerStateStore) loadUnlocked() (workerState, error) {
	state := workerState{Processes: map[string]workerProcessRecord{}}
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("decode worker state: %w", err)
	}
	if state.Processes == nil {
		state.Processes = map[string]workerProcessRecord{}
	}
	return state, nil
}

func writeJSONAtomic(path string, value any) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".worker-state-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	return directoryHandle.Sync()
}
