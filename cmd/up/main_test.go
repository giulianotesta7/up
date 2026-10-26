package main

import (
	"bytes"
	"testing"
)

func TestRunPrintsPlaceholder(t *testing.T) {
	var stdout bytes.Buffer

	code := run(&stdout)

	if code != 0 {
		t.Fatalf("run() exit code = %d, want 0", code)
	}
	if got, want := stdout.String(), "up: uptime monitor TUI (not implemented yet)\n"; got != want {
		t.Fatalf("run() stdout = %q, want %q", got, want)
	}
}
