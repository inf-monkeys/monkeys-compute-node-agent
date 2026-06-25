package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

func loadState(path string) (State, error) {
	var state State
	data, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.AgentToken == "" {
		return state, errors.New("state file does not contain agent token")
	}
	return state, nil
}

func saveState(path string, state State) error {
	if path == "" {
		path = "agent-state.json"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return err
	}
	state.UpdatedAt = time.Now().UnixMilli()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
