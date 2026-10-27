package secret

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hpke"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const keyHeader = "up-secret-key-v1"
const info = "up smtp password v1"

// Key is the daemon's private key.
type Key struct{ private hpke.PrivateKey }

// ErrWrongKey indicates that a password was sealed to another key.
var ErrWrongKey = errors.New("secret: wrong key")

// LoadOrCreate reads a private key or creates it when missing.
func LoadOrCreate(path string) (*Key, bool, error) {
	k, err := load(path)
	if err == nil {
		return k, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	private, err := hpke.DHKEM(ecdh.X25519()).GenerateKey()
	if err != nil {
		return nil, false, fmt.Errorf("secret: generate key: %w", err)
	}
	data, err := private.Bytes()
	if err != nil {
		return nil, false, fmt.Errorf("secret: encode key: %w", err)
	}
	// Write a complete temporary file, then link it into place: the link
	// fails if the key appeared meanwhile, and nobody ever sees a partial key.
	f, err := os.CreateTemp(filepath.Dir(path), ".up-key-*")
	if err != nil {
		return nil, false, fmt.Errorf("secret: create key %s: %w", path, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	writeErr := f.Chmod(0600)
	if writeErr == nil {
		_, writeErr = io.WriteString(f, keyHeader+"\n"+base64.StdEncoding.EncodeToString(data)+"\n")
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if closeErr := f.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return nil, false, fmt.Errorf("secret: write key %s: %w", path, writeErr)
	}
	if err := os.Link(tmp, path); errors.Is(err, os.ErrExist) {
		k, err := load(path)
		return k, false, err
	} else if err != nil {
		return nil, false, fmt.Errorf("secret: create key %s: %w", path, err)
	}
	return &Key{private: private}, true, nil
}

func load(path string) (*Key, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("secret: stat key %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("secret: key file %s is not regular", path)
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("key file %s is accessible by group or others (run: chmod 600 %s)", path, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("secret: open key %s: %w", path, err)
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, fmt.Errorf("secret: stat key %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("secret: key file %s is not regular", path)
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("key file %s is accessible by group or others (run: chmod 600 %s)", path, path)
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("secret: read key %s: %w", path, err)
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) != 3 || lines[0] != keyHeader || lines[2] != "" {
		return nil, fmt.Errorf("secret: invalid key file %s", path)
	}
	data, err := base64.StdEncoding.Strict().DecodeString(lines[1])
	if err != nil {
		return nil, fmt.Errorf("secret: invalid key file %s: %w", path, err)
	}
	private, err := hpke.DHKEM(ecdh.X25519()).NewPrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("secret: invalid key file %s: %w", path, err)
	}
	return &Key{private: private}, nil
}

// PublicKey returns the public key bytes stored in the database.
func (k *Key) PublicKey() []byte { return k.private.PublicKey().Bytes() }

// Seal encrypts plaintext to publicKey without using the private key.
func Seal(publicKey []byte, plaintext string) (Sealed, error) {
	if plaintext == "" {
		return Sealed{}, nil
	}
	pub, err := hpke.DHKEM(ecdh.X25519()).NewPublicKey(publicKey)
	if err != nil {
		return Sealed{}, fmt.Errorf("secret: invalid public key: %w", err)
	}
	data, err := hpke.Seal(pub, hpke.HKDFSHA256(), hpke.AES256GCM(), []byte(info), []byte(plaintext))
	if err != nil {
		return Sealed{}, fmt.Errorf("secret: seal: %w", err)
	}
	return Sealed{Key: bytes.Clone(publicKey), Data: data}, nil
}

// Open decrypts s or returns ErrWrongKey for a different public key.
func (k *Key) Open(s Sealed) (string, error) {
	if s.Empty() {
		return "", nil
	}
	if !bytes.Equal(s.Key, k.PublicKey()) {
		return "", ErrWrongKey
	}
	data, err := hpke.Open(k.private, hpke.HKDFSHA256(), hpke.AES256GCM(), []byte(info), s.Data)
	if err != nil {
		return "", fmt.Errorf("secret: open: %w", err)
	}
	return string(data), nil
}
