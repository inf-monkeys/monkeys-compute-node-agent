package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientDecodesWrappedEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer agent-token" {
			t.Fatalf("missing bearer token: %s", request.Header.Get("Authorization"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":0,"msg":"ok","data":{"id":"plan-1","updatedAt":"1825000000000"}}`))
	}))
	defer server.Close()

	client, err := newClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.plan(t.Context(), "agent-token")
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "plan-1" {
		t.Fatalf("unexpected plan: %+v", result)
	}
	if result.UpdatedAt.Int64() != 1825000000000 {
		t.Fatalf("unexpected updatedAt: %d", result.UpdatedAt.Int64())
	}
}

func TestClientSendsStableTaskExecutionIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("x-request-id") != "compute-task:task-1:complete" {
			t.Fatalf("unexpected request identity: %s", request.Header.Get("x-request-id"))
		}
		var link struct {
			Contract  string `json:"contract"`
			RequestID string `json:"requestId"`
			RunRef    struct {
				ID string `json:"id"`
			} `json:"runRef"`
			TaskRef struct {
				ID string `json:"id"`
			} `json:"taskRef"`
		}
		if err := json.Unmarshal([]byte(request.Header.Get("x-monkeys-execution-link")), &link); err != nil {
			t.Fatal(err)
		}
		if link.Contract != "ExecutionLink" || link.RunRef.ID != "task-1" || link.TaskRef.ID != "task-1" {
			t.Fatalf("unexpected execution link: %+v", link)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer server.Close()

	client, err := newClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.completeTask(t.Context(), "agent-token", "task-1", CompleteTaskRequest{}); err != nil {
		t.Fatal(err)
	}
}
