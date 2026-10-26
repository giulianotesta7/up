package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// databaseFlag shares the resolved help default between serve and tui. A
// resolution error is deferred until parsing, so an explicit --db still works.
func databaseFlag(fs *flag.FlagSet) *string {
	path, _ := defaultDatabasePath()
	return fs.String("db", path, "path to the SQLite database (created if missing)")
}

func defaultDatabasePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot find the config directory (set --db): %w", err)
	}
	return filepath.Join(dir, "up", "up.db"), nil
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
	resolved, err := defaultDatabasePath()
	if err != nil {
		return err
	}
	*path = resolved
	if err := os.MkdirAll(filepath.Dir(resolved), 0700); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	return nil
}
