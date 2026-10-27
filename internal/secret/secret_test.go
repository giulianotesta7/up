package secret_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/giulianotesta7/up/internal/secret"
)

func newKey(t *testing.T) *secret.Key {
	t.Helper()
	k, _, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealAndOpen(t *testing.T) {
	k := newKey(t)
	s, err := secret.Seal(k.PublicKey(), "password")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s.Key, k.PublicKey()) {
		t.Fatal("wrong public key")
	}
	got, err := k.Open(s)
	if err != nil || got != "password" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if _, err := newKey(t).Open(s); !errors.Is(err, secret.ErrWrongKey) {
		t.Fatalf("wrong key: %v", err)
	}
	s.Data[len(s.Data)-1] ^= 1
	if _, err := k.Open(s); err == nil || errors.Is(err, secret.ErrWrongKey) {
		t.Fatalf("tamper: %v", err)
	}
	if _, err := secret.Seal([]byte("bad"), "password"); err == nil {
		t.Fatal("accepted malformed public key")
	}
	empty, err := secret.Seal(nil, "")
	if err != nil || !empty.Empty() {
		t.Fatalf("empty seal: %#v, %v", empty, err)
	}
	if got, err := k.Open(empty); got != "" || err != nil {
		t.Fatalf("empty open: %q, %v", got, err)
	}
}

func TestKeyLifecycle(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	path := filepath.Join(t.TempDir(), "key")
	k, created, err := secret.LoadOrCreate(path)
	if err != nil || !created {
		t.Fatalf("create: %v, %v", created, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode: %v, %v", info, err)
	}
	reloaded, created, err := secret.LoadOrCreate(path)
	if err != nil || created || !bytes.Equal(k.PublicKey(), reloaded.PublicKey()) {
		t.Fatalf("reload: %v, %v", created, err)
	}
	s, err := secret.Seal(k.PublicKey(), "password")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	replacement, created, err := secret.LoadOrCreate(path)
	if err != nil || !created {
		t.Fatalf("replacement: %v, %v", created, err)
	}
	if _, err := replacement.Open(s); !errors.Is(err, secret.ErrWrongKey) {
		t.Fatalf("lost key: %v", err)
	}
}

func TestRejectInsecurePermissions(t *testing.T) {
	for _, mode := range []os.FileMode{0640, 0604} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "key")
			content := []byte("unchanged")
			if err := os.WriteFile(path, content, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			_, created, err := secret.LoadOrCreate(path)
			want := fmt.Sprintf("key file %s is accessible by group or others (run: chmod 600 %s)", path, path)
			if err == nil || err.Error() != want || created {
				t.Fatalf("got %v, %v; want %q", created, err, want)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, content) {
				t.Fatalf("content changed: %q, %v", got, err)
			}
		})
	}
}

func TestInvalidKeyFiles(t *testing.T) {
	for _, content := range []string{"wrong\nYWJj\n", "up-secret-key-v1\n!\n", "up-secret-key-v1\nYWJj\n"} {
		t.Run(strings.ReplaceAll(content, "\n", "_"), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "key")
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			_, created, err := secret.LoadOrCreate(path)
			if err == nil || created || !strings.HasPrefix(err.Error(), "secret: ") || !strings.Contains(err.Error(), path) {
				t.Fatalf("invalid file: %v, %v", created, err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "missing", "key")
	if _, _, err := secret.LoadOrCreate(path); err == nil || !strings.HasPrefix(err.Error(), "secret: ") {
		t.Fatalf("missing parent: %v", err)
	}
	dir := t.TempDir()
	if _, _, err := secret.LoadOrCreate(dir); err == nil || !strings.HasPrefix(err.Error(), "secret: ") {
		t.Fatalf("directory: %v", err)
	}
}

func TestLoadOrCreateConcurrentCreatorsShareOneKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "up.db.key")
	const n = 16
	keys := make([][]byte, n)
	created := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			k, c, err := secret.LoadOrCreate(path)
			if err == nil {
				keys[i] = k.PublicKey()
			}
			created[i], errs[i] = c, err
		})
	}
	wg.Wait()
	creators := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("LoadOrCreate #%d error = %v", i, errs[i])
		}
		if !bytes.Equal(keys[i], keys[0]) {
			t.Fatalf("LoadOrCreate #%d returned a different key", i)
		}
		if created[i] {
			creators++
		}
	}
	if creators != 1 {
		t.Fatalf("%d callers reported creating the key, want 1", creators)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory entries = %v, %v, want only the key file", entries, err)
	}
}
