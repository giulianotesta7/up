package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

var systemDatabasePath = "/var/lib/up/up.db"

// databaseFlag shares the resolved help default between serve and tui. A
// resolution error is deferred until parsing, so an explicit --db still works.
func databaseFlag(fs *flag.FlagSet) *string {
	path, _, _ := defaultDatabasePath()
	return fs.String("db", path, "path to the SQLite database (created if missing)")
}

// defaultDatabasePath also reports whether this is the user-config fallback,
// the only choice whose parent directory we create.
func defaultDatabasePath() (path string, createParent bool, err error) {
	if path := os.Getenv("UP_DB"); path != "" {
		return path, false, nil
	}
	if info, err := os.Stat(systemDatabasePath); err == nil && info.Mode().IsRegular() {
		return systemDatabasePath, false, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", false, fmt.Errorf("cannot find the config directory (set --db): %w", err)
	}
	return filepath.Join(dir, "up", "up.db"), true, nil
}

func prepareDatabase(fs *flag.FlagSet, path *string) error {
	explicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "db" {
			explicit = true
		}
	})
	if explicit {
		return nil
	}
	resolved, createParent, err := defaultDatabasePath()
	if err != nil {
		return err
	}
	*path = resolved
	if !createParent {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0700); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	return nil
}
