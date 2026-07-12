package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestKubernetesClientUsesServiceAccountTLSAndPagination(t *testing.T) {
	var mutex sync.Mutex
	queries := make([]map[string]string, 0)
	client, _ := newTestKubernetesClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer service-account-token" {
			t.Errorf("unexpected authorization header %q", request.Header.Get("Authorization"))
		}
		mutex.Lock()
		queries = append(queries, map[string]string{
			"limit":         request.URL.Query().Get("limit"),
			"continue":      request.URL.Query().Get("continue"),
			"labelSelector": request.URL.Query().Get("labelSelector"),
		})
		mutex.Unlock()
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("continue") == "" {
			_, _ = response.Write([]byte(`{"items":[{"metadata":{"name":"one"}}],"metadata":{"continue":"next-page"}}`))
			return
		}
		_, _ = response.Write([]byte(`{"items":[{"metadata":{"name":"two"}}],"metadata":{}}`))
	}))

	result, err := client.list(context.Background(), "/api/v1/pods", monkeysRuntimeLabel)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || stringValue(objectValue(result.Items[1]["metadata"])["name"]) != "two" {
		t.Fatalf("unexpected items: %#v", result.Items)
	}
	if len(queries) != 2 {
		t.Fatalf("expected two pages, got %d", len(queries))
	}
	if queries[0]["limit"] != "500" || queries[0]["labelSelector"] != monkeysRuntimeLabel {
		t.Fatalf("unexpected first query: %#v", queries[0])
	}
	if queries[1]["continue"] != "next-page" {
		t.Fatalf("unexpected continuation: %#v", queries[1])
	}
}

func TestKubernetesClientCapsListsAtTwoThousandItems(t *testing.T) {
	page := make([]map[string]any, kubernetesListPageSize)
	for index := range page {
		page[index] = map[string]any{"metadata": map[string]any{"name": fmt.Sprintf("pod-%d", index)}}
	}
	pageCount := 0
	client, _ := newTestKubernetesClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		pageCount++
		_ = json.NewEncoder(response).Encode(map[string]any{
			"items":    page,
			"metadata": map[string]any{"continue": fmt.Sprintf("page-%d", pageCount)},
		})
	}))

	result, err := client.list(context.Background(), "/api/v1/pods", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != kubernetesMaxListItems || !result.Truncated {
		t.Fatalf("expected bounded truncated list, got %d truncated=%v", len(result.Items), result.Truncated)
	}
	if pageCount != kubernetesMaxListItems/kubernetesListPageSize {
		t.Fatalf("expected four requests, got %d", pageCount)
	}
}

func TestKubernetesClientRejectsRepeatedContinuationAndLargeResponses(t *testing.T) {
	t.Run("continuation", func(t *testing.T) {
		client, _ := newTestKubernetesClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			_, _ = response.Write([]byte(`{"items":[],"metadata":{"continue":"same"}}`))
		}))
		_, err := client.list(context.Background(), "/api/v1/pods", "")
		if err == nil || !strings.Contains(err.Error(), "repeated a continue token") {
			t.Fatalf("expected repeated-token error, got %v", err)
		}
	})

	t.Run("response size", func(t *testing.T) {
		client, _ := newTestKubernetesClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			response.Header().Set("Content-Length", fmt.Sprint(kubernetesMaxResponseBytes+1))
			_, _ = response.Write([]byte("{}"))
		}))
		_, err := client.get(context.Background(), "/version")
		if err == nil || !strings.Contains(err.Error(), "exceeds 16 MiB") {
			t.Fatalf("expected response-limit error, got %v", err)
		}
	})
}
