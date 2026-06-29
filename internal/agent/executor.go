package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func executeAction(ctx context.Context, cfg Config, action PlanAction) ActionResult {
	switch action.Type {
	case "agent.register", "inspect", "noop":
		return ActionResult{
			ActionType: action.Type,
			Success:    true,
			Message:    "Action completed.",
		}
	case "agent.update", "agent.upgrade":
		return runOrPlan(ctx, cfg, action, buildAgentUpdateCommand(cfg, action.Payload))
	case "k3s.preflight":
		return k3sPreflight(ctx, action)
	case "hami.preflight":
		return hamiPreflight(ctx, action)
	case "k3s.install-server":
		return runK3sInstallServer(ctx, cfg, action)
	case "k3s.join-agent":
		return runOrPlan(ctx, cfg, action, buildK3sJoinAgentCommand(action.Payload))
	case "hami.install":
		return runOrPlan(ctx, cfg, action, buildHamiInstallCommand(action.Payload))
	case "k8s.apply-runtime":
		return applyRuntimeManifests(ctx, cfg, action)
	default:
		return ActionResult{
			ActionType: action.Type,
			Success:    false,
			Message:    "Unsupported action.",
		}
	}
}

func applyRuntimeManifests(ctx context.Context, cfg Config, action PlanAction) ActionResult {
	manifestYaml := strings.TrimSpace(asString(action.Payload["manifestYaml"]))
	namespace := firstNonEmpty(asString(action.Payload["namespace"]), "default")
	runtimeID := asString(action.Payload["runtimeId"])
	manifestCount := asString(action.Payload["manifestCount"])
	if manifestYaml == "" {
		return ActionResult{
			ActionType: action.Type,
			Success:    false,
			Message:    "manifestYaml is required.",
		}
	}

	details := map[string]any{
		"runtimeId":     runtimeID,
		"namespace":     namespace,
		"manifestCount": manifestCount,
	}
	if cfg.DryRun || !cfg.AllowInstall {
		return ActionResult{
			ActionType: action.Type,
			Success:    true,
			Planned:    true,
			DryRun:     true,
			Message:    "Runtime manifest apply prepared.",
			Details:    details,
		}
	}
	if runtime.GOOS != "linux" {
		return ActionResult{
			ActionType: action.Type,
			Success:    false,
			Planned:    true,
			DryRun:     false,
			Message:    "Kubernetes manifest apply is only supported on Linux hosts.",
			Details:    details,
		}
	}

	kubectl := resolveKubectlCommand()
	if len(kubectl) == 0 {
		return ActionResult{
			ActionType: action.Type,
			Success:    false,
			Planned:    true,
			DryRun:     false,
			Message:    "kubectl or k3s is required to apply runtime manifests.",
			Details:    details,
		}
	}

	tmpFile, err := os.CreateTemp("", "monkeys-runtime-*.yaml")
	if err != nil {
		return ActionResult{
			ActionType: action.Type,
			Success:    false,
			Planned:    true,
			Message:    fmt.Sprintf("create temporary manifest file: %v", err),
			Details:    details,
		}
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	if _, err := tmpFile.WriteString(manifestYaml + "\n"); err != nil {
		tmpFile.Close()
		return ActionResult{
			ActionType: action.Type,
			Success:    false,
			Planned:    true,
			Message:    fmt.Sprintf("write temporary manifest file: %v", err),
			Details:    details,
		}
	}
	if err := tmpFile.Close(); err != nil {
		return ActionResult{
			ActionType: action.Type,
			Success:    false,
			Planned:    true,
			Message:    fmt.Sprintf("close temporary manifest file: %v", err),
			Details:    details,
		}
	}

	command := append(append([]string{}, kubectl...), "apply", "-f", tmpPath)
	kubeconfigPath := firstNonEmpty(asString(action.Payload["kubeconfigPath"]), "/etc/rancher/k3s/k3s.yaml")
	env := map[string]string{}
	if kubeconfigPath != "" {
		env["KUBECONFIG"] = kubeconfigPath
	}
	output, err := runCommandWithEnv(ctx, env, command...)
	details["command"] = append(append([]string{}, kubectl...), "apply", "-f", "<rendered-runtime-manifest>")
	details["output"] = output
	if err != nil {
		return ActionResult{
			ActionType: action.Type,
			Success:    false,
			Planned:    true,
			DryRun:     false,
			Message:    strings.TrimSpace(output) + "\n" + err.Error(),
			Details:    details,
		}
	}
	return ActionResult{
		ActionType: action.Type,
		Success:    true,
		Planned:    true,
		DryRun:     false,
		Message:    "Runtime manifests applied.",
		Details:    details,
		Artifacts: map[string]any{
			"runtimeResults": []map[string]any{
				{
					"runtimeId": runtimeID,
					"namespace": namespace,
					"action":    action.Type,
					"success":   true,
					"message":   "Runtime manifests applied.",
				},
			},
		},
	}
}

func runK3sInstallServer(ctx context.Context, cfg Config, action PlanAction) ActionResult {
	result := runOrPlan(ctx, cfg, action, buildK3sInstallServerCommand(action.Payload))
	if !result.Success || result.DryRun {
		return result
	}

	kubeconfig, endpoint, err := collectManagedKubeconfig(action.Payload)
	if err != nil {
		result.Success = false
		result.Message = strings.TrimSpace(result.Message + "\n" + err.Error())
		if result.Details == nil {
			result.Details = map[string]any{}
		}
		result.Details["kubeconfigCollected"] = false
		return result
	}
	if result.Details == nil {
		result.Details = map[string]any{}
	}
	result.Details["kubeconfigCollected"] = true
	result.Details["apiServerEndpoint"] = endpoint
	result.Artifacts = map[string]any{
		"kubeconfig":        kubeconfig,
		"apiServerEndpoint": endpoint,
		"defaultNamespace":  firstNonEmpty(asString(action.Payload["defaultNamespace"]), "default"),
	}
	if namespaceScopes := stringSliceFromPayload(action.Payload, "namespaceScopes", "namespaces"); len(namespaceScopes) > 0 {
		result.Artifacts["namespaceScopes"] = namespaceScopes
	}
	if clusterAlias := firstNonEmpty(asString(action.Payload["clusterAlias"]), asString(action.Payload["name"])); clusterAlias != "" {
		result.Artifacts["clusterAlias"] = clusterAlias
	}
	return result
}

func k3sPreflight(ctx context.Context, action PlanAction) ActionResult {
	details := map[string]any{
		"hasK3s":       commandExists("k3s"),
		"hasKubectl":   commandExists("kubectl"),
		"hasSystemctl": commandExists("systemctl"),
		"hasCurl":      commandExists("curl"),
		"hasWget":      commandExists("wget"),
	}
	if output, err := runCommand(ctx, "k3s", "--version"); err == nil {
		details["k3sVersion"] = firstLine(output)
	}
	if output, err := runCommand(ctx, "kubectl", "version", "--client=true", "--short"); err == nil {
		details["kubectlVersion"] = strings.TrimSpace(output)
	}
	return ActionResult{
		ActionType: action.Type,
		Success:    details["hasSystemctl"] == true && (details["hasCurl"] == true || details["hasWget"] == true),
		Message:    "K3s preflight completed.",
		Details:    details,
	}
}

func hamiPreflight(ctx context.Context, action PlanAction) ActionResult {
	gpus := detectNvidiaGPUs(ctx)
	kubernetesReady, kubernetesNodes := detectKubernetesReady(ctx)
	details := map[string]any{
		"hasNvidiaSMI":              commandExists("nvidia-smi"),
		"hasKubectl":                commandExists("kubectl"),
		"hasHelm":                   commandExists("helm"),
		"hasContainerd":             commandExists("containerd") || commandExists("ctr"),
		"hasDocker":                 commandExists("docker"),
		"hasNvidiaContainerRuntime": commandExists("nvidia-container-runtime"),
		"hasNvidiaCtk":              commandExists("nvidia-ctk"),
		"kubernetesReady":           kubernetesReady,
		"kubernetesNodes":           kubernetesNodes,
		"nvidiaGPUCount":            totalGPUCount(gpus),
		"nvidiaGPUSummary":          gpus,
		"nodeLabeled":               false,
		"devicePluginReady":         false,
	}
	hami := detectHAMi(ctx)
	for key, value := range hami {
		details[key] = value
	}
	missing := missingChecks(details, "hasNvidiaSMI", "hasKubectl", "hasHelm", "kubernetesReady")
	if totalGPUCount(gpus) <= 0 {
		missing = append(missing, "nvidiaGPUCount")
	}
	if details["hasContainerd"] != true && details["hasDocker"] != true {
		missing = append(missing, "containerRuntime")
	}
	if details["hasNvidiaContainerRuntime"] != true && details["hasNvidiaCtk"] != true {
		missing = append(missing, "nvidiaContainerRuntime")
	}
	details["missing"] = missing
	return ActionResult{
		ActionType: action.Type,
		Success:    len(missing) == 0,
		Message:    "HAMi preflight completed.",
		Details:    details,
	}
}

func runOrPlan(ctx context.Context, cfg Config, action PlanAction, command []string) ActionResult {
	if len(command) == 0 {
		return ActionResult{
			ActionType: action.Type,
			Success:    false,
			Message:    "Unable to build command.",
		}
	}

	planned := ActionResult{
		ActionType: action.Type,
		Success:    true,
		Planned:    true,
		DryRun:     true,
		Message:    "Install command prepared.",
		Command:    command,
		Details: map[string]any{
			"command": command,
		},
	}

	if cfg.DryRun || !cfg.AllowInstall {
		return planned
	}
	if runtime.GOOS != "linux" {
		return ActionResult{
			ActionType: action.Type,
			Success:    false,
			Planned:    true,
			DryRun:     false,
			Message:    "Installation actions are only supported on Linux hosts.",
			Command:    command,
		}
	}

	output, err := runShellCommand(ctx, command)
	if err != nil {
		return ActionResult{
			ActionType: action.Type,
			Success:    false,
			Planned:    true,
			DryRun:     false,
			Message:    strings.TrimSpace(output) + "\n" + err.Error(),
			Command:    command,
			Details: map[string]any{
				"output": output,
			},
		}
	}

	return ActionResult{
		ActionType: action.Type,
		Success:    true,
		Planned:    true,
		DryRun:     false,
		Message:    "Install command executed.",
		Command:    command,
		Details: map[string]any{
			"output": output,
		},
	}
}

func runShellCommand(ctx context.Context, command []string) (string, error) {
	if len(command) == 0 {
		return "", fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func runCommandWithEnv(ctx context.Context, env map[string]string, command ...string) (string, error) {
	if len(command) == 0 {
		return "", fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	if len(env) > 0 {
		cmd.Env = os.Environ()
		for key, value := range env {
			if strings.TrimSpace(key) == "" {
				continue
			}
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func resolveKubectlCommand() []string {
	if commandExists("kubectl") {
		return []string{"kubectl"}
	}
	if commandExists("k3s") {
		return []string{"k3s", "kubectl"}
	}
	return nil
}

func buildK3sInstallServerCommand(payload map[string]any) []string {
	config := normalizeActionConfig(payload)
	flags := make([]string, 0, 8)
	if nodeName := config["nodeName"]; nodeName != "" {
		flags = append(flags, "--node-name", shellQuote(nodeName))
	}
	if dataDir := config["dataDir"]; dataDir != "" {
		flags = append(flags, "--data-dir", shellQuote(dataDir))
	}
	if strings.EqualFold(config["clusterInit"], "true") {
		flags = append(flags, "--cluster-init")
	}
	if advertiseAddress := config["advertiseAddress"]; advertiseAddress != "" {
		flags = append(flags, "--advertise-address", shellQuote(advertiseAddress))
	}
	if nodeIP := config["nodeIP"]; nodeIP != "" {
		flags = append(flags, "--node-ip", shellQuote(nodeIP))
	}
	if disables := stringSliceFromPayload(payload, "disable", "disables"); len(disables) > 0 {
		for _, item := range disables {
			flags = append(flags, "--disable", shellQuote(item))
		}
	}
	if extras := stringSliceFromPayload(payload, "extraArgs", "args"); len(extras) > 0 {
		flags = append(flags, extras...)
	}

	script := fmt.Sprintf(
		"curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC=%s K3S_TOKEN=%s sh -",
		shellQuote(strings.TrimSpace(strings.Join(append([]string{"server"}, flags...), " "))),
		shellQuote(firstNonEmpty(config["token"], config["k3sToken"], "<k3s-token>")),
	)
	return []string{"sh", "-c", script}
}

func collectManagedKubeconfig(payload map[string]any) (string, string, error) {
	path := firstNonEmpty(asString(payload["kubeconfigPath"]), "/etc/rancher/k3s/k3s.yaml")
	content, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read k3s kubeconfig %s: %w", path, err)
	}
	kubeconfig := string(content)
	endpoint := resolveKubeconfigEndpoint(payload)
	if endpoint == "" {
		return "", "", fmt.Errorf("unable to resolve reachable Kubernetes API endpoint; set apiServerEndpoint in the action payload")
	}
	return rewriteKubeconfigServer(kubeconfig, endpoint), endpoint, nil
}

func resolveKubeconfigEndpoint(payload map[string]any) string {
	if endpoint := firstNonEmpty(asString(payload["apiServerEndpoint"]), asString(payload["serverEndpoint"])); endpoint != "" {
		return normalizeKubeAPIServerEndpoint(endpoint)
	}
	host := firstNonEmpty(asString(payload["apiServerHost"]), asString(payload["advertiseAddress"]), asString(payload["nodeIP"]))
	if host == "" {
		facts := Discover(context.Background(), "")
		host = firstNonEmpty(facts.PrivateIP, facts.PublicIP)
	}
	if host == "" {
		return ""
	}
	port := firstNonEmpty(asString(payload["apiServerPort"]), "6443")
	return normalizeKubeAPIServerEndpoint(host + ":" + port)
}

func rewriteKubeconfigServer(kubeconfig string, endpoint string) string {
	lines := strings.Split(kubeconfig, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "server:") {
			prefix := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[index] = prefix + "server: " + endpoint
		}
	}
	return strings.Join(lines, "\n")
}

func normalizeKubeAPIServerEndpoint(value string) string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return ""
	}
	if !strings.Contains(normalized, "://") {
		normalized = "https://" + normalized
	}
	return strings.TrimRight(normalized, "/")
}

func buildK3sJoinAgentCommand(payload map[string]any) []string {
	config := normalizeActionConfig(payload)
	flags := make([]string, 0, 6)
	if nodeName := config["nodeName"]; nodeName != "" {
		flags = append(flags, "--node-name", shellQuote(nodeName))
	}
	if nodeIP := config["nodeIP"]; nodeIP != "" {
		flags = append(flags, "--node-ip", shellQuote(nodeIP))
	}
	if labels := stringSliceFromPayload(payload, "labels"); len(labels) > 0 {
		for _, label := range labels {
			flags = append(flags, "--node-label", shellQuote(label))
		}
	}
	if taints := stringSliceFromPayload(payload, "taints"); len(taints) > 0 {
		for _, taint := range taints {
			flags = append(flags, "--node-taint", shellQuote(taint))
		}
	}
	if extras := stringSliceFromPayload(payload, "extraArgs", "args"); len(extras) > 0 {
		flags = append(flags, extras...)
	}

	serverURL := firstNonEmpty(config["serverUrl"], config["url"], "<compute-server-url>")
	token := firstNonEmpty(config["token"], config["k3sToken"], "<k3s-token>")
	script := fmt.Sprintf(
		"curl -sfL https://get.k3s.io | K3S_URL=%s K3S_TOKEN=%s sh - %s",
		shellQuote(serverURL),
		shellQuote(token),
		strings.TrimSpace(strings.Join(flags, " ")),
	)
	return []string{"sh", "-c", script}
}

func buildHamiInstallCommand(payload map[string]any) []string {
	config := normalizeActionConfig(payload)
	release := firstNonEmpty(config["release"], config["version"], "v2.6.0")
	host := firstNonEmpty(config["serverUrl"], "<compute-server-url>")
	schedulerName := firstNonEmpty(config["schedulerName"], "hami-scheduler")
	resourceGpu := firstNonEmpty(config["resourceGpu"], "nvidia.com/gpu")
	resourceGpuMem := firstNonEmpty(config["resourceGpuMem"], "nvidia.com/gpumem")
	resourceGpuCores := firstNonEmpty(config["resourceGpuCores"], "nvidia.com/gpucores")
	resourceGpuMemPercentage := firstNonEmpty(config["resourceGpuMemPercentage"], "nvidia.com/gpumem-percentage")
	setArgs := []string{
		fmt.Sprintf("--set scheduler.image.tag=%s", shellQuote(release)),
		fmt.Sprintf("--set scheduler.schedulerName=%s", shellQuote(schedulerName)),
		"--set controller.manager.env[0].name=MONKEYS_SERVER",
		fmt.Sprintf("--set controller.manager.env[0].value=%s", shellQuote(host)),
		"--set controller.manager.env[1].name=RESOURCE_GPU",
		fmt.Sprintf("--set controller.manager.env[1].value=%s", shellQuote(resourceGpu)),
		"--set controller.manager.env[2].name=RESOURCE_GPUMEM",
		fmt.Sprintf("--set controller.manager.env[2].value=%s", shellQuote(resourceGpuMem)),
		"--set controller.manager.env[3].name=RESOURCE_GPUCORES",
		fmt.Sprintf("--set controller.manager.env[3].value=%s", shellQuote(resourceGpuCores)),
		"--set controller.manager.env[4].name=RESOURCE_GPUMEM_PERCENTAGE",
		fmt.Sprintf("--set controller.manager.env[4].value=%s", shellQuote(resourceGpuMemPercentage)),
	}
	command := "helm repo add hami https://project-hami.github.io/HAMi && helm repo update && helm upgrade --install hami hami/hami --namespace kube-system --create-namespace " + strings.Join(setArgs, " ")
	return []string{"sh", "-c", command}
}

func buildAgentUpdateCommand(cfg Config, payload map[string]any) []string {
	config := normalizeActionConfig(payload)
	downloadURL := firstNonEmpty(config["downloadUrl"], config["url"])
	if downloadURL == "" {
		serverURL := strings.TrimRight(firstNonEmpty(config["serverUrl"], cfg.ServerURL), "/")
		if serverURL == "" {
			return nil
		}
		downloadURL = fmt.Sprintf("%s/api/compute/node-agent/download/monkeys-compute-node-agent_linux_%s", serverURL, runtime.GOARCH)
	}
	installPath := firstNonEmpty(config["installPath"], "/usr/local/bin/monkeys-compute-node-agent")
	serviceName := firstNonEmpty(config["serviceName"], "monkeys-compute-node-agent")
	checksum := firstNonEmpty(config["sha256"], config["checksum"])
	checksumBlock := ":"
	if checksum != "" {
		checksumBlock = fmt.Sprintf("printf '%%s  %%s\\n' %s \"$tmp\" | sha256sum -c -", shellQuote(checksum))
	}
	restartScript := fmt.Sprintf("sleep 2; systemctl restart %s", shellQuote(serviceName))
	script := fmt.Sprintf(`set -eu
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
if command -v curl >/dev/null 2>&1; then
  curl -fsSL %s -o "$tmp"
elif command -v wget >/dev/null 2>&1; then
  wget -q %s -O "$tmp"
else
  echo "curl or wget is required" >&2
  exit 1
fi
chmod +x "$tmp"
"$tmp" version
%s
install -m 0755 "$tmp" %s
if command -v systemctl >/dev/null 2>&1; then
  nohup sh -c %s >/dev/null 2>&1 &
fi`, shellQuote(downloadURL), shellQuote(downloadURL), checksumBlock, shellQuote(installPath), shellQuote(restartScript))
	return []string{"sh", "-c", script}
}

func normalizeActionConfig(payload map[string]any) map[string]string {
	result := map[string]string{}
	for key, value := range payload {
		if value == nil {
			continue
		}
		result[key] = strings.TrimSpace(fmt.Sprint(value))
	}
	return result
}

func stringSliceFromPayload(payload map[string]any, keys ...string) []string {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case []string:
			return compactStrings(typed)
		case []any:
			items := make([]string, 0, len(typed))
			for _, item := range typed {
				items = append(items, strings.TrimSpace(fmt.Sprint(item)))
			}
			return compactStrings(items)
		case string:
			return compactStrings(strings.FieldsFunc(typed, func(r rune) bool {
				return r == ',' || r == '\n' || r == '\r'
			}))
		}
	}
	return nil
}

func compactStrings(values []string) []string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			filtered = append(filtered, trimmed)
		}
	}
	return filtered
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func asString(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(value), "\n")
	return line
}

func totalGPUCount(gpus []GPUInfo) int {
	total := 0
	for _, gpu := range gpus {
		if gpu.Count > 0 {
			total += gpu.Count
		}
	}
	return total
}

func missingChecks(details map[string]any, keys ...string) []string {
	missing := make([]string, 0)
	for _, key := range keys {
		if details[key] != true {
			missing = append(missing, key)
		}
	}
	return missing
}

func detectKubernetesReady(ctx context.Context) (bool, string) {
	if !commandExists("kubectl") {
		return false, ""
	}
	output, err := runCommand(ctx, "kubectl", "get", "nodes", "--no-headers")
	if err != nil {
		return false, strings.TrimSpace(output)
	}
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return false, ""
	}
	for _, line := range strings.Split(trimmed, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.Contains(fields[1], "Ready") && !strings.Contains(fields[1], "NotReady") {
			return true, trimmed
		}
	}
	return false, trimmed
}

func eventForActionResult(result ActionResult) EventRequest {
	severity := "info"
	eventType := "node.plan.action.completed"
	if !result.Success {
		severity = "warning"
		eventType = "node.plan.action.failed"
		if result.Message == "Unsupported action." {
			eventType = "node.plan.action.unsupported"
		}
	}
	return EventRequest{
		EventType: eventType,
		Severity:  severity,
		Message:   fmt.Sprintf("%s: %s", result.ActionType, result.Message),
		Payload: map[string]any{
			"actionType": result.ActionType,
			"success":    result.Success,
			"planned":    result.Planned,
			"dryRun":     result.DryRun,
			"command":    result.Command,
			"details":    result.Details,
		},
	}
}
