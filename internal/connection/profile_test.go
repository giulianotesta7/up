package connection

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileLifecycle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	path, err := ProfilePath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = LoadProfile(); !errors.Is(err, ErrNoProfile) {
		t.Fatalf("missing: %v", err)
	}
	if err = SaveProfile(Profile{ServerURL: "HTTPS://EXAMPLE.COM/", Token: "up_secret"}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProfile()
	if err != nil || got != (Profile{ServerURL: "https://example.com", Token: "up_secret"}) {
		t.Fatalf("load: %+v %v", got, err)
	}
	for p, mode := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
		info, e := os.Stat(p)
		if e != nil || info.Mode().Perm() != mode {
			t.Fatalf("mode %s: %v %v", p, info, e)
		}
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err = SaveProfile(got); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatal(entries)
	}
	other := filepath.Join(filepath.Dir(path), "other")
	if err = os.WriteFile(other, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, want := range []bool{true, false} {
		ok, e := DeleteProfile()
		if e != nil || ok != want {
			t.Fatalf("delete: %v %v", ok, e)
		}
	}
	if _, err = os.Stat(other); err != nil {
		t.Fatal(err)
	}
}

func TestProfileAtomicFailurePreservesOldFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	old := Profile{ServerURL: "https://example.com", Token: "up_old_secret"}
	if err := SaveProfile(old); err != nil {
		t.Fatal(err)
	}
	path, _ := ProfilePath()
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	err := SaveProfile(Profile{ServerURL: "https://example.org", Token: "up_new_secret"})
	if err == nil {
		t.Skip("directory permissions are not enforced for this user")
	}
	if strings.Contains(err.Error(), "up_new_secret") {
		t.Fatalf("token leaked: %v", err)
	}
	got, err := LoadProfile()
	if err != nil || got != old {
		t.Fatalf("old profile: %+v %v", got, err)
	}
}

func TestProfileRejectsUnsafeAndInvalid(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	path, err := ProfilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	const secret = "up_super_secret"
	cases := []struct {
		name, data string
		mode       os.FileMode
		exact      string
	}{
		{"readable", `{"server_url":"https://example.com","token":"up_super_secret"}`, 0644, fmt.Sprintf("saved remote file %s has unsafe permissions 0644; run chmod 600", path)},
		{"writable", `{}`, 0620, fmt.Sprintf("saved remote file %s has unsafe permissions 0620; run chmod 600", path)},
		{"unknown", `{"server_url":"https://example.com","token":"up_super_secret","extra":true}`, 0600, ""},
		{"trailing", `{"server_url":"https://example.com","token":"up_super_secret"} {}`, 0600, ""},
		{"url", `{"server_url":"wrong","token":"up_super_secret"}`, 0600, ""},
		{"token", `{"server_url":"https://example.com","token":""}`, 0600, ""},
		{"empty", ``, 0600, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			_, err := LoadProfile()
			if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), path) || (tc.exact != "" && err.Error() != tc.exact) {
				t.Fatalf("error: %v", err)
			}
		})
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfile(); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("directory: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(path), "target")
	if err := os.WriteFile(target, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfile(); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("symlink: %v", err)
	}
}
