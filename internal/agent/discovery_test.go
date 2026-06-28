package agent

import "testing"

func TestConfiguredIPNormalizesIPv4(t *testing.T) {
	t.Setenv("MONKEYS_PRIVATE_IP", " 192.168.128.134 ")

	if got := configuredIP("MONKEYS_PRIVATE_IP"); got != "192.168.128.134" {
		t.Fatalf("unexpected configured IP: %q", got)
	}
}

func TestConfiguredIPRejectsInvalidValues(t *testing.T) {
	t.Setenv("MONKEYS_PUBLIC_IP", "not-an-ip")

	if got := configuredIP("MONKEYS_PUBLIC_IP"); got != "" {
		t.Fatalf("invalid IP should be ignored, got %q", got)
	}
}

func TestDiscoverPublicIPUsesExplicitEnvOnlyByDefault(t *testing.T) {
	t.Setenv("MONKEYS_PUBLIC_IP", "203.0.113.10")
	t.Setenv("MONKEYS_DISCOVER_PUBLIC_IP", "false")

	if got := discoverPublicIP(t.Context()); got != "203.0.113.10" {
		t.Fatalf("unexpected public IP: %q", got)
	}
}

func TestPublicIPFromIPAPIResponseBody(t *testing.T) {
	body := []byte(`{"ip":"198.51.100.24","city":"Singapore"}`)

	if got := publicIPFromResponseBody(body); got != "198.51.100.24" {
		t.Fatalf("unexpected public IP: %q", got)
	}
}
