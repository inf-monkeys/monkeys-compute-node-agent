package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/inf-monkeys/monkeys-compute-node-agent/internal/agent"
)

const version = "0.1.0"

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
	bootstrapToken := fs.String("bootstrap-token", env("MONKEYS_BOOTSTRAP_TOKEN", ""), "Bootstrap token from compute control plane")
	statePath := fs.String("state", env("MONKEYS_AGENT_STATE", "agent-state.json"), "State file path")
	name := fs.String("name", env("MONKEYS_NODE_NAME", ""), "Node display name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := agent.Config{
		ServerURL:      *server,
		BootstrapToken: *bootstrapToken,
		StatePath:      *statePath,
		NodeName:       *name,
		Version:        version,
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
	interval := fs.Duration("interval", 30*time.Second, "Heartbeat interval")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return agent.Run(ctx, agent.Config{
		ServerURL: *server,
		StatePath: *statePath,
		Version:   version,
		Interval:  *interval,
	})
}

func runOnce(args []string) error {
	fs := flag.NewFlagSet("once", flag.ExitOnError)
	server := fs.String("server", env("MONKEYS_SERVER", ""), "Monkeys server base URL")
	statePath := fs.String("state", env("MONKEYS_AGENT_STATE", "agent-state.json"), "State file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return agent.Once(context.Background(), agent.Config{
		ServerURL: *server,
		StatePath: *statePath,
		Version:   version,
	})
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func usage() {
	fmt.Fprintf(os.Stderr, `monkeys-compute-node-agent %s

Usage:
  monkeys-compute-node-agent register --server URL --bootstrap-token TOKEN [--state PATH]
  monkeys-compute-node-agent run --server URL [--state PATH]
  monkeys-compute-node-agent once --server URL [--state PATH]
  monkeys-compute-node-agent version
`, version)
}
