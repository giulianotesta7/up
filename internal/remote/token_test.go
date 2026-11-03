package remote

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestGenerateFormat(t *testing.T) {
	a, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := GenerateToken()
	if a == b {
		t.Fatal("tokens must be unique")
	}
	if !regexp.MustCompile(`^up_[A-Za-z0-9_-]{43}$`).MatchString(a) {
		t.Fatalf("token format = %q", a)
	}
}

func TestSaveWritesOnlyTheHashWithPrivateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "up.db.token")
	token, _ := GenerateToken()
	if err := SaveToken(path, token); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err = %v", info.Mode().Perm(), err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), token) || strings.Contains(string(data), token[3:]) {
		t.Fatal("the token itself must never be stored")
	}
}

func TestLoadVerifies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "up.db.token")
	token, _ := GenerateToken()
	if err := SaveToken(path, token); err != nil {
		t.Fatal(err)
	}
	v, err := LoadVerifier(path)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Verify(token) {
		t.Error("correct token rejected")
	}
	for _, bad := range []string{"", "up_", token + "x", token[:len(token)-1], strings.ToUpper(token), "up_" + strings.Repeat("A", 43)} {
		if v.Verify(bad) {
			t.Errorf("wrong token %q accepted", bad)
		}
	}
}

func TestResetInvalidatesTheOldToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "up.db.token")
	old, _ := GenerateToken()
	if err := SaveToken(path, old); err != nil {
		t.Fatal(err)
	}
	fresh, _ := GenerateToken()
	if err := SaveToken(path, fresh); err != nil {
		t.Fatal(err)
	}
	v, _ := LoadVerifier(path)
	if v.Verify(old) || !v.Verify(fresh) {
		t.Error("reset must replace the old token")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode after reset = %v", info.Mode().Perm())
	}
}

func TestLoadMissingAndUnsafe(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadVerifier(filepath.Join(dir, "missing")); !errors.Is(err, ErrNoToken) {
		t.Errorf("missing: got %v, want ErrNoToken", err)
	}
	path := filepath.Join(dir, "up.db.token")
	token, _ := GenerateToken()
	if err := SaveToken(path, token); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadVerifier(path); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Errorf("group-readable file: got %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadVerifier(path); err == nil {
		t.Error("corrupt hash must be rejected")
	}
}
