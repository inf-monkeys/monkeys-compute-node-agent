package agent

import (
	"strings"
	"testing"
)

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

func TestParseNvidiaGPUsIncludesBoundedDynamicMetrics(t *testing.T) {
	output := "0, GPU-abc, NVIDIA A100-SXM4-80GB, 81920, 2048, 75.5, 61, 212.75, 550.54.15\n" +
		"1, GPU-def, \"NVIDIA, Special\", 40960, N/A, N/A, [Not Supported], [N/A], 550.54.15\n"
	result := parseNvidiaGPUs(output, "12.4")
	if len(result) != 2 {
		t.Fatalf("unexpected GPU count: %#v", result)
	}
	first := result[0]
	if first.Index == nil || *first.Index != 0 || first.Count != 1 || first.MemoryMiB != 81920 || first.MemoryUsedMiB == nil || *first.MemoryUsedMiB != 2048 {
		t.Fatalf("unexpected first GPU: %#v", first)
	}
	if first.UtilizationPercent == nil || *first.UtilizationPercent != 75.5 || first.TemperatureC == nil || *first.TemperatureC != 61 || first.PowerWatts == nil || *first.PowerWatts != 212.75 {
		t.Fatalf("dynamic metrics missing: %#v", first)
	}
	if result[1].Model != "NVIDIA, Special" || result[1].MemoryUsedMiB != nil || result[1].PowerWatts != nil {
		t.Fatalf("unsupported metrics must degrade independently: %#v", result[1])
	}
}

func TestParseNvidiaGPUsCapsPayload(t *testing.T) {
	line := "0, GPU-abc, NVIDIA T4, 16384, 0, 0, 30, 20, 550.1"
	result := parseNvidiaGPUs(strings.Repeat(line+"\n", 64), "12.4")
	if len(result) != 32 {
		t.Fatalf("GPU payload must be capped, got %d", len(result))
	}
}

func TestParseStaticNvidiaGPUsFallback(t *testing.T) {
	result := parseStaticNvidiaGPUs("NVIDIA T4, 16384, 550.1\n", "12.4")
	if len(result) != 1 || result[0].Count != 1 || result[0].MemoryMiB != 16384 || result[0].UtilizationPercent != nil {
		t.Fatalf("unexpected static fallback: %#v", result)
	}
}
