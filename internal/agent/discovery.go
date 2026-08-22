package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func Discover(ctx context.Context, version string) Facts {
	hostname, _ := os.Hostname()
	privateIP := discoverPrivateIP()
	publicIP := discoverPublicIP(ctx)
	facts := Facts{
		Hostname:        hostname,
		PublicIP:        publicIP,
		PrivateIP:       privateIP,
		OS:              detectOS(),
		Arch:            runtime.GOARCH,
		CPUCoreCount:    runtime.NumCPU(),
		MemoryBytes:     detectMemoryBytes(),
		GPUs:            detectNvidiaGPUs(ctx),
		Kubernetes:      detectKubernetes(ctx),
		HAMi:            detectHAMi(ctx),
		Labels:          map[string]string{},
		Telemetry:       collectTelemetry(ctx),
		RuntimeStatuses: detectRuntimeStatuses(ctx),
	}
	if len(facts.GPUs) > 0 {
		facts.Labels["gpu"] = "on"
	}
	if version != "" {
		facts.Labels["kernel.runtime.monkeys.ai/version"] = version
	}
	if privateIP != "" {
		facts.Labels["kernel.runtime.monkeys.ai/private-ip"] = privateIP
	}
	if publicIP != "" {
		facts.Labels["kernel.runtime.monkeys.ai/public-ip"] = publicIP
	}
	return facts
}

func heartbeatFromFacts(facts Facts, cfg Config) HeartbeatRequest {
	return HeartbeatRequest{
		TargetKind:      targetKindForMode(cfg.Mode),
		AgentMode:       normalizedMode(cfg.Mode),
		Hostname:        facts.Hostname,
		PublicIP:        facts.PublicIP,
		PrivateIP:       facts.PrivateIP,
		OS:              facts.OS,
		Arch:            facts.Arch,
		CPUCoreCount:    facts.CPUCoreCount,
		MemoryBytes:     facts.MemoryBytes,
		AgentVersion:    cfg.Version,
		GPUs:            facts.GPUs,
		Kubernetes:      facts.Kubernetes,
		HAMi:            facts.HAMi,
		Labels:          facts.Labels,
		Telemetry:       facts.Telemetry,
		RuntimeStatuses: facts.RuntimeStatuses,
		Capabilities:    capabilitiesForFacts(cfg.Mode, cfg.Workspace, facts),
	}
}

func registerFromFacts(facts Facts, cfg Config) RegisterRequest {
	name := cfg.NodeName
	if name == "" {
		name = facts.Hostname
	}
	return RegisterRequest{
		BootstrapToken:  cfg.BootstrapToken,
		TargetKind:      targetKindForMode(cfg.Mode),
		AgentMode:       normalizedMode(cfg.Mode),
		TargetID:        cfg.TargetID,
		AgentInstanceID: cfg.AgentInstanceID,
		Name:            name,
		Hostname:        facts.Hostname,
		PublicIP:        facts.PublicIP,
		PrivateIP:       facts.PrivateIP,
		OS:              facts.OS,
		Arch:            facts.Arch,
		CPUCoreCount:    facts.CPUCoreCount,
		MemoryBytes:     facts.MemoryBytes,
		AgentVersion:    cfg.Version,
		GPUs:            facts.GPUs,
		Kubernetes:      facts.Kubernetes,
		HAMi:            facts.HAMi,
		Labels:          facts.Labels,
		Capabilities:    capabilitiesForFacts(cfg.Mode, cfg.Workspace, facts),
		Telemetry:       facts.Telemetry,
	}
}

func normalizedMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "worker":
		return "worker"
	case "cluster":
		return "cluster"
	default:
		return "host"
	}
}

func targetKindForMode(mode string) string {
	switch normalizedMode(mode) {
	case "worker":
		return "worker"
	case "cluster":
		return "cluster"
	default:
		return "node"
	}
}

func capabilitiesForFacts(mode string, workspace string, facts Facts) map[string]any {
	switch normalizedMode(mode) {
	case "worker":
		return map[string]any{
			"process":          map[string]any{"manage": true},
			"filesystem":       map[string]any{"workspace": true},
			"http":             map[string]any{"localhostProxy": true},
			"resourceBoundary": "upstream-allocation",
			"workspace":        strings.TrimSpace(workspace),
		}
	case "cluster":
		return clusterCapabilities(facts.Kubernetes)
	default:
		return map[string]any{"host": map[string]any{"manage": true}}
	}
}

func detectOS() string {
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		values := map[string]string{}
		for scanner.Scan() {
			line := scanner.Text()
			key, value, ok := strings.Cut(line, "=")
			if ok {
				values[key] = strings.Trim(value, `"`)
			}
		}
		if pretty := values["PRETTY_NAME"]; pretty != "" {
			return pretty
		}
	}
	return runtime.GOOS
}

func detectMemoryBytes() string {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return ""
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kib, err := strconv.ParseInt(fields[1], 10, 64)
			if err == nil {
				return strconv.FormatInt(kib*1024, 10)
			}
		}
	}
	return ""
}

func discoverPrivateIP() string {
	if ip := configuredIP("MONKEYS_PRIVATE_IP"); ip != "" {
		return ip
	}
	if ip := outboundIPv4(); ip != "" {
		return ip
	}
	return firstInterfaceIPv4()
}

func discoverPublicIP(ctx context.Context) string {
	if ip := configuredIP("MONKEYS_PUBLIC_IP"); ip != "" {
		return ip
	}
	if !envBool("MONKEYS_DISCOVER_PUBLIC_IP", true) {
		return ""
	}
	urls := []string{
		os.Getenv("MONKEYS_PUBLIC_IP_URL"),
		"https://ipapi.co/json/",
	}
	for _, url := range urls {
		url = strings.TrimSpace(url)
		if url == "" {
			continue
		}
		if ip := fetchPublicIP(ctx, url); ip != "" {
			return ip
		}
	}
	return ""
}

func configuredIP(key string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return ""
	}
	ip := net.ParseIP(value)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.String()
}

func outboundIPv4() string {
	conn, err := net.DialTimeout("udp", "1.1.1.1:80", 2*time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()
	localAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || localAddr.IP == nil {
		return ""
	}
	ip := localAddr.IP.To4()
	if ip == nil || !isUsableIPv4(ip) {
		return ""
	}
	return ip.String()
}

func firstInterfaceIPv4() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var fallback string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil || !isUsableIPv4(ip) {
				continue
			}
			if ip.IsPrivate() {
				return ip.String()
			}
			if fallback == "" {
				fallback = ip.String()
			}
		}
	}
	return fallback
}

func fetchPublicIP(ctx context.Context, url string) string {
	requestCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8192))
	if err != nil {
		return ""
	}
	return publicIPFromResponseBody(body)
}

func publicIPFromResponseBody(body []byte) string {
	value := strings.TrimSpace(string(body))
	if strings.HasPrefix(value, "{") {
		var payload struct {
			IP string `json:"ip"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return ""
		}
		value = strings.TrimSpace(payload.IP)
	}
	ip := net.ParseIP(value)
	if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.String()
}

func isUsableIPv4(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsLinkLocalUnicast()
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func detectNvidiaGPUs(ctx context.Context) []GPUInfo {
	output, err := runCommand(ctx, "nvidia-smi", "--query-gpu=index,uuid,name,memory.total,memory.used,utilization.gpu,temperature.gpu,power.draw,driver_version", "--format=csv,noheader,nounits")
	if err == nil {
		return parseNvidiaGPUs(output, detectCudaVersion(ctx))
	}
	output, err = runCommand(ctx, "nvidia-smi", "--query-gpu=name,memory.total,driver_version", "--format=csv,noheader,nounits")
	if err != nil {
		return nil
	}
	return parseStaticNvidiaGPUs(output, detectCudaVersion(ctx))
}

func parseNvidiaGPUs(output string, cudaVersion string) []GPUInfo {
	reader := csv.NewReader(strings.NewReader(output))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	result := make([]GPUInfo, 0, 8)
	for len(result) < 32 {
		parts, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(parts) < 9 {
			continue
		}
		index, indexOK := telemetryInt(parts[0])
		memoryMiB, _ := telemetryInt(parts[3])
		memoryUsedMiB, memoryUsedOK := telemetryInt(parts[4])
		utilization, utilizationOK := telemetryFloat(parts[5])
		temperature, temperatureOK := telemetryFloat(parts[6])
		power, powerOK := telemetryFloat(parts[7])
		gpu := GPUInfo{
			Vendor:        "nvidia",
			UUID:          boundedTelemetryString(parts[1], 128),
			Model:         boundedTelemetryString(parts[2], 256),
			Count:         1,
			MemoryMiB:     memoryMiB,
			DriverVersion: boundedTelemetryString(parts[8], 64),
			CUDAVersion:   boundedTelemetryString(cudaVersion, 64),
		}
		if indexOK {
			gpu.Index = &index
		}
		if memoryUsedOK {
			gpu.MemoryUsedMiB = &memoryUsedMiB
		}
		if utilizationOK {
			value := roundPercent(utilization)
			gpu.UtilizationPercent = &value
		}
		if temperatureOK {
			gpu.TemperatureC = &temperature
		}
		if powerOK {
			gpu.PowerWatts = &power
		}
		result = append(result, gpu)
	}
	return result
}

func parseStaticNvidiaGPUs(output string, cudaVersion string) []GPUInfo {
	reader := csv.NewReader(strings.NewReader(output))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	result := make([]GPUInfo, 0, 8)
	for len(result) < 32 {
		parts, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(parts) < 3 {
			continue
		}
		memoryMiB, _ := telemetryInt(parts[1])
		result = append(result, GPUInfo{
			Vendor: "nvidia", Model: boundedTelemetryString(parts[0], 256), Count: 1,
			MemoryMiB: memoryMiB, DriverVersion: boundedTelemetryString(parts[2], 64),
			CUDAVersion: boundedTelemetryString(cudaVersion, 64),
		})
	}
	return result
}

func telemetryInt(value string) (int, bool) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	return parsed, err == nil && parsed >= 0
}

func telemetryFloat(value string) (float64, bool) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return parsed, err == nil && parsed >= 0
}

func detectCudaVersion(ctx context.Context) string {
	output, err := runCommand(ctx, "nvidia-smi")
	if err != nil {
		return ""
	}
	if index := strings.Index(output, "CUDA Version:"); index >= 0 {
		rest := strings.TrimSpace(output[index+len("CUDA Version:"):])
		fields := strings.Fields(rest)
		if len(fields) > 0 {
			return strings.Trim(fields[0], "|")
		}
	}
	return ""
}

func detectKubernetes(ctx context.Context) map[string]any {
	result := map[string]any{
		"installed": false,
		"ready":     false,
	}
	if _, err := exec.LookPath("k3s"); err == nil {
		result["installed"] = true
		result["distribution"] = "k3s"
		if output, err := runCommand(ctx, "k3s", "--version"); err == nil {
			if version := parseK3sVersion(output); version != "" {
				result["version"] = version
			}
			result["distributionVersion"] = strings.TrimSpace(output)
		}
	}
	if len(resolveKubectlCommand()) > 0 {
		result["kubectl"] = true
		if output, err := runKubernetesCommand(ctx, "version", "-o", "json"); err == nil {
			if version := parseKubectlServerVersion(output); version != "" {
				result["version"] = version
			}
		}
	}
	if output, err := runKubernetesCommand(ctx, "get", "node", "-o", "jsonpath={.items[0].metadata.name}"); err == nil && strings.TrimSpace(output) != "" {
		result["ready"] = true
		result["nodeName"] = strings.TrimSpace(output)
	}
	return result
}

func parseKubectlServerVersion(output string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		return ""
	}
	serverVersion, _ := payload["serverVersion"].(map[string]any)
	gitVersion, _ := serverVersion["gitVersion"].(string)
	return strings.TrimSpace(gitVersion)
}

func parseK3sVersion(output string) string {
	fields := strings.Fields(strings.TrimSpace(output))
	for _, field := range fields {
		if strings.HasPrefix(field, "v") && strings.Contains(field, "+k3s") {
			return strings.TrimSpace(field)
		}
	}
	return ""
}

func detectHAMi(ctx context.Context) map[string]any {
	result := map[string]any{
		"nodeLabeled":       false,
		"devicePluginReady": false,
	}
	if output, err := runKubernetesCommand(ctx, "get", "nodes", "-l", "gpu=on", "-o", "name"); err == nil && strings.TrimSpace(output) != "" {
		result["nodeLabeled"] = true
	}
	if output, err := runKubernetesCommand(ctx, "get", "daemonset", "-n", "kube-system", "-o", "name"); err == nil && strings.Contains(strings.ToLower(output), "hami") {
		result["devicePluginReady"] = true
	}
	return result
}

func runKubernetesCommand(ctx context.Context, args ...string) (string, error) {
	command := resolveKubectlCommand()
	if len(command) == 0 {
		return "", fmt.Errorf("kubectl or k3s is not installed")
	}
	command = append(command, args...)
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return runCommandWithEnv(commandCtx, hostKubernetesEnvironment(command), command...)
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}
