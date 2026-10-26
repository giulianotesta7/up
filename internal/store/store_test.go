package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
)

var t0 = time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "up.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func result(name string, minute int, status check.Status) check.Result {
	r := check.Result{CheckName: name, At: t0.Add(time.Duration(minute) * time.Minute), Status: status}
	if status == check.StatusUp {
		r.StatusCode, r.Latency = 200, 84*time.Millisecond
	} else {
		r.StatusCode, r.Error = 503, "unexpected status 503 (expected 200)"
	}
	return r
}

func record(t *testing.T, s *Store, results ...check.Result) {
	t.Helper()
	for _, r := range results {
		if err := s.Record(context.Background(), r); err != nil {
			t.Fatalf("Record(%+v) error = %v", r, err)
		}
	}
}

func TestSetChecksReplacesListInOrder(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	first := []config.Check{
		{Name: "api", URL: "https://api.example.com", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200},
		{Name: "web", URL: "https://example.com", Interval: time.Minute, Timeout: 10 * time.Second, ExpectedStatus: 204},
	}
	if err := s.SetChecks(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := []config.Check{first[1]}
	if err := s.SetChecks(ctx, second); err != nil {
		t.Fatal(err)
	}

	got, err := s.Checks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, second) {
		t.Fatalf("Checks() = %+v, want %+v", got, second)
	}
}

func TestLatestAndRecent(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	record(t, s, result("api", 0, check.StatusUp), result("api", 1, check.StatusDown), result("web", 2, check.StatusUp))

	latest, ok, err := s.Latest(ctx, "api")
	if err != nil || !ok {
		t.Fatalf("Latest() = %v, %v, want a result", ok, err)
	}
	if want := result("api", 1, check.StatusDown); !reflect.DeepEqual(latest, want) {
		t.Fatalf("Latest() = %+v, want %+v", latest, want)
	}

	recent, err := s.Recent(ctx, "api", 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []check.Result{result("api", 1, check.StatusDown), result("api", 0, check.StatusUp)}
	if !reflect.DeepEqual(recent, want) {
		t.Fatalf("Recent() = %+v, want newest first %+v", recent, want)
	}

	if recent, _ := s.Recent(ctx, "api", 1); len(recent) != 1 {
		t.Fatalf("Recent(limit 1) returned %d results", len(recent))
	}
	if _, ok, err := s.Latest(ctx, "missing"); ok || err != nil {
		t.Fatalf("Latest(missing) = %v, %v, want no result and no error", ok, err)
	}
}

func TestOutages(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	record(t, s,
		result("api", 0, check.StatusDown),
		result("api", 1, check.StatusUp),
		result("api", 2, check.StatusDown),
		result("api", 3, check.StatusDown),
		result("api", 4, check.StatusUp),
		result("web", 5, check.StatusDown),
		result("api", 6, check.StatusDown),
	)

	got, err := s.Outages(ctx, "api", 10)
	if err != nil {
		t.Fatal(err)
	}
	reason := "unexpected status 503 (expected 200)"
	want := []Outage{
		{Start: t0.Add(6 * time.Minute), Ongoing: true, Error: reason},
		{Start: t0.Add(2 * time.Minute), End: t0.Add(4 * time.Minute), Error: reason},
		{Start: t0, End: t0.Add(time.Minute), Error: reason},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Outages() = %+v, want newest first %+v", got, want)
	}

	limited, err := s.Outages(ctx, "api", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(limited, want[:2]) {
		t.Fatalf("Outages(limit 2) = %+v, want %+v", limited, want[:2])
	}
}

func TestRecordRejectsCanceledResult(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	err := s.Record(ctx, check.Result{CheckName: "api", At: t0, Status: check.StatusCanceled})
	if err == nil || !strings.Contains(err.Error(), `store: refusing to record status "canceled"`) {
		t.Fatalf("Record(canceled) error = %v, want refusal", err)
	}
	if _, ok, _ := s.Latest(ctx, "api"); ok {
		t.Fatal("canceled result was stored")
	}
}

func TestHeartbeat(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	if _, ok, err := s.LastBeat(ctx); ok || err != nil {
		t.Fatalf("LastBeat() on new store = %v, %v, want none", ok, err)
	}
	for _, at := range []time.Time{t0, t0.Add(time.Minute)} {
		if err := s.Beat(ctx, at); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := s.LastBeat(ctx)
	if err != nil || !ok || !got.Equal(t0.Add(time.Minute)) {
		t.Fatalf("LastBeat() = %v, %v, %v, want %v", got, ok, err, t0.Add(time.Minute))
	}
}

func TestReadOnlySeesWriterData(t *testing.T) {
	writer, path := openTemp(t)
	record(t, writer, result("api", 0, check.StatusUp))

	reader, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly() error = %v", err)
	}
	defer reader.Close()

	record(t, writer, result("api", 1, check.StatusDown))
	latest, ok, err := reader.Latest(context.Background(), "api")
	if err != nil || !ok || latest.Status != check.StatusDown {
		t.Fatalf("reader Latest() = %+v, %v, %v, want the writer's newest result", latest, ok, err)
	}
	if err := reader.Record(context.Background(), result("api", 2, check.StatusUp)); err == nil {
		t.Fatal("read-only store accepted a write")
	}
}

func TestOpenReadOnlyMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")

	_, err := OpenReadOnly(path)
	if err == nil {
		t.Fatal("OpenReadOnly() error = nil, want error")
	}
	if want := "store: open " + path; !strings.Contains(err.Error(), want) {
		t.Fatalf("OpenReadOnly() error = %q, want it to contain %q", err, want)
	}
}

func TestOpenReadOnlyUninitializedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := OpenReadOnly(path)
	if err == nil || !strings.Contains(err.Error(), "database is not initialized (start `up serve` first)") {
		t.Fatalf("OpenReadOnly() error = %v, want not-initialized error", err)
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	s, path := openTemp(t)
	s.Close()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	for name, open := range map[string]func(string) (*Store, error){"Open": Open, "OpenReadOnly": OpenReadOnly} {
		_, err := open(path)
		if err == nil || !strings.Contains(err.Error(), "store: database schema version 99 is newer than this up supports (1)") {
			t.Errorf("%s() error = %v, want newer-schema refusal", name, err)
		}
	}
}

func TestReopenKeepsData(t *testing.T) {
	s, path := openTemp(t)
	record(t, s, result("api", 0, check.StatusUp))
	s.Close()

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, ok, err := again.Latest(context.Background(), "api"); !ok || err != nil {
		t.Fatalf("Latest() after reopen = %v, %v, want the stored result", ok, err)
	}
}
