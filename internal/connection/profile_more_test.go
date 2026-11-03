package connection

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const secretToken = "up_SECRET-TOKEN-VALUE-0123456789abcdefghijklmnopq"

func isolateConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", t.TempDir())
	return filepath.Join(dir, "up")
}

func TestSaveCreatesPrivateDirectoryAndFile(t *testing.T) {
	dir := isolateConfig(t)
	if err := SaveProfile(Profile{ServerURL: "HTTPS://Up.Acme.com/", Token: secretToken}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory: %v %v", info, err)
	}
	path := filepath.Join(dir, "remote.json")
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", info, err)
	}
	got, err := LoadProfile()
	if err != nil || got.ServerURL != "https://up.acme.com" || got.Token != secretToken {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}

func TestSaveOverAWiderFileEndsPrivateAndLeavesNothingElse(t *testing.T) {
	dir := isolateConfig(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "remote.json")
	if err := os.WriteFile(path, []byte(`{"server_url":"https://old.example.com","token":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveProfile(Profile{ServerURL: "https://new.example.com", Token: secretToken}); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode after save over a 0644 file: %v", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "remote.json" {
		t.Errorf("leftover files: %v", entries)
	}
}

func TestInvalidFilesAreRejectedWithoutEchoingTheToken(t *testing.T) {
	dir := isolateConfig(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "remote.json")
	good := `{"server_url":"https://up.acme.com","token":"` + secretToken + `"}`
	for label, tc := range map[string]struct {
		content string
		mode    os.FileMode
		want    string
	}{
		"group readable":  {good, 0o640, "has unsafe permissions 0640; run chmod 600"},
		"world readable":  {good, 0o644, "has unsafe permissions 0644; run chmod 600"},
		"group writable":  {good, 0o620, "has unsafe permissions 0620; run chmod 600"},
		"unknown field":   {`{"server_url":"https://up.acme.com","token":"` + secretToken + `","admin":true}`, 0o600, "invalid JSON"},
		"trailing data":   {good + `{"x":1}`, 0o600, "invalid JSON"},
		"not json":        {`token=` + secretToken, 0o600, "invalid JSON"},
		"empty file":      {``, 0o600, "invalid JSON"},
		"http scheme bad": {`{"server_url":"ftp://up.acme.com","token":"` + secretToken + `"}`, 0o600, "invalid URL"},
		"url credentials": {`{"server_url":"https://u:` + secretToken + `@up.acme.com","token":"x"}`, 0o600, "invalid URL"},
		"empty token":     {`{"server_url":"https://up.acme.com","token":"  "}`, 0o600, "empty token"},
		"missing token":   {`{"server_url":"https://up.acme.com"}`, 0o600, "empty token"},
	} {
		_ = os.Remove(path)
		if err := os.WriteFile(path, []byte(tc.content), tc.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, tc.mode); err != nil {
			t.Fatal(err)
		}
		_, err := LoadProfile()
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), path) {
			t.Errorf("%s: got %v, want it to name the file and contain %q", label, err, tc.want)
			continue
		}
		if strings.Contains(err.Error(), secretToken) || strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: error leaks the token: %v", label, err)
		}
	}
	// A directory and a symlink are refused too, and the symlink target is never read.
	_ = os.Remove(path)
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(target, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfile(); err == nil || !strings.Contains(err.Error(), "not a regular file") || strings.Contains(err.Error(), secretToken) {
		t.Errorf("symlink: %v", err)
	}
	_ = os.Remove(path)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfile(); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("directory: %v", err)
	}
}

func TestSaveRejectsBadInputWithoutWritingAnything(t *testing.T) {
	dir := isolateConfig(t)
	for label, p := range map[string]Profile{
		"bad url":     {ServerURL: "nope", Token: secretToken},
		"empty token": {ServerURL: "https://up.acme.com", Token: " "},
		"credentials": {ServerURL: "https://u:p@up.acme.com", Token: secretToken},
	} {
		err := SaveProfile(p)
		if err == nil || strings.Contains(err.Error(), secretToken) {
			t.Errorf("%s: %v", label, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "remote.json")); !os.IsNotExist(err) {
		t.Errorf("a rejected save created the file: %v", err)
	}
}

func TestDeleteRemovesOnlyTheProfile(t *testing.T) {
	dir := isolateConfig(t)
	if removed, err := DeleteProfile(); removed || err != nil {
		t.Fatalf("nothing saved: %v %v", removed, err)
	}
	if err := SaveProfile(Profile{ServerURL: "https://up.acme.com", Token: secretToken}); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "up.db")
	if err := os.WriteFile(other, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if removed, err := DeleteProfile(); !removed || err != nil {
		t.Fatalf("delete: %v %v", removed, err)
	}
	if _, err := LoadProfile(); err != ErrNoProfile {
		t.Errorf("after delete: %v", err)
	}
	if data, err := os.ReadFile(other); err != nil || string(data) != "db" {
		t.Errorf("an unrelated file was touched: %q %v", data, err)
	}
}
