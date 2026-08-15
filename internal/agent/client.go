package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type client struct {
	baseURL    string
	httpClient *http.Client
}

type responseEnvelope[T any] struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data T      `json:"data"`
}

type envelopeProbe struct {
	Data json.RawMessage `json:"data"`
}

type httpResponseError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *httpResponseError) Error() string {
	return fmt.Sprintf("%s %s failed: HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

func newClient(serverURL string) (*client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("server URL is required")
	}
	return &client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

func (c *client) register(ctx context.Context, request RegisterRequest) (RegisterResult, error) {
	return doJSON[RegisterResult](ctx, c, http.MethodPost, "/api/compute/node-agent/register", "", request)
}

func (c *client) heartbeat(ctx context.Context, token string, request HeartbeatRequest) (HeartbeatResult, error) {
	return doJSON[HeartbeatResult](ctx, c, http.MethodPost, "/api/compute/node-agent/heartbeat", token, request)
}

func (c *client) plan(ctx context.Context, token string) (Plan, error) {
	return doJSON[Plan](ctx, c, http.MethodGet, "/api/compute/node-agent/plan", token, nil)
}

func (c *client) event(ctx context.Context, token string, planID string, request EventRequest) error {
	_, err := doJSON[map[string]any](ctx, c, http.MethodPost, "/api/compute/node-agent/plan/"+planID+"/events", token, request)
	return err
}

func (c *client) complete(ctx context.Context, token string, planID string, payload map[string]any) error {
	_, err := doJSON[map[string]any](ctx, c, http.MethodPost, "/api/compute/node-agent/plan/"+planID+"/complete", token, payload)
	return err
}

func (c *client) claimTasks(ctx context.Context, token string, batchSize int, leaseSeconds int) (ClaimTasksResult, error) {
	return doJSON[ClaimTasksResult](ctx, c, http.MethodPost, "/api/compute/node-agent/tasks/claim", token, map[string]int{
		"batchSize":    batchSize,
		"leaseSeconds": leaseSeconds,
	})
}

func (c *client) taskHeartbeat(ctx context.Context, token string, taskID string, request TaskLeaseRequest) error {
	_, err := doJSON[map[string]any](ctx, c, http.MethodPost, "/api/compute/node-agent/tasks/"+taskID+"/heartbeat", token, request)
	return err
}

func (c *client) completeTask(ctx context.Context, token string, taskID string, request CompleteTaskRequest) error {
	_, err := doJSON[map[string]any](ctx, c, http.MethodPost, "/api/compute/node-agent/tasks/"+taskID+"/complete", token, request)
	return err
}

func (c *client) failTask(ctx context.Context, token string, taskID string, request FailTaskRequest) error {
	_, err := doJSON[map[string]any](ctx, c, http.MethodPost, "/api/compute/node-agent/tasks/"+taskID+"/fail", token, request)
	return err
}

func doJSON[T any](ctx context.Context, c *client, method string, path string, token string, body any) (T, error) {
	var zero T
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return zero, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return zero, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return zero, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return zero, &httpResponseError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: string(data)}
	}

	var probe envelopeProbe
	if err := json.Unmarshal(data, &probe); err == nil && len(probe.Data) > 0 && string(probe.Data) != "null" {
		var envelope responseEnvelope[T]
		if err := json.Unmarshal(data, &envelope); err != nil {
			return zero, fmt.Errorf("decode response envelope from %s: %w", path, err)
		}
		return envelope.Data, nil
	}
	var direct T
	if err := json.Unmarshal(data, &direct); err != nil {
		return zero, fmt.Errorf("decode response from %s: %w", path, err)
	}
	return direct, nil
}
