package agent

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func Discover(ctx context.Context, version string) Facts {
	hostname, _ := os.Hostname()
	privateIP := firstPrivateIP()
	facts := Facts{
		Hostname:     hostname,
		PrivateIP:    privateIP,
		OS:           detectOS(),
		Arch:         runtime.GOARCH,
		CPUCoreCount: runtime.NumCPU(),
		MemoryBytes:  detectMemoryBytes(),
		GPUs:         detectNvidiaGPUs(ctx),
		Kubernetes:   detectKubernetes(ctx),
		HAMi:         detectHAMi(ctx),
		Labels:       map[string]string{},
	}
	if len(facts.GPUs) > 0 {
		facts.Labels["gpu"] = "on"
	}
	if version != "" {
		facts.Labels["monkeys.compute.agent/version"] = version
	}
	return facts
}

func heartbeatFromFacts(facts Facts, version string) HeartbeatRequest {
	return HeartbeatRequest{
		Hostname:     facts.Hostname,
		PublicIP:     facts.PublicIP,
		PrivateIP:    facts.PrivateIP,
		OS:           facts.OS,
		Arch:         facts.Arch,
		CPUCoreCount: facts.CPUCoreCount,
		MemoryBytes:  facts.MemoryBytes,
		AgentVersion: version,
		GPUs:         facts.GPUs,
		Kubernetes:   facts.Kubernetes,
		HAMi:         facts.HAMi,
		Labels:       facts.Labels,
	}
}

func registerFromFacts(facts Facts, cfg Config) RegisterRequest {
	name := cfg.NodeName
	if name == "" {
		name = facts.Hostname
	}
	return RegisterRequest{
		BootstrapToken: cfg.BootstrapToken,
		Name:           name,
		Hostname:       facts.Hostname,
		PublicIP:       facts.PublicIP,
		PrivateIP:      facts.PrivateIP,
		OS:             facts.OS,
		Arch:           facts.Arch,
		CPUCoreCount:   facts.CPUCoreCount,
		MemoryBytes:    facts.MemoryBytes,
		AgentVersion:   cfg.Version,
		GPUs:           facts.GPUs,
		Kubernetes:     facts.Kubernetes,
		HAMi:           facts.HAMi,
		Labels:         facts.Labels,
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

func firstPrivateIP() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
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
			if ip == nil {
				continue
			}
			return ip.String()
		}
	}
	return ""
}

func detectNvidiaGPUs(ctx context.Context) []GPUInfo {
	output, err := runCommand(ctx, "nvidia-smi", "--query-gpu=name,memory.total,driver_version", "--format=csv,noheader,nounits")
	if err != nil {
		return nil
	}
	counts := map[string]*GPUInfo{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		parts := strings.Split(line, ",")
		if len(parts) < 3 {
			continue
		}
		model := strings.TrimSpace(parts[0])
		memoryMiB, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		driver := strings.TrimSpace(parts[2])
		key := model + "|" + strconv.Itoa(memoryMiB) + "|" + driver
		if counts[key] == nil {
			counts[key] = &GPUInfo{
				Vendor:        "nvidia",
				Model:         model,
				Count:         0,
				MemoryMiB:     memoryMiB,
				DriverVersion: driver,
				CUDAVersion:   detectCudaVersion(ctx),
			}
		}
		counts[key].Count++
	}
	result := make([]GPUInfo, 0, len(counts))
	for _, gpu := range counts {
		result = append(result, *gpu)
	}
	return result
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
	}
	if _, err := exec.LookPath("kubectl"); err == nil {
		result["kubectl"] = true
	}
	if output, err := runCommand(ctx, "kubectl", "get", "node", "-o", "jsonpath={.items[0].metadata.name}"); err == nil && strings.TrimSpace(output) != "" {
		result["ready"] = true
		result["nodeName"] = strings.TrimSpace(output)
	}
	return result
}

func detectHAMi(ctx context.Context) map[string]any {
	result := map[string]any{
		"nodeLabeled":       false,
		"devicePluginReady": false,
	}
	if output, err := runCommand(ctx, "kubectl", "get", "nodes", "-l", "gpu=on", "-o", "name"); err == nil && strings.TrimSpace(output) != "" {
		result["nodeLabeled"] = true
	}
	if output, err := runCommand(ctx, "kubectl", "get", "daemonset", "-n", "kube-system", "-o", "name"); err == nil && strings.Contains(strings.ToLower(output), "hami") {
		result["devicePluginReady"] = true
	}
	return result
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
