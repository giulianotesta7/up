package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

var now = time.Date(2026, 10, 25, 3, 45, 0, 0, time.UTC)

var testChecks = []config.Check{
	{Name: "api", URL: "https://api.example.com/health", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200},
	{Name: "payments", URL: "https://pay.example.com", Interval: time.Minute, Timeout: 10 * time.Second, ExpectedStatus: 204},
	{Name: "new", URL: "https://new.example.com", Interval: time.Minute, Timeout: 5 * time.Second, ExpectedStatus: 200},
}

const reason = "unexpected status 503 (expected 204)"

func at(clock string) time.Time {
	t, err := time.Parse("15:04:05", clock)
	if err != nil {
		panic(err)
	}
	return time.Date(2026, 10, 25, t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

func up(name, clock string) check.Result {
	return check.Result{CheckName: name, At: at(clock), Status: check.StatusUp, StatusCode: 200, Latency: 84 * time.Millisecond}
}

func down(name, clock string) check.Result {
	return check.Result{CheckName: name, At: at(clock), Status: check.StatusDown, StatusCode: 503, Latency: 12 * time.Millisecond, Error: reason}
}

// seededSnapshot stores a realistic history and loads it back: api is up,
// payments had one outage 03:12 → 03:40 and is down again since 03:44, new
// has no result yet.
func seededSnapshot(t *testing.T, beat time.Time) Snapshot {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "up.db")
	w, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.SetChecks(ctx, testChecks); err != nil {
		t.Fatal(err)
	}
	for _, r := range []check.Result{
		up("api", "03:44:30"),
		up("payments", "03:10:00"), down("payments", "03:12:00"), down("payments", "03:30:00"),
		up("payments", "03:40:00"), down("payments", "03:44:00"),
	} {
		if err := w.Record(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if !beat.IsZero() {
		if err := w.Beat(ctx, beat); err != nil {
			t.Fatal(err)
		}
	}

	r, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	snap, err := Load(ctx, r)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return snap
}

func TestLoad(t *testing.T) {
	snap := seededSnapshot(t, now.Add(-5*time.Second))

	if len(snap.Services) != 3 {
		t.Fatalf("got %d services, want 3 in config order", len(snap.Services))
	}
	api, pay, fresh := snap.Services[0], snap.Services[1], snap.Services[2]
	if api.Check != testChecks[0] || !api.HasResult || api.Latest != up("api", "03:44:30") || api.Label() != "UP" {
		t.Errorf("api = %+v, want the latest up result", api)
	}
	if pay.Label() != "DOWN" || !pay.DownSince.Equal(at("03:44:00")) || pay.Latest.Error != reason {
		t.Errorf("payments = %+v, want DOWN since 03:44:00 with the reason", pay)
	}
	if len(pay.Recent) != 5 || !pay.Recent[0].At.Equal(at("03:44:00")) {
		t.Errorf("payments recent = %+v, want 5 results newest first", pay.Recent)
	}
	if len(pay.Outages) != 2 || !pay.Outages[1].Start.Equal(at("03:12:00")) || !pay.Outages[1].End.Equal(at("03:40:00")) {
		t.Errorf("payments outages = %+v, want the ongoing one and 03:12 → 03:40", pay.Outages)
	}
	if fresh.HasResult || fresh.Label() != "PENDING" || !fresh.DownSince.IsZero() {
		t.Errorf("new = %+v, want PENDING without result", fresh)
	}
	if !snap.HasBeat || !snap.LastBeat.Equal(now.Add(-5*time.Second)) {
		t.Errorf("heartbeat = %v %v, want the stored one", snap.LastBeat, snap.HasBeat)
	}
}

func TestStale(t *testing.T) {
	tests := []struct {
		name string
		snap Snapshot
		want bool
	}{
		{"fresh heartbeat", Snapshot{HasBeat: true, LastBeat: now.Add(-5 * time.Second)}, false},
		{"exactly at the threshold", Snapshot{HasBeat: true, LastBeat: now.Add(-StaleAfter)}, false},
		{"older than the threshold", Snapshot{HasBeat: true, LastBeat: now.Add(-StaleAfter - time.Second)}, true},
		{"never beat", Snapshot{}, true},
	}
	for _, tt := range tests {
		if got := tt.snap.Stale(now); got != tt.want {
			t.Errorf("%s: Stale() = %v, want %v", tt.name, got, tt.want)
		}
	}
	if StaleAfter != 30*time.Second {
		t.Errorf("StaleAfter = %v, want 30s", StaleAfter)
	}
}

func view(m Model) string {
	return ansi.Strip(m.View().Content)
}

func modelWith(snap Snapshot) Model {
	m := New(nil, func() time.Time { return now })
	updated, _ := m.Update(snapshotMsg{snap: snap})
	return updated.(Model)
}

func press(t *testing.T, m Model, keys ...tea.KeyPressMsg) (Model, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, k := range keys {
		var next tea.Model
		next, cmd = m.Update(k)
		m = next.(Model)
	}
	return m, cmd
}

var (
	keyUp    = tea.KeyPressMsg{Code: tea.KeyUp}
	keyDown  = tea.KeyPressMsg{Code: tea.KeyDown}
	keyEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
	keyEsc   = tea.KeyPressMsg{Code: tea.KeyEscape}
	keyBack  = tea.KeyPressMsg{Code: tea.KeyBackspace}
	keyJ     = tea.KeyPressMsg{Code: 'j', Text: "j"}
	keyK     = tea.KeyPressMsg{Code: 'k', Text: "k"}
	keyQ     = tea.KeyPressMsg{Code: 'q', Text: "q"}
	keyCtrlC = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
)

func TestDatabaseFooterAllViews(t *testing.T) {
	m := New(nil, func() time.Time { return now }, Options{DatabasePath: "/shared/up.db"})
	next, _ := m.Update(snapshotMsg{snap: seededSnapshot(t, now)})
	m = next.(Model)
	for _, name := range []string{"list", "detail", "form"} {
		t.Run(name, func(t *testing.T) {
			current := m
			if name == "detail" {
				current, _ = press(t, current, keyEnter)
			}
			if name == "form" {
				current, _ = press(t, current, keyN)
			}
			out := view(current)
			if !strings.HasSuffix(out, "\ndatabase: /shared/up.db") || strings.Count(out, "database: ") != 1 {
				t.Fatalf("missing single footer below key help:\n%s", out)
			}
		})
	}
}

func TestListView(t *testing.T) {
	out := view(modelWith(seededSnapshot(t, now.Add(-5*time.Second))))

	for _, want := range []string{
		"SERVICE", "STATUS", "LATENCY", "LAST CHECK",
		"api", "UP", "84ms", "30s ago",
		"payments", "DOWN", "1m ago", reason,
		"new", "PENDING",
		"↑/↓ select", "enter details", "q quit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list view missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "daemon not responding") {
		t.Errorf("list view shows the stale banner with a fresh heartbeat:\n%s", out)
	}
	if line := lineWith(out, "new"); strings.Contains(line, "ago") {
		t.Errorf("pending row %q shows a last check", line)
	}
}

func TestListViewWhenDaemonIsStale(t *testing.T) {
	out := view(modelWith(seededSnapshot(t, now.Add(-2*time.Minute))))

	if want := "daemon not responding: last heartbeat 2m ago — is `up serve` running?"; !strings.Contains(out, want) {
		t.Errorf("stale view missing %q:\n%s", want, out)
	}
	if line := lineWith(out, "api"); !strings.Contains(line, "UP") {
		t.Errorf("api row %q lost its last known status while stale", line)
	}
	if line := lineWith(out, "payments"); !strings.Contains(line, "DOWN") {
		t.Errorf("payments row %q lost its last known status while stale", line)
	}

	never := view(modelWith(seededSnapshot(t, time.Time{})))
	if want := "daemon not responding: no heartbeat yet — is `up serve` running?"; !strings.Contains(never, want) {
		t.Errorf("never-beat view missing %q:\n%s", want, never)
	}
}

func TestDetailView(t *testing.T) {
	m, _ := press(t, modelWith(seededSnapshot(t, now.Add(-5*time.Second))), keyDown, keyEnter)
	out := view(m)

	for _, want := range []string{
		"payments", "https://pay.example.com",
		"Interval: 1m0s", "Timeout: 10s", "Expected status: 204",
		"Status: DOWN", "Last error: " + reason, "Down since: Oct 25 03:44:00 (1m ago)",
		"Recent checks (oldest → newest): █▁▁█▁",
		"Oct 25 03:44:00 → ongoing",
		"Oct 25 03:12:00 → Oct 25 03:40:00 (28m0s)",
		"esc back",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("detail view missing %q:\n%s", want, out)
		}
	}
}

func TestDetailViewOfUpAndPendingServices(t *testing.T) {
	snap := seededSnapshot(t, now.Add(-5*time.Second))

	apiView := view(func() Model { m, _ := press(t, modelWith(snap), keyEnter); return m }())
	for _, want := range []string{"Status: UP", "Last error: —", "Down since: —", "No outages recorded."} {
		if !strings.Contains(apiView, want) {
			t.Errorf("api detail missing %q:\n%s", want, apiView)
		}
	}
	pendingView := view(func() Model { m, _ := press(t, modelWith(snap), keyDown, keyDown, keyEnter); return m }())
	if !strings.Contains(pendingView, "Status: PENDING") || !strings.Contains(pendingView, "No checks yet.") {
		t.Errorf("pending detail:\n%s", pendingView)
	}
}

func TestKeys(t *testing.T) {
	m := modelWith(seededSnapshot(t, now.Add(-5*time.Second)))

	if m, _ = press(t, m, keyUp); m.selected != 0 {
		t.Errorf("up at the top moved selection to %d", m.selected)
	}
	if m, _ = press(t, m, keyDown, keyJ, keyDown, keyDown); m.selected != 2 {
		t.Errorf("down past the bottom = %d, want 2", m.selected)
	}
	if m, _ = press(t, m, keyK); m.selected != 1 {
		t.Errorf("k = %d, want 1", m.selected)
	}
	if m, _ = press(t, m, keyEnter); !m.detail {
		t.Error("enter did not open the detail")
	}
	if m, _ = press(t, m, keyEsc); m.detail {
		t.Error("esc did not return to the list")
	}
	if m, _ = press(t, m, keyEnter, keyBack); m.detail {
		t.Error("backspace did not return to the list")
	}

	for _, k := range []tea.KeyPressMsg{keyQ, keyCtrlC} {
		_, cmd := press(t, m, k)
		if cmd == nil {
			t.Fatalf("%s returned no command", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%s did not quit", k)
		}
	}
}

func TestSelectionFollowsShrinkingList(t *testing.T) {
	snap := seededSnapshot(t, now.Add(-5*time.Second))
	m, _ := press(t, modelWith(snap), keyDown, keyDown)

	snap.Services = snap.Services[:1]
	next, _ := m.Update(snapshotMsg{snap: snap})
	if got := next.(Model).selected; got != 0 {
		t.Fatalf("selected = %d after the list shrank to one, want 0", got)
	}
}

func TestLoadErrorKeepsLastSnapshot(t *testing.T) {
	m := modelWith(seededSnapshot(t, now.Add(-5*time.Second)))

	next, cmd := m.Update(snapshotMsg{err: errors.New("store: checks: database is locked")})
	out := view(next.(Model))

	if !strings.Contains(out, "error reading the database: store: checks: database is locked") || !strings.Contains(out, "payments") {
		t.Fatalf("view after a read error:\n%s", out)
	}
	if cmd == nil {
		t.Fatal("a read error stopped polling")
	}
}

func TestEmptyConfigMessage(t *testing.T) {
	out := view(modelWith(Snapshot{HasBeat: true, LastBeat: now}))

	if !strings.Contains(out, "No checks configured.") {
		t.Fatalf("empty view:\n%s", out)
	}
}

func lineWith(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " >"), name+" ") {
			return line
		}
	}
	return ""
}
