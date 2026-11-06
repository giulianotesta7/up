package tui_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/remote"
	"github.com/giulianotesta7/up/internal/store"
	"github.com/giulianotesta7/up/internal/tui"
)

// BenchmarkLoadWithIncidents loads the snapshot of 30 monitors with 2000
// results each over 30 days (3% down, in short runs) and 600 delivery
// attempts each, as the TUI does on every refresh. It reports the size of the
// remote snapshot payload too.
func BenchmarkLoadWithIncidents(b *testing.B) {
	const (
		monitors = 30
		results  = 2000
		attempts = 600
		period   = 30 * 24 * time.Hour
	)
	ctx := context.Background()
	s, err := store.Open(filepath.Join(b.TempDir(), "up.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	first := now.Add(-period)
	step := period / results
	for m := range monitors {
		name := fmt.Sprintf("m%02d", m)
		c := config.Check{Name: name, URL: "https://" + name + ".example.com", Interval: time.Minute, Timeout: 5 * time.Second, ExpectedStatus: 200}
		if err := s.AddCheck(ctx, c); err != nil {
			b.Fatal(err)
		}
		for i := range results {
			r := check.Result{CheckName: name, At: first.Add(time.Duration(i) * step), Status: check.StatusUp, StatusCode: 200, Latency: 40 * time.Millisecond}
			// Runs of 3 down results every 100: 3% down.
			if (i+m)%100 < 3 {
				r.Status, r.StatusCode, r.Error = check.StatusDown, 503, "unexpected status 503 (expected 200)"
			}
			if err := s.Record(ctx, r); err != nil {
				b.Fatal(err)
			}
		}
		astep := period / attempts
		for i := range attempts {
			a := store.Attempt{At: first.Add(time.Duration(i) * astep), Monitor: name, Channel: fmt.Sprintf("ch%d", i%2), Event: store.EventDown, Outcome: store.OutcomeSent}
			if i%3 == 1 {
				a.Event = store.EventRecovered
			}
			if i%7 == 0 {
				a.Outcome, a.Reason = store.OutcomeFailed, "connection refused"
			}
			if err := s.RecordAttempt(ctx, a); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := s.Beat(ctx, now); err != nil {
		b.Fatal(err)
	}
	var snap tui.Snapshot
	for b.Loop() {
		if snap, err = tui.Load(ctx, s, now); err != nil {
			b.Fatal(err)
		}
	}
	payload, err := json.Marshal(remote.NewSnapshotDTO(snap, now))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(len(payload)), "payload-bytes")
}
