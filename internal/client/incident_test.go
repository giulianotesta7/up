package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/store"
	"github.com/giulianotesta7/up/internal/tui"
)

// detailFrom opens the first monitor's detail and returns the screen from the
// INCIDENTS header to the footer.
func detailFrom(t *testing.T, s tui.Store, clock time.Time) string {
	t.Helper()
	m := tui.New(s, func() time.Time { return clock })
	next, _ := m.Update(m.Init()())
	next, _ = next.Update(kEnter)
	out := ansi.Strip(next.View().Content)
	i := strings.Index(out, "INCIDENTS")
	j := strings.Index(out, "esc back")
	if i < 0 || j < i {
		t.Fatalf("no incidents section:\n%s", out)
	}
	return out[i:j]
}

// --remote shows the same incidents, alerts, downtime and recent alerts as
// the local database.
func TestIncidentsAndRecentAlertsLocalAndRemote(t *testing.T) {
	c, s, _ := realServer(t)
	ctx := context.Background()
	if err := c.AddCheck(ctx, newCheck("api")); err != nil {
		t.Fatal(err)
	}
	// Half a minute off the minute, so the small clock-skew shift of the
	// remote snapshot cannot change a shown minute.
	clock := time.Now().Truncate(time.Minute).Add(30 * time.Second)
	base := clock.Add(-48 * time.Hour)
	result := func(minutes int, status check.Status, reason string) check.Result {
		return check.Result{CheckName: "api", At: base.Add(time.Duration(minutes) * time.Minute), Status: status, Latency: 50 * time.Millisecond, Error: reason}
	}
	for _, r := range []check.Result{
		result(0, check.StatusUp, ""),
		result(10, check.StatusDown, "status 502"), result(24, check.StatusUp, ""),
		result(60, check.StatusDown, "timeout after 5s"), result(62, check.StatusUp, ""),
		result(120, check.StatusDown, "connection refused"), result(161, check.StatusUp, ""),
	} {
		if err := s.Record(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	attempt := func(minutes int, channel string, event store.Event, outcome store.Outcome, reason string) store.Attempt {
		return store.Attempt{At: base.Add(time.Duration(minutes)*time.Minute + 5*time.Second), Monitor: "api", Channel: channel, Event: event, Outcome: outcome, Reason: reason}
	}
	for _, a := range []store.Attempt{
		attempt(10, "ops", store.EventDown, store.OutcomeSent, ""),
		attempt(10, "pager", store.EventDown, store.OutcomeSent, ""),
		attempt(24, "pager", store.EventRecovered, store.OutcomeFailed, "status 502"),
		attempt(120, "ops", store.EventDown, store.OutcomeHeld, "muted"),
	} {
		if err := s.RecordAttempt(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Beat(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	local, remote := detailFrom(t, s, clock), detailFrom(t, c, clock)
	if local != remote {
		t.Fatalf("remote differs from local:\n%s\n----\n%s", remote, local)
	}
	at := func(minutes int) string {
		return base.Add(time.Duration(minutes) * time.Minute).Local().Format("Jan 02 15:04")
	}
	for _, want := range []string{
		"INCIDENTS  30d · 3 incidents · 57m down · ",
		at(120) + "  41m       connection refused  ◷ held muted",
		at(60) + "  2m        timeout after 5s    — none",
		at(10) + "  14m       status 502          ✗ failed pager",
		"RECENT ALERTS",
		at(120) + "  monitor.down       ops    ◷ held    muted",
		at(24) + "  monitor.recovered  pager  ✗ failed  status 502",
		at(10) + "  monitor.down       pager  ✓ sent",
	} {
		if !strings.Contains(remote, want) {
			t.Errorf("remote detail missing %q:\n%s", want, remote)
		}
	}
}
