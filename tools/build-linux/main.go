package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	binaryName = "monkeys-compute-node-agent"
	mainPkg    = "./cmd/monkeys-compute-node-agent"
	distDir    = "dist"
)

func main() {
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		exit(err)
	}

	for _, arch := range []string{"amd64", "arm64"} {
		output := filepath.Join(distDir, fmt.Sprintf("%s_linux_%s", binaryName, arch))
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", output, mainPkg)
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			exit(fmt.Errorf("build linux/%s: %w", arch, err))
		}
		fmt.Printf("built %s\n", output)
	}
}

func exit(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
