package agent

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	maxTelemetryInterfaces         = 32
	maxTelemetryInterfaceAddrs     = 8
	maxTelemetryMounts             = 32
	maxTelemetryMountCandidates    = 256
	maxTelemetryMountInfoLines     = 1024
	maxTelemetryMountInfoBytes     = 2 * 1024 * 1024
	maxTelemetryMountInfoLineBytes = 64 * 1024
	maxTelemetryStringBytes        = 256
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
	result["storage"] = collectStorageTelemetry()
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

type networkCounters struct {
	rxBytes uint64
	txBytes uint64
}

func collectNetworkTelemetry() map[string]any {
	counters := readNetworkCounters()
	var rxTotal uint64
	var txTotal uint64
	for name, values := range counters {
		if name != "lo" {
			rxTotal += values.rxBytes
			txTotal += values.txBytes
		}
	}

	available, err := net.Interfaces()
	if err != nil {
		return map[string]any{"rxBytes": rxTotal, "txBytes": txTotal}
	}
	sort.Slice(available, func(i, j int) bool { return available[i].Name < available[j].Name })
	interfaces := make([]map[string]any, 0, min(len(available), maxTelemetryInterfaces))
	truncated := false
	for _, iface := range available {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(interfaces) >= maxTelemetryInterfaces {
			truncated = true
			break
		}
		addresses := make([]string, 0, maxTelemetryInterfaceAddrs)
		addressesTruncated := false
		if values, addressErr := iface.Addrs(); addressErr == nil {
			for index, address := range values {
				if len(addresses) >= maxTelemetryInterfaceAddrs {
					addressesTruncated = index < len(values)
					break
				}
				addresses = append(addresses, boundedTelemetryString(address.String(), 128))
			}
			sort.Strings(addresses)
		}
		values := counters[iface.Name]
		item := map[string]any{
			"index":           iface.Index,
			"name":            boundedTelemetryString(iface.Name, 64),
			"mtu":             iface.MTU,
			"up":              iface.Flags&net.FlagUp != 0,
			"flags":           boundedTelemetryString(iface.Flags.String(), 128),
			"hardwareAddress": boundedTelemetryString(iface.HardwareAddr.String(), 64),
			"addresses":       addresses,
			"rxBytes":         values.rxBytes,
			"txBytes":         values.txBytes,
		}
		if addressesTruncated {
			item["addressesTruncated"] = true
		}
		interfaces = append(interfaces, item)
	}
	result := map[string]any{
		"rxBytes":    rxTotal,
		"txBytes":    txTotal,
		"interfaces": interfaces,
	}
	if truncated {
		result["interfacesTruncated"] = true
	}
	return result
}

func readNetworkCounters() map[string]networkCounters {
	file, err := os.Open("/proc/net/dev")
	if err != nil {
		return map[string]networkCounters{}
	}
	defer file.Close()
	return parseNetworkCounters(io.LimitReader(file, 1024*1024))
}

func parseNetworkCounters(reader io.Reader) map[string]networkCounters {
	result := map[string]networkCounters{}
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		fields := strings.Fields(rest)
		if name == "" || len(fields) < 16 {
			continue
		}
		rxBytes, rxErr := strconv.ParseUint(fields[0], 10, 64)
		txBytes, txErr := strconv.ParseUint(fields[8], 10, 64)
		if rxErr == nil && txErr == nil {
			result[name] = networkCounters{rxBytes: rxBytes, txBytes: txBytes}
		}
	}
	return result
}

type telemetryMount struct {
	deviceID   string
	source     string
	mountPoint string
	filesystem string
	readOnly   bool
}

func collectStorageTelemetry() map[string]any {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return map[string]any{}
	}
	defer file.Close()
	candidates, candidatesTruncated := parseTelemetryMounts(io.LimitReader(file, maxTelemetryMountInfoBytes))
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].mountPoint == "/" {
			return true
		}
		if candidates[j].mountPoint == "/" {
			return false
		}
		return candidates[i].mountPoint < candidates[j].mountPoint
	})
	mounts := make([]map[string]any, 0, min(len(candidates), maxTelemetryMounts))
	seenDevices := map[string]bool{}
	var totalBytes uint64
	var usedBytes uint64
	var availableBytes uint64
	for _, candidate := range candidates {
		if len(mounts) >= maxTelemetryMounts {
			candidatesTruncated = true
			break
		}
		usage, ok := filesystemUsage(candidate.mountPoint)
		if !ok {
			continue
		}
		mounts = append(mounts, map[string]any{
			"mountPoint":     boundedTelemetryString(candidate.mountPoint, maxTelemetryStringBytes),
			"filesystem":     boundedTelemetryString(candidate.filesystem, 64),
			"device":         boundedTelemetryString(candidate.source, 256),
			"deviceId":       boundedTelemetryString(candidate.deviceID, 64),
			"readOnly":       candidate.readOnly,
			"totalBytes":     usage.totalBytes,
			"usedBytes":      usage.usedBytes,
			"availableBytes": usage.availableBytes,
			"usagePercent":   usage.usagePercent,
		})
		if !seenDevices[candidate.deviceID] {
			seenDevices[candidate.deviceID] = true
			totalBytes += usage.totalBytes
			usedBytes += usage.usedBytes
			availableBytes += usage.availableBytes
		}
	}
	result := map[string]any{
		"totalBytes":     totalBytes,
		"usedBytes":      usedBytes,
		"availableBytes": availableBytes,
		"usagePercent":   usagePercent(usedBytes, totalBytes),
		"mounts":         mounts,
	}
	if candidatesTruncated {
		result["mountsTruncated"] = true
	}
	return result
}

func parseTelemetryMounts(reader io.Reader) ([]telemetryMount, bool) {
	result := make([]telemetryMount, 0)
	seen := map[string]bool{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxTelemetryMountInfoLineBytes)
	truncated := false
	lines := 0
	for scanner.Scan() {
		lines++
		if len(result) >= maxTelemetryMountCandidates || lines > maxTelemetryMountInfoLines {
			truncated = true
			break
		}
		fields := strings.Fields(scanner.Text())
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if len(fields) < 10 || separator < 6 || separator+2 >= len(fields) {
			continue
		}
		filesystem := fields[separator+1]
		if virtualTelemetryFilesystem(filesystem) {
			continue
		}
		mountPoint := decodeMountInfoPath(fields[4])
		if mountPoint == "" || seen[mountPoint] {
			continue
		}
		seen[mountPoint] = true
		result = append(result, telemetryMount{
			deviceID:   fields[2],
			source:     decodeMountInfoPath(fields[separator+2]),
			mountPoint: mountPoint,
			filesystem: filesystem,
			readOnly:   strings.Contains(","+fields[5]+",", ",ro,"),
		})
	}
	return result, truncated
}

func virtualTelemetryFilesystem(value string) bool {
	switch value {
	case "autofs", "bpf", "cgroup", "cgroup2", "configfs", "debugfs", "devpts", "devtmpfs", "fusectl", "hugetlbfs", "mqueue", "proc", "pstore", "ramfs", "securityfs", "sysfs", "tmpfs", "tracefs":
		return true
	default:
		return false
	}
}

func decodeMountInfoPath(value string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(value)
}

type filesystemUsageSnapshot struct {
	totalBytes     uint64
	usedBytes      uint64
	availableBytes uint64
	usagePercent   float64
}

func filesystemUsage(path string) (filesystemUsageSnapshot, bool) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil || stat.Bsize <= 0 {
		return filesystemUsageSnapshot{}, false
	}
	blockSize := uint64(stat.Bsize)
	total := uint64(stat.Blocks) * blockSize
	free := uint64(stat.Bfree) * blockSize
	available := uint64(stat.Bavail) * blockSize
	used := uint64(0)
	if total > free {
		used = total - free
	}
	return filesystemUsageSnapshot{totalBytes: total, usedBytes: used, availableBytes: available, usagePercent: usagePercent(used, total)}, true
}

func usagePercent(used uint64, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return roundPercent(float64(used) / float64(total) * 100)
}

func boundedTelemetryString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
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
