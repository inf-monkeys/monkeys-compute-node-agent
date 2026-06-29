package agent

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type cpuSample struct {
	idle  uint64
	total uint64
}

func collectTelemetry(ctx context.Context) map[string]any {
	result := map[string]any{
		"collectedAt": time.Now().UnixMilli(),
		"os":          runtime.GOOS,
	}
	if runtime.GOOS != "linux" {
		return result
	}
	result["cpu"] = collectCPUTelemetry(ctx)
	result["memory"] = collectMemoryTelemetry()
	result["network"] = collectNetworkTelemetry()
	return result
}

func collectCPUTelemetry(ctx context.Context) map[string]any {
	first, ok := readCPUSample()
	if !ok {
		return map[string]any{"cores": runtime.NumCPU()}
	}
	timer := time.NewTimer(120 * time.Millisecond)
	select {
	case <-ctx.Done():
		timer.Stop()
	case <-timer.C:
	}
	second, ok := readCPUSample()
	if !ok || second.total <= first.total {
		return map[string]any{"cores": runtime.NumCPU()}
	}
	totalDelta := float64(second.total - first.total)
	idleDelta := float64(second.idle - first.idle)
	usage := 0.0
	if totalDelta > 0 {
		usage = ((totalDelta - idleDelta) / totalDelta) * 100
	}
	return map[string]any{
		"cores":        runtime.NumCPU(),
		"usagePercent": roundPercent(usage),
	}
}

func readCPUSample() (cpuSample, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuSample{}, false
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	if !scanner.Scan() {
		return cpuSample{}, false
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuSample{}, false
	}
	var values []uint64
	for _, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			value = 0
		}
		values = append(values, value)
	}
	var total uint64
	for _, value := range values {
		total += value
	}
	idle := values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	return cpuSample{idle: idle, total: total}, true
}

func collectMemoryTelemetry() map[string]any {
	values := readMeminfo()
	total := values["MemTotal"] * 1024
	available := values["MemAvailable"] * 1024
	if available <= 0 {
		available = (values["MemFree"] + values["Buffers"] + values["Cached"]) * 1024
	}
	used := total - available
	if used < 0 {
		used = 0
	}
	usage := 0.0
	if total > 0 {
		usage = (float64(used) / float64(total)) * 100
	}
	return map[string]any{
		"totalBytes":     total,
		"usedBytes":      used,
		"availableBytes": available,
		"usagePercent":   roundPercent(usage),
	}
}

func readMeminfo() map[string]int64 {
	values := map[string]int64{}
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return values
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil {
			values[key] = value
		}
	}
	return values
}

func collectNetworkTelemetry() map[string]any {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return map[string]any{}
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	interfaces := make([]map[string]any, 0)
	var rxTotal uint64
	var txTotal uint64
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" || name == "lo" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 16 {
			continue
		}
		rxBytes, _ := strconv.ParseUint(fields[0], 10, 64)
		txBytes, _ := strconv.ParseUint(fields[8], 10, 64)
		rxTotal += rxBytes
		txTotal += txBytes
		interfaces = append(interfaces, map[string]any{
			"name":    name,
			"rxBytes": rxBytes,
			"txBytes": txBytes,
		})
	}
	return map[string]any{
		"rxBytes":    rxTotal,
		"txBytes":    txTotal,
		"interfaces": interfaces,
	}
}

func roundPercent(value float64) float64 {
	if value < 0 {
		value = 0
	}
	if value > 100 {
		value = 100
	}
	return float64(int(value*10+0.5)) / 10
}
