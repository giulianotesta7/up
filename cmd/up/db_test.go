package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/store"
)

func isolateDefaultDatabase(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func TestDatabaseDefaults(t *testing.T) {
	for _, command := range []string{"serve", "tui"} {
		t.Run(command, func(t *testing.T) {
			config := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", config)
			t.Setenv("HOME", t.TempDir())
			t.Chdir(t.TempDir())
			previous := runTUIProgram
			t.Cleanup(func() { runTUIProgram = previous })
			runTUIProgram = func(context.Context, *store.Store) error { return nil }
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			code, out, errOut := runCmd(ctx, command)
			if code != 0 || out != "" || (command == "tui" && errOut != "") {
				t.Fatalf("got %d %q %q", code, out, errOut)
			}
			path := filepath.Join(config, "up", "up.db")
			s, err := store.OpenReadOnly(path)
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
			info, err := os.Stat(filepath.Dir(path))
			if err != nil || info.Mode().Perm() != 0700 {
				t.Fatalf("directory: %v %v", info, err)
			}
			code, out, errOut = runCmd(context.Background(), command, "-h")
			if code != 0 || errOut != "" || !strings.Contains(out, "(default \""+path+"\")") {
				t.Fatalf("help: %d %q %q", code, out, errOut)
			}
			if _, err := os.Stat("up.db"); !os.IsNotExist(err) {
				t.Fatal("created local database")
			}
		})
	}
}

func TestExplicitDatabaseDoesNotCreateParent(t *testing.T) {
	for _, command := range []string{"serve", "tui"} {
		t.Run(command, func(t *testing.T) {
			t.Setenv("HOME", "")
			t.Setenv("XDG_CONFIG_HOME", "")
			parent := filepath.Join(t.TempDir(), "missing")
			code, out, errOut := runCmd(context.Background(), command, "--db", filepath.Join(parent, "up.db"))
			if code != 1 || out != "" || errOut != "up: store: open "+filepath.Join(parent, "up.db")+": unable to open database file (14)\n" {
				t.Fatalf("got %d %q %q", code, out, errOut)
			}
			if _, err := os.Stat(parent); !os.IsNotExist(err) {
				t.Fatal("created explicit parent")
			}
		})
	}
}

func TestDatabaseConfigDirectoryFailure(t *testing.T) {
	for _, command := range []string{"serve", "tui"} {
		t.Run(command, func(t *testing.T) {
			t.Setenv("HOME", "")
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Chdir(t.TempDir())
			_, err := os.UserConfigDir()
			if err == nil {
				t.Skip("platform does not require HOME")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			previous := runTUIProgram
			t.Cleanup(func() { runTUIProgram = previous })
			runTUIProgram = func(context.Context, *store.Store) error { return nil }
			code, out, errOut := runCmd(ctx, command)
			want := "up: cannot find the config directory (set --db): " + err.Error() + "\n"
			if code != 1 || out != "" || errOut != want {
				t.Fatalf("got %d %q %q; want %q", code, out, errOut, want)
			}
			entries, _ := os.ReadDir(".")
			if len(entries) != 0 {
				t.Fatal("created files on failure")
			}
		})
	}
}
