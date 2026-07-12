package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	version := buildVersion()
	if strings.ContainsAny(version, " \t\r\n") {
		exit(fmt.Errorf("version must not contain whitespace: %q", version))
	}
	artifacts := make([]string, 0, 2)
	for _, arch := range []string{"amd64", "arm64"} {
		output := filepath.Join(distDir, fmt.Sprintf("%s_linux_%s", binaryName, arch))
		ldflags := fmt.Sprintf("-s -w -X main.version=%s", version)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags="+ldflags, "-o", output, mainPkg)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			exit(fmt.Errorf("build linux/%s: %w", arch, err))
		}
		artifacts = append(artifacts, output)
		fmt.Printf("built %s\n", output)
	}
	installer := filepath.Join(distDir, "install.sh")
	if err := copyFile("scripts/install.sh", installer, 0o755); err != nil {
		exit(err)
	}
	artifacts = append(artifacts, installer)
	if err := writeChecksums(artifacts); err != nil {
		exit(err)
	}
}

func buildVersion() string {
	if version := strings.TrimSpace(os.Getenv("VERSION")); version != "" {
		return version
	}
	output, err := exec.Command("git", "describe", "--tags", "--always", "--dirty").Output()
	if err != nil {
		return "dev"
	}
	return strings.TrimSpace(string(output))
}

func copyFile(source, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open %s: %w", source, err)
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", target, err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return fmt.Errorf("copy %s: %w", source, err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close %s: %w", target, err)
	}
	return os.Chmod(target, mode)
}

func writeChecksums(paths []string) error {
	checksumsPath := filepath.Join(distDir, "SHA256SUMS")
	checksums, err := os.Create(checksumsPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", checksumsPath, err)
	}
	defer checksums.Close()
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return fmt.Errorf("hash %s: %w", path, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close %s: %w", path, closeErr)
		}
		if _, err := fmt.Fprintf(checksums, "%x  %s\n", hash.Sum(nil), filepath.Base(path)); err != nil {
			return fmt.Errorf("write %s: %w", checksumsPath, err)
		}
	}
	return checksums.Sync()
}

func exit(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
