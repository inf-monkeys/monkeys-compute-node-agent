package agent

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func newTestKubernetesClient(t *testing.T, handler http.Handler) (*kubernetesClient, *httptest.Server) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.StartTLS()
	t.Cleanup(server.Close)
	directory := t.TempDir()
	tokenPath := filepath.Join(directory, "token")
	caPath := filepath.Join(directory, "ca.crt")
	if err := os.WriteFile(tokenPath, []byte("service-account-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	certificate := server.Certificate()
	if certificate == nil {
		t.Fatal("test server certificate is unavailable")
	}
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := newKubernetesClient(server.URL, tokenPath, caPath)
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}
