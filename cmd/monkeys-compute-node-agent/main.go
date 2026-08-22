package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/inf-monkeys/monkeys-compute-node-agent/internal/agent"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "register":
		err = runRegister(os.Args[2:])
	case "run":
		err = runAgent(os.Args[2:])
	case "once":
		err = runOnce(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runRegister(args []string) error {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	server := fs.String("server", env("MONKEYS_SERVER", ""), "Monkeys server base URL")
	bootstrapToken := fs.String("bootstrap-token", env("MONKEYS_BOOTSTRAP_TOKEN", ""), "Bootstrap token from Kernel runtime control plane")
	statePath := fs.String("state", env("MONKEYS_AGENT_STATE", "agent-state.json"), "State file path")
	mode := fs.String("mode", env("MONKEYS_AGENT_MODE", "host"), "Agent mode: host, cluster, or worker")
	targetID := fs.String("target-id", env("MONKEYS_TARGET_ID", ""), "Pre-created Kernel runtime target ID")
	agentInstanceID := fs.String("agent-instance-id", env("MONKEYS_AGENT_INSTANCE_ID", ""), "Stable agent process instance ID")
	workspace := fs.String("workspace", env("MONKEYS_AGENT_WORKSPACE", ""), "Managed workspace root")
	name := fs.String("name", env("MONKEYS_NODE_NAME", ""), "Node display name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resolvedMode, err := normalizeMode(*mode)
	if err != nil {
		return err
	}
	cfg := agent.Config{
		ServerURL:       *server,
		BootstrapToken:  *bootstrapToken,
		StatePath:       *statePath,
		Mode:            resolvedMode,
		TargetID:        *targetID,
		AgentInstanceID: *agentInstanceID,
		Workspace:       resolveWorkspace(*workspace, *statePath),
		NodeName:        *name,
		Version:         version,
	}
	result, err := agent.Register(context.Background(), cfg)
	if err != nil {
		return err
	}
	fmt.Printf("registered node %s (%s)\n", result.Node.Name, result.Node.ID)
	return nil
}

func runAgent(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	server := fs.String("server", env("MONKEYS_SERVER", ""), "Monkeys server base URL")
	statePath := fs.String("state", env("MONKEYS_AGENT_STATE", "agent-state.json"), "State file path")
	mode := fs.String("mode", env("MONKEYS_AGENT_MODE", "host"), "Agent mode: host, cluster, or worker")
	targetID := fs.String("target-id", env("MONKEYS_TARGET_ID", ""), "Kernel runtime target ID for direct-token mode")
	agentInstanceID := fs.String("agent-instance-id", env("MONKEYS_AGENT_INSTANCE_ID", ""), "Stable agent process instance ID")
	workspace := fs.String("workspace", env("MONKEYS_AGENT_WORKSPACE", ""), "Managed workspace root")
	token := env("MONKEYS_AGENT_TOKEN", "")
	fs.StringVar(&token, "token", token, "Agent token (prefer MONKEYS_AGENT_TOKEN to avoid process-list exposure)")
	fs.StringVar(&token, "agent-token", token, "Alias for --token")
	interval := fs.Duration("interval", 30*time.Second, "Heartbeat interval")
	allowInstall := fs.Bool("allow-install", envBool("MONKEYS_ALLOW_INSTALL", false), "Allow install actions to execute")
	dryRun := fs.Bool("dry-run", envBool("MONKEYS_DRY_RUN", true), "Prepare install commands without executing them")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resolvedMode, err := normalizeMode(*mode)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	token = strings.TrimSpace(token)
	return agent.Run(ctx, agent.Config{
		ServerURL:       *server,
		StatePath:       *statePath,
		Mode:            resolvedMode,
		TargetID:        *targetID,
		AgentInstanceID: *agentInstanceID,
		AgentToken:      token,
		Workspace:       resolveWorkspace(*workspace, *statePath),
		Version:         version,
		Interval:        *interval,
		AllowInstall:    *allowInstall,
		DryRun:          *dryRun,
	})
}

func runOnce(args []string) error {
	fs := flag.NewFlagSet("once", flag.ExitOnError)
	server := fs.String("server", env("MONKEYS_SERVER", ""), "Monkeys server base URL")
	statePath := fs.String("state", env("MONKEYS_AGENT_STATE", "agent-state.json"), "State file path")
	mode := fs.String("mode", env("MONKEYS_AGENT_MODE", "host"), "Agent mode: host, cluster, or worker")
	targetID := fs.String("target-id", env("MONKEYS_TARGET_ID", ""), "Kernel runtime target ID for direct-token mode")
	agentInstanceID := fs.String("agent-instance-id", env("MONKEYS_AGENT_INSTANCE_ID", ""), "Stable agent process instance ID")
	workspace := fs.String("workspace", env("MONKEYS_AGENT_WORKSPACE", ""), "Managed workspace root")
	token := env("MONKEYS_AGENT_TOKEN", "")
	fs.StringVar(&token, "token", token, "Agent token (prefer MONKEYS_AGENT_TOKEN to avoid process-list exposure)")
	fs.StringVar(&token, "agent-token", token, "Alias for --token")
	allowInstall := fs.Bool("allow-install", envBool("MONKEYS_ALLOW_INSTALL", false), "Allow install actions to execute")
	dryRun := fs.Bool("dry-run", envBool("MONKEYS_DRY_RUN", true), "Prepare install commands without executing them")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resolvedMode, err := normalizeMode(*mode)
	if err != nil {
		return err
	}
	token = strings.TrimSpace(token)
	return agent.Once(context.Background(), agent.Config{
		ServerURL:       *server,
		StatePath:       *statePath,
		Mode:            resolvedMode,
		TargetID:        *targetID,
		AgentInstanceID: *agentInstanceID,
		AgentToken:      token,
		Workspace:       resolveWorkspace(*workspace, *statePath),
		Version:         version,
		AllowInstall:    *allowInstall,
		DryRun:          *dryRun,
	})
}

func normalizeMode(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	switch mode {
	case "host", "cluster", "worker":
		return mode, nil
	default:
		return "", fmt.Errorf("invalid agent mode %q: expected host, cluster, or worker", value)
	}
}

func resolveWorkspace(value, statePath string) string {
	if strings.TrimSpace(value) != "" {
		return filepath.Clean(value)
	}
	if strings.TrimSpace(statePath) == "" {
		return "workspace"
	}
	return filepath.Join(filepath.Dir(filepath.Clean(statePath)), "workspace")
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func usage() {
	fmt.Fprintf(os.Stderr, `monkeys-compute-node-agent %s

Usage:
  monkeys-compute-node-agent register --mode MODE --server URL --bootstrap-token TOKEN [--target-id ID] [--state PATH]
  monkeys-compute-node-agent run --mode MODE --server URL [--target-id ID] [--token TOKEN] [--state PATH]
  monkeys-compute-node-agent once --mode MODE --server URL [--target-id ID] [--token TOKEN] [--state PATH]
  monkeys-compute-node-agent version

Modes:
  host      Manage a full Linux host and optional local Kubernetes installation.
  cluster   Observe and operate a Kubernetes cluster from an in-cluster Agent pod.
  worker    Run and observe trusted workload processes inside one rented container or pod.

Set MONKEYS_AGENT_TOKEN instead of --token when possible so the token is not exposed in the process list.
`, version)
}
