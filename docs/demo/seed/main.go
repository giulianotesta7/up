// Command seed creates synthetic demo history in a new, disposable database.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

func main() {
	db := flag.String("db", "", "required path to a new demo database (existing files are refused)")
	wait := flag.Bool("wait-ready", false, "read-only: wait for the local server and a new daemon heartbeat in an existing demo database")
	flag.Parse()
	if *db == "" || flag.NArg() != 0 {
		log.Fatal("usage: seed --db PATH [--wait-ready]")
	}
	if *wait {
		if err := waitReady(*db); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := seed(*db); err != nil {
		log.Fatal(err)
	}
}

func seed(path string) error {
	// Reserve the path exclusively before opening SQLite, including against symlinks.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("refusing to overwrite database: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now()
	rng := rand.New(rand.NewSource(12))
	monitors := []struct{ name, url string }{
		{"api", "http://127.0.0.1:8099/api"},
		{"web", "https://example.com"},
		{"search", "http://127.0.0.1:8099/search"},
		{"payments", "http://127.0.0.1:8099/payments"},
		{"docs", "http://127.0.0.1:8099/docs"},
		{"github", "https://github.com"},
	}
	for _, m := range monitors {
		c := config.Check{Name: m.name, URL: m.url, Interval: 5 * time.Second, Timeout: 3 * time.Second, ExpectedStatus: 200}
		if err := s.AddCheck(ctx, c); err != nil {
			return err
		}
		// This history is synthetic demo data, not measured uptime.
		for i := range 28 {
			ms := 75 + rng.Intn(11)
			switch m.name {
			case "search":
				ms = 110 + rng.Intn(21)
				if i%6 == 5 {
					ms = 600
				}
			case "docs":
				ms = 400
			case "web", "github":
				ms = 60 + rng.Intn(141)
			}
			r := check.Result{CheckName: m.name, At: now.Add(-3*time.Second - time.Duration(27-i)*c.Interval), Status: check.StatusUp, Latency: time.Duration(ms) * time.Millisecond, StatusCode: 200}
			if m.name == "payments" && ((i >= 8 && i <= 10) || i >= 22) {
				r.Status = check.StatusDown
				r.StatusCode = 503
				r.Error = "unexpected status 503 (expected 200)"
			}
			if err := s.Record(ctx, r); err != nil {
				return err
			}
		}
	}
	return s.Beat(ctx, now)
}

func waitReady(path string) error {
	s, err := store.OpenReadOnly(path)
	if err != nil {
		return err
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	initial, _, err := s.LastBeat(ctx)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: time.Second}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		beat, ok, err := s.LastBeat(ctx)
		if err != nil {
			return err
		}
		if ok && beat.After(initial) && time.Since(beat) < 3*time.Second {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8099/api", nil)
			if err != nil {
				return err
			}
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for demo server and fresh daemon heartbeat: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
