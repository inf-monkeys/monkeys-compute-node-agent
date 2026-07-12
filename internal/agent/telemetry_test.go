package agent

import (
	"strconv"
	"strings"
	"testing"
)

func TestParseNetworkCountersBoundsAndSkipsInvalidRows(t *testing.T) {
	input := `Inter-| Receive | Transmit
 eth0: 100 1 2 3 4 5 6 7 200 9 10 11 12 13 14 15
 malformed: nope
 lo: 10 1 2 3 4 5 6 7 20 9 10 11 12 13 14 15
`
	result := parseNetworkCounters(strings.NewReader(input))
	if len(result) != 2 || result["eth0"].rxBytes != 100 || result["eth0"].txBytes != 200 {
		t.Fatalf("unexpected counters: %#v", result)
	}
}

func TestParseTelemetryMountsFiltersVirtualAndBoundsCandidates(t *testing.T) {
	lines := []string{
		`36 25 0:32 / / rw,relatime - overlay overlay rw`,
		`37 36 8:1 /data /data\040disk ro,relatime - ext4 /dev/sda1 rw`,
		`38 36 0:5 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw`,
	}
	for index := 0; index < maxTelemetryMountCandidates+10; index++ {
		lines = append(lines, `39 36 8:2 / /mnt/`+strconv.Itoa(index)+` rw,relatime - ext4 /dev/sdb rw`)
	}
	mounts, truncated := parseTelemetryMounts(strings.NewReader(strings.Join(lines, "\n")))
	if !truncated || len(mounts) > maxTelemetryMountCandidates {
		t.Fatalf("mount parsing was not bounded: len=%d truncated=%v", len(mounts), truncated)
	}
	if mounts[0].mountPoint != "/" || mounts[1].mountPoint != "/data disk" || !mounts[1].readOnly {
		t.Fatalf("unexpected parsed mounts: %#v", mounts[:2])
	}
	for _, mount := range mounts {
		if mount.filesystem == "proc" {
			t.Fatal("virtual proc mount must be filtered")
		}
	}
}

func TestBoundedTelemetryString(t *testing.T) {
	if got := boundedTelemetryString("  abcdef  ", 3); got != "abc" {
		t.Fatalf("unexpected bounded string %q", got)
	}
}
