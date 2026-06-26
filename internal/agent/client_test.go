package agent

import (
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
		_, _ = writer.Write([]byte(`{"code":0,"msg":"ok","data":{"id":"plan-1"}}`))
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
}
