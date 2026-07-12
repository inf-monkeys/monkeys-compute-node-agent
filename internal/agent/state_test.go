package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	want := State{
		ServerURL:  "https://compute.example.com",
		NodeID:     "node-1",
		NodeName:   "gpu-01",
		AgentToken: "mnag_xxx",
	}
	if err := saveState(path, want); err != nil {
		t.Fatalf("saveState failed: %v", err)
	}
	got, err := loadState(path)
	if err != nil {
		t.Fatalf("loadState failed: %v", err)
	}
	if got.ServerURL != want.ServerURL || got.NodeID != want.NodeID || got.AgentToken != want.AgentToken {
		t.Fatalf("unexpected state: %+v", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file not written: %v", err)
	}
}

func TestLoadOrCreateAgentInstanceIDIsStable(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "agent-state.json")
	first, err := loadOrCreateAgentInstanceID(statePath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateAgentInstanceID(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatalf("agent instance identity is not stable: first=%q second=%q", first, second)
	}
	info, err := os.Stat(statePath + ".instance-id")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected instance identity mode: %o", info.Mode().Perm())
	}
}
