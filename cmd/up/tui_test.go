package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
	"github.com/giulianotesta7/up/internal/tui"
)

func TestTUICreationHelp(t *testing.T) {
	isolateDefaultDatabase(t)
	t.Chdir(t.TempDir())
	code, out, errOut := runCmd(context.Background(), "tui", "--help")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	for _, want := range []string{
		"n new monitor", "tab/shift+tab", "enter save", "esc cancel", "ctrl+c quit",
		"30s", "5s", "200", "created if missing", "edit cannot rename", "delete removes history",
		"e edit", "d delete", "y delete", "n cancel",
		"Keys: ↑/↓ or k/j select · enter details · n new monitor · e edit · d delete · s SMTP servers · c channels · a toggle animation · esc back · q/ctrl+c quit",
		"Manage SMTP servers and channels for email alerts with s and c; choose a monitor's channel in its form.",
		"SMTP passwords are encrypted with the key that 'up serve' keeps in <db>.key; run it once before entering a password, and back up that file with the database.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("help missing %q: %s", want, out)
		}
	}
	if _, err := os.Stat("up.db"); !os.IsNotExist(err) {
		t.Fatalf("help created DB: %v", err)
	}
}

func TestTUIReadWriteFirstUseAndProgramErrors(t *testing.T) {
	isolateDefaultDatabase(t)
	for _, emptyFile := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "empty"}[emptyFile], func(t *testing.T) {
			t.Chdir(t.TempDir())
			if emptyFile {
				if err := os.WriteFile("up.db", nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			previous := runTUIProgram
			t.Cleanup(func() { runTUIProgram = previous })
			runTUIProgram = func(ctx context.Context, s *store.Store, _ ...tui.Options) error {
				if err := s.AddCheck(ctx, config.Check{
					Name: "first", URL: "https://example.com", Interval: 30 * time.Second,
					Timeout: 5 * time.Second, ExpectedStatus: 200,
				}); err != nil {
					t.Fatal(err)
				}
				return errors.New("terminal unavailable")
			}
			code, out, errOut := runCmd(context.Background(), "tui", "--db", "up.db")
			if code != 1 || out != "" || errOut != "up: terminal unavailable\n" {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
			}
			s, err := store.OpenReadOnly("up.db")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			checks, err := s.Checks(context.Background())
			if err != nil || len(checks) != 1 || checks[0].Name != "first" {
				t.Fatalf("persisted checks=%v err=%v", checks, err)
			}
		})
	}
}

func TestTUIInvalidDatabasesRemainClear(t *testing.T) {
	isolateDefaultDatabase(t)
	for _, kind := range []string{"corrupt", "incompatible", "uninitialized incompatible"} {
		t.Run(kind, func(t *testing.T) {
			t.Chdir(t.TempDir())
			path := filepath.Join(t.TempDir(), "up.db")
			want := ""
			if kind == "corrupt" {
				if err := os.WriteFile(path, []byte("not SQLite"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "not a database"
			} else {
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				query := "PRAGMA user_version=99"
				want = "schema version 99 is newer"
				if kind == "uninitialized incompatible" {
					query = "CREATE TABLE checks (other TEXT)"
					want = "create schema"
				}
				_, err = db.Exec(query)
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			previous := runTUIProgram
			t.Cleanup(func() { runTUIProgram = previous })
			runTUIProgram = func(context.Context, *store.Store, ...tui.Options) error {
				t.Fatal("UI started for invalid database")
				return nil
			}
			code, out, errOut := runCmd(context.Background(), "tui", "--db", path)
			if code != 1 || out != "" || !strings.HasPrefix(errOut, "up: store: open "+path+": ") || !strings.Contains(errOut, want) {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
			}
		})
	}
}
