package connection

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Profile is the saved remote destination and its access token.
type Profile struct {
	ServerURL string `json:"server_url"`
	Token     string `json:"token"`
}

var ErrNoProfile = errors.New("no saved remote")

// ProfilePath returns the location of the saved remote.
func ProfilePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return "", errors.New("cannot find the config directory")
	}
	return filepath.Join(dir, "up", "remote.json"), nil
}

// SaveProfile replaces the saved remote atomically with an owner-only file.
func SaveProfile(p Profile) error {
	path, err := ProfilePath()
	if err != nil {
		return err
	}
	u, err := NormalizeURL(p.ServerURL)
	if err != nil {
		return fmt.Errorf("saved remote file %s: invalid URL", path)
	}
	if strings.TrimSpace(p.Token) == "" {
		return fmt.Errorf("saved remote file %s: empty token", path)
	}
	p.ServerURL = u
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("save saved remote file %s: %w", path, err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), "remote.json.tmp-*")
	if err != nil {
		return fmt.Errorf("save saved remote file %s: %w", path, err)
	}
	defer os.Remove(f.Name())
	if err = json.NewEncoder(f).Encode(p); err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("save saved remote file %s: %w", path, err)
	}
	return nil
}

// LoadProfile reads a strictly validated owner-only saved remote.
func LoadProfile() (Profile, error) {
	var p Profile
	path, err := ProfilePath()
	if err != nil {
		return p, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return p, ErrNoProfile
	}
	if err != nil {
		return p, fmt.Errorf("read saved remote file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return p, fmt.Errorf("saved remote file %s is not a regular file", path)
	}
	if info.Mode().Perm()&0077 != 0 {
		return p, fmt.Errorf("saved remote file %s has unsafe permissions %#o; run chmod 600", path, info.Mode().Perm())
	}
	f, err := os.Open(path)
	if err != nil {
		return p, fmt.Errorf("read saved remote file %s: %w", path, err)
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err = d.Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("saved remote file %s has invalid JSON", path)
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return Profile{}, fmt.Errorf("saved remote file %s has invalid JSON", path)
	}
	p.ServerURL, err = NormalizeURL(p.ServerURL)
	if err != nil {
		return Profile{}, fmt.Errorf("saved remote file %s has invalid URL", path)
	}
	if strings.TrimSpace(p.Token) == "" {
		return Profile{}, fmt.Errorf("saved remote file %s has empty token", path)
	}
	return p, nil
}

// DeleteProfile removes only the saved remote.
func DeleteProfile() (bool, error) {
	path, err := ProfilePath()
	if err != nil {
		return false, err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("remove saved remote file %s: %w", path, err)
	}
	return true, nil
}
