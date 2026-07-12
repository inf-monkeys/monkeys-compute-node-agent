package main

import (
	"path/filepath"
	"testing"
)

func TestNormalizeMode(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "host", want: "host"},
		{input: " Cluster ", want: "cluster"},
		{input: "WORKER", want: "worker"},
	} {
		got, err := normalizeMode(test.input)
		if err != nil {
			t.Fatalf("normalizeMode(%q): %v", test.input, err)
		}
		if got != test.want {
			t.Fatalf("normalizeMode(%q) = %q, want %q", test.input, got, test.want)
		}
	}
	if _, err := normalizeMode("node"); err == nil {
		t.Fatal("normalizeMode(node) did not reject an unsupported mode")
	}
}

func TestResolveWorkspace(t *testing.T) {
	t.Parallel()
	if got := resolveWorkspace("/srv/work/../workspace", "/tmp/state.json"); got != "/srv/workspace" {
		t.Fatalf("explicit workspace = %q", got)
	}
	want := filepath.Join("var", "lib", "agent", "workspace")
	if got := resolveWorkspace("", filepath.Join("var", "lib", "agent", "state.json")); got != want {
		t.Fatalf("workspace from state = %q, want %q", got, want)
	}
}
