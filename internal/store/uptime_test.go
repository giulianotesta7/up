package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
)

func TestUptimeWindow(t *testing.T) {
	for _, tc := range []struct {
		name         string
		rows         []check.Result
		up, observed time.Duration
		incidents    int
	}{
		{"empty", nil, 0, 0, 0},
		{"all down", []check.Result{result("api", 12, check.StatusDown)}, 0, 8 * time.Minute, 1},
		{"ongoing at since", []check.Result{result("api", 0, check.StatusUp), result("api", 5, check.StatusDown), result("api", 15, check.StatusUp)}, 5 * time.Minute, 10 * time.Minute, 1},
		{"inside", []check.Result{result("api", 10, check.StatusUp), result("api", 12, check.StatusDown), result("api", 14, check.StatusDown), result("api", 16, check.StatusUp)}, 6 * time.Minute, 10 * time.Minute, 1},
		{"only before up", []check.Result{result("api", 1, check.StatusUp)}, 10 * time.Minute, 10 * time.Minute, 0},
		{"only before down", []check.Result{result("api", 1, check.StatusDown)}, 0, 10 * time.Minute, 1},
		{"since boundary", []check.Result{result("api", 1, check.StatusDown), result("api", 10, check.StatusUp)}, 10 * time.Minute, 10 * time.Minute, 0},
		{"down repeated at since", []check.Result{result("api", 1, check.StatusDown), result("api", 10, check.StatusDown), result("api", 15, check.StatusUp)}, 5 * time.Minute, 10 * time.Minute, 1},
		{"until boundary", []check.Result{result("api", 10, check.StatusUp), result("api", 20, check.StatusDown), result("api", 21, check.StatusUp)}, 10 * time.Minute, 10 * time.Minute, 1},
		{"only after", []check.Result{result("api", 21, check.StatusDown)}, 0, 0, 0},
		{"observation starts inside", []check.Result{result("api", 15, check.StatusUp), result("api", 21, check.StatusDown)}, 5 * time.Minute, 5 * time.Minute, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, path := openTemp(t)
			record(t, s, tc.rows...)
			record(t, s, result("other", 10, check.StatusDown))
			ro, err := OpenReadOnly(path)
			if err != nil {
				t.Fatal(err)
			}
			defer ro.Close()
			up, observed, incidents, err := ro.UptimeWindow(context.Background(), "api", t0.Add(10*time.Minute), t0.Add(20*time.Minute))
			if err != nil || up != tc.up || observed != tc.observed || incidents != tc.incidents {
				t.Fatalf("got %v, %v, %d, %v; want %v, %v, %d", up, observed, incidents, err, tc.up, tc.observed, tc.incidents)
			}
			// Querying must preserve history and existing public reads.
			rows, err := s.Recent(context.Background(), "api", 100)
			if err != nil || len(rows) != len(tc.rows) {
				t.Fatalf("history changed: %v %v", rows, err)
			}
		})
	}
}

func TestUptimeWindowIncidentMatchesOutagesAtEqualTimestamps(t *testing.T) {
	s, _ := openTemp(t)
	record(t, s, result("api", 1, check.StatusDown), result("api", 1, check.StatusUp), result("api", 15, check.StatusUp))
	outages, err := s.Outages(context.Background(), "api", 10)
	if err != nil || len(outages) != 1 || !outages[0].End.Equal(t0.Add(15*time.Minute)) {
		t.Fatalf("outages: %v %v", outages, err)
	}
	_, _, incidents, err := s.UptimeWindow(context.Background(), "api", t0.Add(10*time.Minute), t0.Add(20*time.Minute))
	if err != nil || incidents != 1 {
		t.Fatalf("incidents = %d, err = %v; want 1 overlapping outage", incidents, err)
	}
}

func TestUptimeWindowIncidentParity(t *testing.T) {
	for _, rows := range [][]check.Result{
		{result("api", 1, check.StatusDown), result("api", 1, check.StatusUp), result("api", 1, check.StatusDown), result("api", 1, check.StatusUp)},
		{result("api", 1, check.StatusDown), result("api", 1, check.StatusUp), result("api", 5, check.StatusDown), result("api", 21, check.StatusUp)},
		{result("api", 1, check.StatusDown), result("api", 10, check.StatusUp), result("api", 10, check.StatusDown), result("api", 10, check.StatusUp), result("api", 20, check.StatusDown)},
		{result("api", 1, check.StatusDown), result("api", 5, check.StatusDown), result("api", 10, check.StatusDown), result("api", 21, check.StatusUp)},
	} {
		s, _ := openTemp(t)
		record(t, s, rows...)
		outages, err := s.Outages(context.Background(), "api", -1)
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		since, until := t0.Add(10*time.Minute), t0.Add(20*time.Minute)
		for _, o := range outages {
			if !o.Start.After(until) && (o.Ongoing || o.End.After(since)) {
				want++
			}
		}
		_, _, got, err := s.UptimeWindow(context.Background(), "api", since, until)
		if err != nil || got != want {
			t.Fatalf("rows %v: got %d, %v; want %d", rows, got, err, want)
		}
	}
}

func TestUptimeWindowIncidentParitySequences(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	// Equal timestamps straddle both boundaries; exercise every status order.
	times := []int{1, 1, 5, 10, 10, 15, 20, 21}
	since, until := t0.Add(10*time.Minute), t0.Add(20*time.Minute)
	for mask := 0; mask < 1<<len(times); mask++ {
		name := fmt.Sprintf("sequence-%d", mask)
		for i, minute := range times {
			status := check.StatusUp
			if mask&(1<<i) != 0 {
				status = check.StatusDown
			}
			if err := s.Record(ctx, result(name, minute, status)); err != nil {
				t.Fatal(err)
			}
		}
		outages, err := s.Outages(ctx, name, -1)
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		for _, o := range outages {
			if !o.Start.After(until) && (o.Ongoing || o.End.After(since)) {
				want++
			}
		}
		_, _, got, err := s.UptimeWindow(ctx, name, since, until)
		if err != nil || got != want {
			t.Fatalf("mask %08b: got %d, %v; want %d", mask, got, err, want)
		}
	}
}

func BenchmarkUptimeWindowHistory(b *testing.B) {
	s, err := Open(filepath.Join(b.TempDir(), "benchmark.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 10000; i++ {
		status := check.StatusUp
		if i%2 != 0 {
			status = check.StatusDown
		}
		if err := s.Record(context.Background(), check.Result{CheckName: "api", At: t0.Add(time.Duration(i) * time.Minute), Status: status}); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, err := s.UptimeWindow(context.Background(), "api", t0.Add(9990*time.Minute), t0.Add(10000*time.Minute)); err != nil {
			b.Fatal(err)
		}
	}
}

func TestUptimeWindowManyRows(t *testing.T) {
	s, _ := openTemp(t)
	for i := 0; i < 17280; i++ {
		status := check.StatusUp
		if i%2 == 1 {
			status = check.StatusDown
		}
		record(t, s, check.Result{CheckName: "api", At: t0.Add(time.Duration(i) * 5 * time.Second), Status: status})
	}
	up, observed, incidents, err := s.UptimeWindow(context.Background(), "api", t0, t0.Add(24*time.Hour))
	if err != nil || up != 12*time.Hour || observed != 24*time.Hour || incidents != 8640 {
		t.Fatalf("%v %v %d %v", up, observed, incidents, err)
	}
}
