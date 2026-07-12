package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var workerHTTPMethods = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "OPTIONS": true}
var workerBlockedHeaders = map[string]bool{"host": true, "connection": true, "proxy-authorization": true, "proxy-authenticate": true, "transfer-encoding": true, "upgrade": true}

func executeWorkerHTTPRequest(ctx context.Context, processes *workerProcessManager, runtimeID string, payload map[string]any) (map[string]any, error) {
	if request, ok := workerMap(payload["request"]); ok {
		payload = request
	}
	if safeIdentifier(runtimeID) == "" {
		runtimeID = strings.TrimSpace(workerString(payload["runtimeId"]))
	}
	if safeIdentifier(runtimeID) == "" {
		return nil, permanentWorkerError("a valid runtimeId is required for http.request")
	}
	state, err := processes.store.load()
	if err != nil {
		return nil, err
	}
	record, exists := state.Processes[runtimeID]
	if !exists || record.RuntimeID != runtimeID {
		return nil, permanentWorkerError("http.request runtime process does not exist")
	}
	if processID := strings.TrimSpace(workerString(payload["processId"])); processID != "" && processID != runtimeID {
		return nil, permanentWorkerError("http.request processId must match task runtimeId")
	}
	if !processRecordRunning(record) {
		return nil, permanentWorkerError("http.request runtime process is not running")
	}
	port := workerInt(payload["port"], 0, 0, 65535)
	if port == 0 || record.ServicePort == 0 || port != record.ServicePort {
		return nil, permanentWorkerError("http.request port must match the process declared servicePort")
	}
	method := strings.ToUpper(strings.TrimSpace(workerString(payload["method"])))
	if method == "" {
		method = "GET"
	}
	if !workerHTTPMethods[method] {
		return nil, permanentWorkerError("unsupported HTTP method: %s", method)
	}
	path := workerString(payload["path"])
	if path == "" {
		path = "/"
	}
	parsed, err := url.ParseRequestURI(path)
	if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || parsed.IsAbs() {
		return nil, permanentWorkerError("path must be an origin-form HTTP path")
	}
	body, err := decodeWorkerBody(payload["bodyBase64"])
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(workerInt(payload["timeoutSeconds"], 15, 1, 120)) * time.Second
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), bytes.NewReader(body))
	if err != nil {
		return nil, permanentWorkerError("invalid localhost request: %v", err)
	}
	if headers, ok := workerMap(payload["headers"]); ok {
		if len(headers) > 100 {
			return nil, permanentWorkerError("headers must have at most 100 entries")
		}
		for key, value := range headers {
			name := strings.TrimSpace(key)
			text := workerString(value)
			if name == "" || workerBlockedHeaders[strings.ToLower(name)] || strings.ContainsAny(text, "\r\n") {
				continue
			}
			request.Header.Set(name, text)
		}
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, selectedPort, err := net.SplitHostPort(address)
		if err != nil || host != "127.0.0.1" || selectedPort != strconv.Itoa(port) {
			return nil, fmt.Errorf("localhost-only HTTP transport rejected destination")
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	}}
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, retryableWorkerError("localhost HTTP request failed: %v", err)
	}
	defer response.Body.Close()
	limit := workerInt(payload["maxResponseBytes"], 4*1024*1024, 1024, 8*1024*1024)
	value, err := io.ReadAll(io.LimitReader(response.Body, int64(limit+1)))
	if err != nil {
		return nil, retryableWorkerError("read localhost HTTP response: %v", err)
	}
	truncated := len(value) > limit
	if truncated {
		value = value[:limit]
	}
	headers := map[string]string{}
	for key, values := range response.Header {
		lower := strings.ToLower(key)
		if lower == "set-cookie" || lower == "connection" || lower == "transfer-encoding" {
			continue
		}
		headers[key] = strings.Join(values, ", ")
	}
	return map[string]any{"status": response.StatusCode, "reason": response.Status, "headers": headers, "bodyBase64": base64.StdEncoding.EncodeToString(value), "truncated": truncated}, nil
}

func decodeWorkerBody(value any) ([]byte, error) {
	if value == nil || workerString(value) == "" {
		return nil, nil
	}
	body, err := base64.StdEncoding.Strict().DecodeString(workerString(value))
	if err != nil {
		return nil, permanentWorkerError("bodyBase64 is invalid")
	}
	if len(body) > 8*1024*1024 {
		return nil, permanentWorkerError("request body exceeds 8 MiB")
	}
	return body, nil
}
