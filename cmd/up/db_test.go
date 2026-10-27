package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/store"
	"github.com/giulianotesta7/up/internal/tui"
)

func isolateDefaultDatabase(t *testing.T) {
	t.Helper()
	t.Setenv("UP_DB", "")
	previous := systemDatabasePath
	systemDatabasePath = filepath.Join(t.TempDir(), "system.db")
	t.Cleanup(func() { systemDatabasePath = previous })
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func TestDatabaseResolutionPrecedence(t *testing.T) {
	for _, command := range []string{"serve", "tui"} {
		for _, branch := range []string{"explicit", "environment", "system", "directory", "missing", "environment missing parent"} {
			t.Run(command+"/"+branch, func(t *testing.T) {
				isolateDefaultDatabase(t)
				config := os.Getenv("XDG_CONFIG_HOME")
				systemDatabasePath = filepath.Join(t.TempDir(), "system.db")
				if branch == "directory" {
					if err := os.Mkdir(systemDatabasePath, 0700); err != nil {
						t.Fatal(err)
					}
				} else if branch != "missing" {
					if err := os.WriteFile(systemDatabasePath, nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
				want := filepath.Join(config, "up", "up.db")
				args := []string{command}
				switch branch {
				case "explicit", "environment", "environment missing parent":
					env := filepath.Join(t.TempDir(), "env.db")
					if branch == "environment missing parent" {
						env = filepath.Join(t.TempDir(), "missing", "up.db")
					}
					t.Setenv("UP_DB", env)
					want = env
					if branch == "explicit" {
						want = filepath.Join(t.TempDir(), "explicit.db")
						args = append(args, "--db", want)
					}
				case "system":
					want = systemDatabasePath
				}
				previous := runTUIProgram
				t.Cleanup(func() { runTUIProgram = previous })
				runTUIProgram = func(_ context.Context, _ *store.Store, options ...tui.Options) error {
					if len(options) != 1 || options[0].DatabasePath != want {
						t.Fatalf("TUI database options: %+v", options)
					}
					return nil
				}
				ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
				defer cancel()
				code, out, errOut := runCmd(ctx, args...)
				if branch == "environment missing parent" {
					if code != 1 || out != "" || errOut != "up: store: open "+want+": unable to open database file (14)\n" {
						t.Fatalf("got %d %q %q", code, out, errOut)
					}
					if _, err := os.Stat(filepath.Dir(want)); !os.IsNotExist(err) {
						t.Fatal("created environment parent")
					}
				} else {
					if code != 0 || out != "" || (command == "tui" && errOut != "") {
						t.Fatalf("got %d %q %q", code, out, errOut)
					}
					s, err := store.OpenReadOnly(want)
					if err != nil {
						t.Fatal(err)
					}
					s.Close()
				}
				if branch != "directory" && branch != "missing" {
					if _, err := os.Stat(filepath.Join(config, "up")); !os.IsNotExist(err) {
						t.Fatal("created fallback directory")
					}
				}
				helpPath := want
				if branch == "explicit" {
					helpPath = os.Getenv("UP_DB")
				}
				code, out, errOut = runCmd(context.Background(), command, "-h")
				if code != 0 || errOut != "" || !strings.Contains(out, "(default \""+helpPath+"\")") {
					t.Fatalf("help: %d %q %q", code, out, errOut)
				}
			})
		}
	}
}

func TestDatabaseDefaults(t *testing.T) {
	for _, command := range []string{"serve", "tui"} {
		t.Run(command, func(t *testing.T) {
			isolateDefaultDatabase(t)
			config := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", config)
			t.Setenv("HOME", t.TempDir())
			t.Chdir(t.TempDir())
			previous := runTUIProgram
			t.Cleanup(func() { runTUIProgram = previous })
			runTUIProgram = func(context.Context, *store.Store, ...tui.Options) error { return nil }
			ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
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
	isolateDefaultDatabase(t)
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
	isolateDefaultDatabase(t)
	for _, command := range []string{"serve", "tui"} {
		t.Run(command, func(t *testing.T) {
			t.Setenv("HOME", "")
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Chdir(t.TempDir())
			_, err := os.UserConfigDir()
			if err == nil {
				t.Skip("platform does not require HOME")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
			defer cancel()
			previous := runTUIProgram
			t.Cleanup(func() { runTUIProgram = previous })
			runTUIProgram = func(context.Context, *store.Store, ...tui.Options) error { return nil }
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
