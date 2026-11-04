package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRetentionEnvironmentRejectedBeforeFiles(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"broken", "up: UP_RETENTION: invalid duration \"broken\"\n"},
		{"-1h", "up: UP_RETENTION must not be negative\n"},
	} {
		t.Run(tc.value, func(t *testing.T) {
			isolateDefaultDatabase(t)
			clearAPIEnv(t)
			t.Setenv("UP_RETENTION", tc.value)
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			code, out, errout := runCmd(ctx, "serve", "--db", filepath.Join(dir, "up.db"))
			if code != 1 || out != "" || errout != tc.want {
				t.Errorf("got %d %q %q; want 1 %q", code, out, errout, tc.want)
			}
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
				t.Errorf("changed files: %v %v", entries, err)
			}
		})
	}
}
