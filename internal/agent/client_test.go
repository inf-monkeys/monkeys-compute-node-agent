package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterUsesBootstrapTokenAndStoresAgentToken(t *testing.T) {
	var registerPayload RegisterRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/compute/node-agent/register" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&registerPayload); err != nil {
			t.Fatalf("decode register payload: %v", err)
		}
		writeEnvelope(w, RegisterResult{
			Node: NodeInfo{
				ID:   "node-1",
				Name: "gpu-01",
			},
			AgentToken: "mnag_test",
			Plan: Plan{
				ID:     "bootstrap-node-1",
				NodeID: "node-1",
				Status: "pending",
			},
		})
	}))
	defer server.Close()

	statePath := filepath.Join(t.TempDir(), "state.json")
	result, err := Register(t.Context(), Config{
		ServerURL:      server.URL,
		BootstrapToken: "mnbt_test",
		StatePath:      statePath,
		NodeName:       "gpu-01",
		Version:        "test",
	})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if registerPayload.BootstrapToken != "mnbt_test" {
		t.Fatalf("unexpected bootstrap token: %s", registerPayload.BootstrapToken)
	}
	if result.AgentToken != "mnag_test" {
		t.Fatalf("unexpected agent token: %s", result.AgentToken)
	}
	state, err := loadState(statePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.AgentToken != "mnag_test" || state.NodeID != "node-1" {
		t.Fatalf("unexpected state: %+v", state)
	}
}

func TestOnceSendsHeartbeatAndCompletesPlan(t *testing.T) {
	paths := []string{}
	authHeaders := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/compute/node-agent/heartbeat":
			writeEnvelope(w, HeartbeatResult{
				Node: NodeInfo{ID: "node-1", Name: "gpu-01"},
				Plan: Plan{
					ID:     "plan-1",
					NodeID: "node-1",
					Status: "pending",
					Actions: []PlanAction{
						{Type: "inspect"},
					},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/compute/node-agent/plan/plan-1/events":
			writeEnvelope(w, map[string]any{"ok": true})
		case r.Method == http.MethodPost && r.URL.Path == "/api/compute/node-agent/plan/plan-1/complete":
			writeEnvelope(w, map[string]any{"ok": true})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := saveState(statePath, State{
		ServerURL:  server.URL,
		NodeID:     "node-1",
		NodeName:   "gpu-01",
		AgentToken: "mnag_test",
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if err := Once(t.Context(), Config{
		StatePath: statePath,
		Version:   "test",
	}); err != nil {
		t.Fatalf("Once failed: %v", err)
	}
	joinedPaths := strings.Join(paths, "\n")
	for _, want := range []string{
		"/api/compute/node-agent/heartbeat",
		"/api/compute/node-agent/plan/plan-1/events",
		"/api/compute/node-agent/plan/plan-1/complete",
	} {
		if !strings.Contains(joinedPaths, want) {
			t.Fatalf("missing path %s in:\n%s", want, joinedPaths)
		}
	}
	for _, header := range authHeaders {
		if header != "Bearer mnag_test" {
			t.Fatalf("unexpected auth header: %q", header)
		}
	}
}

func writeEnvelope(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": 0,
		"msg":  "success",
		"data": data,
	})
}
