package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	kubernetesListPageSize       = 500
	kubernetesMaxListItems       = 2000
	kubernetesMaxResponseBytes   = 16 * 1024 * 1024
	kubernetesServiceAccountPath = "/var/run/secrets/kubernetes.io/serviceaccount"
)

type kubernetesError struct {
	message   string
	retryable bool
}

func (e *kubernetesError) Error() string   { return e.message }
func (e *kubernetesError) Retryable() bool { return e.retryable }

func newKubernetesError(message string, retryable bool) error {
	return &kubernetesError{message: message, retryable: retryable}
}

type kubernetesClient struct {
	baseURL   string
	tokenPath string
	http      *http.Client
}

type kubernetesListResult struct {
	Items     []map[string]any
	Truncated bool
}

func newInClusterKubernetesClient() (*kubernetesClient, error) {
	host := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST"))
	port := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_PORT_HTTPS"))
	if port == "" {
		port = "443"
	}
	if host == "" {
		return nil, newKubernetesError("KUBERNETES_SERVICE_HOST is not set; cluster mode requires an in-cluster ServiceAccount", false)
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return newKubernetesClient(
		"https://"+host+":"+port,
		kubernetesServiceAccountPath+"/token",
		kubernetesServiceAccountPath+"/ca.crt",
	)
}

func inClusterKubernetesConfigured() bool {
	if strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST")) == "" {
		return false
	}
	for _, path := range []string{kubernetesServiceAccountPath + "/token", kubernetesServiceAccountPath + "/ca.crt"} {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

func newKubernetesClient(baseURL, tokenPath, caPath string) (*kubernetesClient, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, newKubernetesError("Kubernetes API URL is required", false)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, newKubernetesError("Kubernetes API URL must be a valid HTTPS URL", false)
	}
	if strings.TrimSpace(tokenPath) == "" {
		return nil, newKubernetesError("Kubernetes ServiceAccount token path is required", false)
	}
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, newKubernetesError(fmt.Sprintf("read Kubernetes ServiceAccount CA: %v", err), false)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, newKubernetesError("Kubernetes ServiceAccount CA is invalid", false)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
	}
	return &kubernetesClient{
		baseURL:   baseURL,
		tokenPath: tokenPath,
		http: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}, nil
}

func (c *kubernetesClient) request(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	body any,
	contentType string,
	allowedStatuses ...int,
) (map[string]any, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, newKubernetesError("Kubernetes request cancelled", true)
	}
	token, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return nil, 0, newKubernetesError(fmt.Sprintf("read Kubernetes ServiceAccount token: %v", err), false)
	}
	if strings.TrimSpace(string(token)) == "" {
		return nil, 0, newKubernetesError("Kubernetes ServiceAccount token is empty", false)
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, 0, newKubernetesError(fmt.Sprintf("encode Kubernetes request: %v", err), false)
		}
		reader = bytes.NewReader(encoded)
	}
	requestURL := c.baseURL + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return nil, 0, newKubernetesError(fmt.Sprintf("create Kubernetes request: %v", err), false)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("User-Agent", "monkeys-compute-node-agent")
	if body != nil {
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		retryable := !errors.Is(err, context.Canceled)
		return nil, 0, newKubernetesError(fmt.Sprintf("Kubernetes API %s %s failed: %v", method, path, err), retryable)
	}
	defer resp.Body.Close()
	if resp.ContentLength > kubernetesMaxResponseBytes {
		return nil, resp.StatusCode, newKubernetesError(fmt.Sprintf("Kubernetes API %s %s response exceeds 16 MiB", method, path), false)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, kubernetesMaxResponseBytes+1))
	if err != nil {
		return nil, resp.StatusCode, newKubernetesError(fmt.Sprintf("read Kubernetes API %s %s response: %v", method, path, err), true)
	}
	if len(raw) > kubernetesMaxResponseBytes {
		return nil, resp.StatusCode, newKubernetesError(fmt.Sprintf("Kubernetes API %s %s response exceeds 16 MiB", method, path), false)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		for _, status := range allowedStatuses {
			if resp.StatusCode == status {
				return decodeKubernetesObject(raw, method, path, resp.StatusCode)
			}
		}
		detail := strings.TrimSpace(string(raw))
		if len(detail) > 4096 {
			detail = detail[:4096]
		}
		if detail == "" {
			detail = http.StatusText(resp.StatusCode)
		}
		retryable := resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, resp.StatusCode, newKubernetesError(
			fmt.Sprintf("Kubernetes API %s %s returned HTTP %d: %s", method, path, resp.StatusCode, detail),
			retryable,
		)
	}
	return decodeKubernetesObject(raw, method, path, resp.StatusCode)
}

func decodeKubernetesObject(raw []byte, method, path string, status int) (map[string]any, int, error) {
	if len(raw) == 0 {
		return map[string]any{}, status, nil
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, status, newKubernetesError(fmt.Sprintf("Kubernetes API %s %s returned invalid JSON", method, path), false)
	}
	return value, status, nil
}

func (c *kubernetesClient) get(ctx context.Context, path string) (map[string]any, error) {
	value, _, err := c.request(ctx, http.MethodGet, path, nil, nil, "")
	return value, err
}

func (c *kubernetesClient) list(ctx context.Context, path, labelSelector string) (kubernetesListResult, error) {
	items := make([]map[string]any, 0)
	continuation := ""
	seen := map[string]struct{}{}
	for len(items) < kubernetesMaxListItems {
		if err := ctx.Err(); err != nil {
			return kubernetesListResult{}, newKubernetesError("Kubernetes list cancelled", true)
		}
		remaining := kubernetesMaxListItems - len(items)
		limit := kubernetesListPageSize
		if remaining < limit {
			limit = remaining
		}
		query := url.Values{"limit": {strconv.Itoa(limit)}}
		if labelSelector != "" {
			query.Set("labelSelector", labelSelector)
		}
		if continuation != "" {
			query.Set("continue", continuation)
		}
		value, _, err := c.request(ctx, http.MethodGet, path, query, nil, "")
		if err != nil {
			return kubernetesListResult{}, err
		}
		for _, item := range objectSlice(value["items"]) {
			if len(items) == kubernetesMaxListItems {
				break
			}
			items = append(items, item)
		}
		next := stringValue(objectValue(value["metadata"])["continue"])
		if next == "" {
			return kubernetesListResult{Items: items}, nil
		}
		if _, exists := seen[next]; exists {
			return kubernetesListResult{}, newKubernetesError(fmt.Sprintf("Kubernetes API pagination repeated a continue token for %s", path), false)
		}
		seen[next] = struct{}{}
		continuation = next
	}
	return kubernetesListResult{Items: items, Truncated: continuation != ""}, nil
}

func objectValue(value any) map[string]any {
	if result, ok := value.(map[string]any); ok {
		return result
	}
	return map[string]any{}
}

func objectSlice(value any) []map[string]any {
	if typed, ok := value.([]map[string]any); ok {
		return typed
	}
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if mapped, ok := item.(map[string]any); ok {
			result = append(result, mapped)
		}
	}
	return result
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func intValue(value any) int {
	switch selected := value.(type) {
	case int:
		return selected
	case int64:
		return int(selected)
	case float64:
		return int(selected)
	case json.Number:
		result, _ := selected.Int64()
		return int(result)
	case string:
		result, _ := strconv.Atoi(strings.TrimSpace(selected))
		return result
	default:
		return 0
	}
}

func cloneObject(value map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}
