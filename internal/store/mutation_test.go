package store_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

func diskState(t *testing.T, path string) [][]byte {
	t.Helper()
	var state [][]byte
	for _, suffix := range []string{"", "-wal"} {
		b, err := os.ReadFile(path + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		state = append(state, b)
	}
	return state
}

func TestRecordDefinedRejectsObsoleteDefinition(t *testing.T) {
	for _, field := range []string{"url", "interval", "timeout", "status", "other handle recreate"} {
		t.Run(field, func(t *testing.T) {
			s, path := seeded(t)
			started := definition("api")
			changed := started
			switch field {
			case "url", "other handle recreate":
				changed.URL += "/new"
			case "interval":
				changed.Interval += time.Second
			case "timeout":
				changed.Timeout += time.Second
			case "status":
				changed.ExpectedStatus = 204
			}
			if field == "other handle recreate" {
				other, err := store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer other.Close()
				if err := other.DeleteCheck(ctx, started.Name); err != nil {
					t.Fatal(err)
				}
				if err := other.AddCheck(ctx, changed); err != nil {
					t.Fatal(err)
				}
			} else if err := s.UpdateCheck(ctx, changed); err != nil {
				t.Fatal(err)
			}
			before := observe(t, s)
			r := check.Result{CheckName: started.Name, At: at, Status: check.StatusDown, StatusCode: 503}
			stored, err := s.RecordDefined(ctx, started, r)
			if stored || err != nil {
				t.Fatalf("obsolete RecordDefined = %v, %v; want false, nil", stored, err)
			}
			assertUnchanged(t, s, before)
		})
	}
}

func TestUpdateCheckKeepsOrderHistoryAndHeartbeat(t *testing.T) {
	s, path := seeded(t)
	before := observe(t, s)
	c := definition("api")
	c.URL, c.Interval, c.Timeout, c.ExpectedStatus = "https://changed.example", time.Minute, time.Second, 204
	if err := s.UpdateCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	before.Checks[0] = c
	assertUnchanged(t, s, before)
	reader, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	assertUnchanged(t, reader, before)
	if err := s.AddCheck(ctx, definition("last")); err != nil {
		t.Fatal(err)
	}
	before.Checks = append(before.Checks, definition("last"))
	assertUnchanged(t, s, before)
}

func TestMutationRejectionsLeaveStorageIdentical(t *testing.T) {
	for _, rule := range []string{"name", "url", "interval", "timeout", "timeout limit", "status low", "status high", "missing", "cancel update", "missing delete", "cancel delete"} {
		t.Run(rule, func(t *testing.T) {
			s, path := seeded(t)
			before := observe(t, s)
			c := definition("api")
			callCtx := ctx
			want := ""
			switch rule {
			case "name":
				c.Name = ""
			case "url":
				c.URL = "ftp://example.com"
			case "interval":
				c.Interval = 0
			case "timeout":
				c.Timeout = 0
			case "timeout limit":
				c.Timeout = time.Hour
			case "status low":
				c.ExpectedStatus = 99
			case "status high":
				c.ExpectedStatus = 600
			case "missing":
				c.Name = "new"
				want = `store: update check: name "new" not found`
			case "missing delete":
				c.Name = "new"
				want = `store: delete check: name "new" not found`
			case "cancel update", "cancel delete":
				var cancel context.CancelFunc
				callCtx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if want == "" && rule != "cancel update" && rule != "cancel delete" {
				want = fmt.Sprintf("store: update check: %v", config.ValidateCheck(c))
			}
			disk := diskState(t, path)
			var err error
			if rule == "missing delete" || rule == "cancel delete" {
				err = s.DeleteCheck(callCtx, c.Name)
			} else {
				err = s.UpdateCheck(callCtx, c)
			}
			if err == nil || (want != "" && err.Error() != want) {
				t.Fatalf("error = %v, want %q", err, want)
			}
			after := diskState(t, path)
			for i := range disk {
				if !bytes.Equal(disk[i], after[i]) {
					t.Fatal("rejected mutation changed database bytes")
				}
			}
			assertUnchanged(t, s, before)
		})
	}
}

func TestDeleteCheckAndRecordDefinedAcrossHandles(t *testing.T) {
	daemon, path := seeded(t)
	tui, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer tui.Close()
	r := check.Result{CheckName: "web", At: at, Status: check.StatusDown, Error: "web failure"}
	if stored, err := daemon.RecordDefined(ctx, definition(r.CheckName), r); err != nil || !stored {
		t.Fatalf("RecordDefined = %v, %v", stored, err)
	}
	before := observe(t, daemon)
	if err := tui.DeleteCheck(ctx, "api"); err != nil {
		t.Fatal(err)
	}
	before.Checks = before.Checks[1:]
	before.Recent["api"], before.Outages["api"] = nil, nil
	assertUnchanged(t, daemon, before)
	r.CheckName = "api"
	if stored, err := daemon.RecordDefined(ctx, definition(r.CheckName), r); err != nil || stored {
		t.Fatalf("late RecordDefined = %v, %v; want false, nil", stored, err)
	}
	assertUnchanged(t, daemon, before)
	if err := tui.AddCheck(ctx, definition("api")); err != nil {
		t.Fatal(err)
	}
	before.Checks = append(before.Checks, definition("api"))
	assertUnchanged(t, daemon, before)
	if stored, err := daemon.RecordDefined(ctx, definition(r.CheckName), r); err != nil || !stored {
		t.Fatalf("re-added RecordDefined = %v, %v", stored, err)
	}
	before.Recent["api"] = []check.Result{r}
	before.Outages["api"] = []store.Outage{{Start: at, Ongoing: true, Error: r.Error}}
	assertUnchanged(t, daemon, before)
}

func TestRecordDefinedValidationAndRecordCompatibility(t *testing.T) {
	s, _ := seeded(t)
	beforeMismatch := observe(t, s)
	mismatched := check.Result{CheckName: "web", At: at, Status: check.StatusUp}
	if stored, err := s.RecordDefined(ctx, definition("api"), mismatched); stored || err != nil {
		t.Fatalf("mismatched name = %v, %v; want false, nil", stored, err)
	}
	assertUnchanged(t, s, beforeMismatch)
	for _, name := range []string{"api", "new"} {
		for _, status := range []check.Status{check.StatusCanceled, "unknown", ""} {
			before := observe(t, s)
			r := check.Result{CheckName: name, At: at, Status: status}
			stored, err := s.RecordDefined(ctx, definition(r.CheckName), r)
			want := fmt.Sprintf("store: refusing to record status %q for %s", status, name)
			if stored || err == nil || err.Error() != want {
				t.Fatalf("RecordDefined = %v, %v; want false, %s", stored, err, want)
			}
			assertUnchanged(t, s, before)
		}
	}
	before := observe(t, s)
	r := check.Result{CheckName: "new", At: at, Status: check.StatusUp}
	if stored, err := s.RecordDefined(ctx, definition(r.CheckName), r); err != nil || stored {
		t.Fatalf("undefined RecordDefined = %v, %v", stored, err)
	}
	assertUnchanged(t, s, before)
	if err := s.Record(ctx, r); err != nil {
		t.Fatal(err)
	}
	before.Recent["new"] = []check.Result{r}
	assertUnchanged(t, s, before)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	r.CheckName = "api"
	if stored, err := s.RecordDefined(canceled, definition(r.CheckName), r); stored || err == nil {
		t.Fatalf("canceled context = %v, %v", stored, err)
	}
	assertUnchanged(t, s, before)
	// All fields of a successful result are preserved, including down/up outages.
	for i, status := range []check.Status{check.StatusDown, check.StatusUp} {
		r = check.Result{CheckName: "web", At: at.Add(time.Duration(i) * time.Minute), Status: status, Latency: time.Second, StatusCode: 503, Error: "reason"}
		if stored, err := s.RecordDefined(ctx, definition(r.CheckName), r); err != nil || !stored {
			t.Fatalf("RecordDefined = %v, %v", stored, err)
		}
		before.Recent["web"] = append([]check.Result{r}, before.Recent["web"]...)
		if status == check.StatusDown {
			before.Outages["web"] = []store.Outage{{Start: r.At, Ongoing: true, Error: r.Error}}
		} else {
			before.Outages["web"][0].End = r.At
			before.Outages["web"][0].Ongoing = false
		}
		assertUnchanged(t, s, before)
	}
}

func TestMissingDeleteKeepsLegacyOrphanHistory(t *testing.T) {
	s, path := seeded(t)
	if err := s.Record(ctx, check.Result{CheckName: "new", At: at, Status: check.StatusDown}); err != nil {
		t.Fatal(err)
	}
	before := observe(t, s)
	disk := diskState(t, path)
	if err := s.DeleteCheck(ctx, "new"); err == nil || err.Error() != `store: delete check: name "new" not found` {
		t.Fatalf("missing delete = %v", err)
	}
	if !reflect.DeepEqual(diskState(t, path), disk) {
		t.Fatal("missing delete changed database bytes")
	}
	assertUnchanged(t, s, before)
}
