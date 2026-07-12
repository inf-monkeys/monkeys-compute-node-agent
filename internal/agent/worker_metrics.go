package agent

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	maxWorkerProcBytes        = 64 * 1024
	maxWorkerWorkspaceEntries = 2048
	maxWorkerWorkspaceScan    = 75 * time.Millisecond
)

func collectWorkerInspectMetrics(record workerProcessRecord, running bool) map[string]any {
	result := map[string]any{
		"collectedAt": time.Now().UnixMilli(),
		"workspace":   collectWorkerWorkspaceMetrics(record.WorkspacePath),
	}
	if running && runtime.GOOS == "linux" && record.PID > 0 {
		if process := collectWorkerProcessMetrics(record); len(process) > 0 {
			result["process"] = process
		}
	}
	return result
}

func collectWorkerProcessMetrics(record workerProcessRecord) map[string]any {
	base := filepath.Join("/proc", strconv.Itoa(record.PID))
	result := map[string]any{}
	if data, ok := readBoundedFile(filepath.Join(base, "schedstat"), maxWorkerProcBytes); ok {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			if cpuNanos, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
				result["cpuTimeMillis"] = cpuNanos / uint64(time.Millisecond)
				if elapsedMillis := time.Now().UnixMilli() - record.StartedAt; record.StartedAt > 0 && elapsedMillis > 0 {
					average := float64(cpuNanos) / float64(time.Duration(elapsedMillis)*time.Millisecond) * 100
					result["cpuAveragePercent"] = roundNonNegative(average, float64(runtime.NumCPU()*100))
				}
			}
		}
	}
	if data, ok := readBoundedFile(filepath.Join(base, "status"), maxWorkerProcBytes); ok {
		values := parseProcKeyValues(data)
		if rssKiB, ok := values["VmRSS"]; ok {
			result["rssBytes"] = rssKiB * 1024
		}
		if threads, ok := values["Threads"]; ok {
			result["threads"] = threads
		}
	}
	if data, ok := readBoundedFile(filepath.Join(base, "io"), maxWorkerProcBytes); ok {
		values := parseProcKeyValues(data)
		if value, ok := values["read_bytes"]; ok {
			result["readBytes"] = value
		}
		if value, ok := values["write_bytes"]; ok {
			result["writeBytes"] = value
		}
		if value, ok := values["cancelled_write_bytes"]; ok {
			result["cancelledWriteBytes"] = value
		}
	}
	return result
}

func parseProcKeyValues(data []byte) map[string]uint64 {
	result := map[string]uint64{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		parsed, err := strconv.ParseUint(fields[0], 10, 64)
		if err == nil {
			result[strings.TrimSpace(key)] = parsed
		}
	}
	return result
}

func collectWorkerWorkspaceMetrics(path string) map[string]any {
	result := map[string]any{}
	if usage, ok := filesystemUsage(path); ok {
		result["filesystem"] = map[string]any{
			"totalBytes": usage.totalBytes, "usedBytes": usage.usedBytes,
			"availableBytes": usage.availableBytes, "usagePercent": usage.usagePercent,
		}
	}
	deadline := time.Now().Add(maxWorkerWorkspaceScan)
	var logicalBytes uint64
	var allocatedBytes uint64
	var files uint64
	var directories uint64
	var entries int
	truncated := false
	directoriesToScan := []string{path}
	scanIncomplete := false
	for len(directoriesToScan) > 0 && !truncated {
		current := directoriesToScan[len(directoriesToScan)-1]
		directoriesToScan = directoriesToScan[:len(directoriesToScan)-1]
		directory, err := os.Open(current)
		if err != nil {
			if !os.IsNotExist(err) {
				scanIncomplete = true
			}
			continue
		}
		for {
			batch, readErr := directory.ReadDir(64)
			for _, entry := range batch {
				if entries >= maxWorkerWorkspaceEntries || time.Now().After(deadline) {
					truncated = true
					break
				}
				entries++
				currentPath := filepath.Join(current, entry.Name())
				if entry.IsDir() {
					directories++
					directoriesToScan = append(directoriesToScan, currentPath)
					continue
				}
				if entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				info, infoErr := entry.Info()
				if infoErr != nil || !info.Mode().IsRegular() {
					continue
				}
				files++
				if info.Size() > 0 {
					logicalBytes += uint64(info.Size())
				}
				if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Blocks > 0 {
					allocatedBytes += uint64(stat.Blocks) * 512
				}
			}
			if truncated || readErr == io.EOF {
				break
			}
			if readErr != nil {
				scanIncomplete = true
				break
			}
		}
		_ = directory.Close()
	}
	if scanIncomplete {
		result["scanIncomplete"] = true
	}
	result["logicalBytes"] = logicalBytes
	result["allocatedBytes"] = allocatedBytes
	result["files"] = files
	result["directories"] = directories
	result["scannedEntries"] = entries
	if truncated {
		result["scanTruncated"] = true
	}
	return result
}

func readBoundedFile(path string, limit int64) ([]byte, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit))
	return data, err == nil
}

func roundNonNegative(value float64, maximum float64) float64 {
	if value < 0 {
		value = 0
	}
	if maximum > 0 && value > maximum {
		value = maximum
	}
	return float64(int(value*10+0.5)) / 10
}
