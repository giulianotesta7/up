package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/connection"
	"github.com/heohlee/up/internal/store"
	"github.com/heohlee/up/internal/tui"
)

const remoteSecret = "up_REMOTE-SECRET-0123456789abcdefghijklmnopqrs"

type runners struct{ local, remote int }

func stubRunners(t *testing.T) *runners {
	t.Helper()
	calls := &runners{}
	prevLocal, prevRemote := runTUIProgram, runRemoteTUIProgram
	t.Cleanup(func() { runTUIProgram, runRemoteTUIProgram = prevLocal, prevRemote })
	runTUIProgram = func(context.Context, *store.Store, ...tui.Options) error { calls.local++; return nil }
	runRemoteTUIProgram = func(context.Context, tui.Store, ...tui.Options) error { calls.remote++; return nil }
	return calls
}

// snapshotOfDirs lists every file under the isolated config and home folders.
func snapshotOfDirs(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, root := range []string{os.Getenv("XDG_CONFIG_HOME"), os.Getenv("HOME")} {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err == nil {
				files = append(files, path)
			}
			return nil
		})
	}
	return files
}

func TestRemoteTUIRefusesAnUnsafeOrCorruptProfile(t *testing.T) {
	for name, prepare := range map[string]func(path string){
		"world readable": func(path string) { _ = os.Chmod(path, 0o644) },
		"corrupt":        func(path string) { _ = os.WriteFile(path, []byte("token="+remoteSecret), 0o600) },
	} {
		isolateDefaultDatabase(t)
		t.Setenv("UP_REMOTE", "")
		calls := stubRunners(t)
		if err := connection.SaveProfile(connection.Profile{ServerURL: "https://up.acme.com", Token: remoteSecret}); err != nil {
			t.Fatal(err)
		}
		path, _ := connection.ProfilePath()
		prepare(path)
		code, out, errout := runCmd(context.Background(), "tui", "--remote")
		if code != 1 || out != "" || !strings.HasPrefix(errout, "up: saved remote file ") || strings.Contains(errout, remoteSecret) {
			t.Errorf("%s: got %d %q %q", name, code, out, errout)
		}
		if calls.local+calls.remote != 0 {
			t.Errorf("%s: a runner was called with a bad profile: %+v", name, calls)
		}
	}
}

func TestExplicitRemoteFalseBeatsTheEnvironment(t *testing.T) {
	isolateDefaultDatabase(t)
	t.Setenv("UP_REMOTE", "true")
	calls := stubRunners(t)
	if code, _, errout := runCmd(context.Background(), "tui", "--remote=false", "--db", filepath.Join(t.TempDir(), "up.db")); code != 0 || errout != "" {
		t.Fatalf("exit %d %q", code, errout)
	}
	if calls.local != 1 || calls.remote != 0 {
		t.Errorf("--remote=false must run the local TUI: %+v", calls)
	}
}

func TestASavedRemoteIsNeverUsedImplicitly(t *testing.T) {
	isolateDefaultDatabase(t)
	t.Setenv("UP_REMOTE", "")
	calls := stubRunners(t)
	if err := connection.SaveProfile(connection.Profile{ServerURL: "https://up.acme.com", Token: remoteSecret}); err != nil {
		t.Fatal(err)
	}
	if code, _, errout := runCmd(context.Background(), "tui", "--db", filepath.Join(t.TempDir(), "up.db")); code != 0 || errout != "" {
		t.Fatalf("exit %d %q", code, errout)
	}
	if calls.local != 1 || calls.remote != 0 {
		t.Errorf("without --remote the saved remote must not be used: %+v", calls)
	}
}

func TestRemoteModeLeavesNoLocalStateEvenWhenTheServerIsDown(t *testing.T) {
	isolateDefaultDatabase(t)
	t.Setenv("UP_REMOTE", "")
	downServer := httptest.NewServer(http.NotFoundHandler())
	address := downServer.URL
	downServer.Close()
	if err := connection.SaveProfile(connection.Profile{ServerURL: address, Token: remoteSecret}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UP_DB", filepath.Join(t.TempDir(), "should-never-exist.db"))
	before := snapshotOfDirs(t)
	var loadErr error
	calls := stubRunners(t)
	runRemoteTUIProgram = func(ctx context.Context, s tui.Store, _ ...tui.Options) error {
		calls.remote++
		_, loadErr = s.(tui.SnapshotLoader).LoadSnapshot(ctx, time.Now())
		return nil
	}
	code, out, errout := runCmd(context.Background(), "tui", "--remote")
	if code != 0 || out != "" || errout != "" || calls.remote != 1 || calls.local != 0 {
		t.Fatalf("exit %d %q %q calls=%+v", code, out, errout, calls)
	}
	if loadErr == nil || strings.Contains(loadErr.Error(), remoteSecret) {
		t.Errorf("the unreachable server must give a clean error: %v", loadErr)
	}
	after := snapshotOfDirs(t)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("remote mode changed local files:\nbefore %v\nafter  %v", before, after)
	}
	if _, err := os.Stat(os.Getenv("UP_DB")); !os.IsNotExist(err) {
		t.Errorf("UP_DB must be ignored in remote mode: %v", err)
	}
}

func TestRemoteRunnerGetsTheSavedDestination(t *testing.T) {
	isolateDefaultDatabase(t)
	t.Setenv("UP_REMOTE", "true")
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"server_time":"2026-01-01T00:00:00Z"}`))
	}))
	defer server.Close()
	if err := connection.SaveProfile(connection.Profile{ServerURL: server.URL + "/", Token: remoteSecret}); err != nil {
		t.Fatal(err)
	}
	stubRunners(t)
	var options tui.Options
	runRemoteTUIProgram = func(ctx context.Context, s tui.Store, o ...tui.Options) error {
		options = o[0]
		_, err := s.(tui.SnapshotLoader).LoadSnapshot(ctx, time.Now())
		return err
	}
	if code, _, errout := runCmd(context.Background(), "tui"); code != 0 || errout != "" {
		t.Fatalf("exit %d %q", code, errout)
	}
	if options.Destination != server.URL || options.Hostname != strings.TrimPrefix(server.URL, "http://") || auth != "Bearer "+remoteSecret {
		t.Errorf("options %+v auth %q", options, auth)
	}
}

func TestRemoteRunnerErrorExitsOneWithoutTheToken(t *testing.T) {
	isolateDefaultDatabase(t)
	t.Setenv("UP_REMOTE", "")
	if err := connection.SaveProfile(connection.Profile{ServerURL: "https://up.acme.com", Token: remoteSecret}); err != nil {
		t.Fatal(err)
	}
	stubRunners(t)
	runRemoteTUIProgram = func(context.Context, tui.Store, ...tui.Options) error { return context.DeadlineExceeded }
	code, out, errout := runCmd(context.Background(), "tui", "--remote")
	if code != 1 || out != "" || errout != "up: context deadline exceeded\n" {
		t.Errorf("got %d %q %q", code, out, errout)
	}
}
