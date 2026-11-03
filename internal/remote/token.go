// Package remote is the optional authenticated HTTP API of `up serve`.
package remote

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	tokenPrefix = "up_"
	hashPrefix  = "sha256:"
)

// ErrNoToken means no access token has been created yet.
var ErrNoToken = errors.New("no access token has been created")

// GenerateToken returns a new access token: "up_" plus 256 random bits. The
// prefix lets secret scanners recognise a leaked token.
func GenerateToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate access token: %w", err)
	}
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// SaveToken stores only the SHA-256 hash of token at path, replacing any
// previous token atomically, so the old token stops working at once and a
// reader never sees a partial file. The file is private to the owner.
func SaveToken(path, token string) error {
	sum := sha256.Sum256([]byte(token))
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("save access token: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("save access token: %w", err)
	}
	if _, err := tmp.WriteString(hashPrefix + hex.EncodeToString(sum[:]) + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("save access token: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("save access token: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save access token: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("save access token: %w", err)
	}
	return nil
}

// Verifier checks tokens against the stored hash.
type Verifier struct{ hash [sha256.Size]byte }

// LoadVerifier reads the token hash at path. Like the key file, it refuses a
// file readable or writable by group or others.
func LoadVerifier(path string) (*Verifier, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNoToken, path)
	}
	if err != nil {
		return nil, fmt.Errorf("read access token: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("access token file %s is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("access token file %s has unsafe permissions %#o; run chmod 600", path, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read access token: %w", err)
	}
	text := strings.TrimSpace(string(data))
	raw, err := hex.DecodeString(strings.TrimPrefix(text, hashPrefix))
	if !strings.HasPrefix(text, hashPrefix) || err != nil || len(raw) != sha256.Size {
		return nil, fmt.Errorf("access token file %s is corrupt; run `up token reset`", path)
	}
	v := &Verifier{}
	copy(v.hash[:], raw)
	return v, nil
}

// Verify reports whether token matches, in constant time.
func (v *Verifier) Verify(token string) bool {
	sum := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(sum[:], v.hash[:]) == 1
}
