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

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/store"
)

var keyV = tea.KeyPressMsg{Code: 'v', Text: "v"}

// hbBase is the ping URL's base for the test daemon (":8080", host "box").
const hbBase = "http://box:8080/api/v1/heartbeat/"

const hbMask = "••••••••"

// hbModel is a model over heartbeatStore ("backup", period 1h, grace 10m)
// sized width x height; api records a daemon listening on ":8080".
func hbModel(t *testing.T, api bool, width, height int) (*store.Store, string, Model) {
	t.Helper()
	s, path := heartbeatStore(t)
	if api {
		if err := s.SetAPIListener(context.Background(), config.APIListener{Address: ":8080"}); err != nil {
			t.Fatal(err)
		}
	}
	return s, path, sizedModel(t, New(s, func() time.Time { return now }, Options{DatabasePath: path, Hostname: "box"}), width, height)
}

func sizedModel(t *testing.T, m Model, width, height int) Model {
	t.Helper()
	if width > 0 {
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		m = next.(Model)
	}
	next, _ := m.Update(m.Init()())
	return next.(Model)
}

func revealed(t *testing.T, s *store.Store, path, name string) string {
	t.Helper()
	token, err := NewLocalHeartbeatTokens(s, path).RevealHeartbeatToken(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// run executes cmd and feeds its message (and any follow-up snapshot read)
// back to the model.
func run(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command")
	}
	return finishCommand(t, m, cmd)
}

// openHeartbeatForm opens a new monitor form with Type Heartbeat.
func openHeartbeatForm(t *testing.T, m Model) Model {
	t.Helper()
	m, _ = press(t, m, keyN, keyShiftTab, keyRight, keyRight)
	return m
}

// createHeartbeat fills and saves a new heartbeat called name.
func createHeartbeat(t *testing.T, m Model, name string) (Model, tea.Cmd) {
	t.Helper()
	m = openHeartbeatForm(t, m)
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, name)
	m, _ = press(t, m, keyTab, keyTab) // Schedule, then Period
	m = replaceField(t, m, "24h", "2h")
	m, _ = press(t, m, keyTab)
	m = replaceField(t, m, "1h", "15m")
	return press(t, m, keyEnter)
}

// boxGeometry is the top row, bottom row, left column and width of the modal
// titled title.
func boxGeometry(t *testing.T, m Model, title string) [4]int {
	t.Helper()
	lines := strings.Split(view(m), "\n")
	g := [4]int{-1, -1, 0, 0}
	for i, line := range lines {
		if at := strings.Index(line, "╭─ "+title); at >= 0 && g[0] < 0 {
			g[0], g[2] = i, ansi.StringWidth(line[:at])
			g[3] = ansi.StringWidth(line[at : at+strings.Index(line[at:], "╮")+len("╮")])
		}
		if g[0] >= 0 && g[1] < 0 && strings.Contains(line, "╰") {
			g[1] = i
		}
	}
	if g[0] < 0 || g[1] < 0 {
		t.Fatalf("no %q box:\n%s", title, view(m))
	}
	return g
}

func requireAbsent(t *testing.T, m Model, unwanted ...string) {
	t.Helper()
	out := view(m)
	for _, u := range unwanted {
		if strings.Contains(out, u) {
			t.Fatalf("screen shows %q:\n%s", u, out)
		}
	}
}

func requireWidth(t *testing.T, m Model, width int) {
	t.Helper()
	for _, line := range strings.Split(view(m), "\n") {
		// The database footer is as long as its path, as on every screen.
		if w := ansi.StringWidth(line); w > width && !strings.HasPrefix(line, "database: ") {
			t.Fatalf("line is %d columns, over %d: %q", w, width, line)
		}
	}
}

func TestHeartbeatFormShowsOnlyHeartbeatRows(t *testing.T) {
	_, _, m := hbModel(t, false, 120, 40)
	m = openHeartbeatForm(t, m)
	requireText(t, m, "Type: ‹ Heartbeat › ", "Name: ", "Period: 24h", "Grace: 1h", "Alerts when no ping arrives within period + grace.",
		"Channel: ‹ none ›", "Alert after: ‹ 1 › missed pings in a row", "Recover after: ‹ 1 › pings in a row",
		"First alert ≈ 25h after the last ping", "! No API listener: pings cannot arrive. Run up serve --api-listen")
	requireAbsent(t, m, "HTTP/HTTPS URL", "Interval", "Timeout", "Expected status", "TLS expiry", "Advanced", "Host", "Port", "Token:")
	m, _ = press(t, m, keyArrowLeft, keyArrowLeft)
	requireText(t, m, "Type: ‹ HTTP › ", "HTTP/HTTPS URL")
	requireAbsent(t, m, "Period", "Grace", "No API listener")

	// With a listener there is nothing to warn about.
	_, _, m = hbModel(t, true, 120, 40)
	m = openHeartbeatForm(t, m)
	requireText(t, m, "Period: 24h")
	requireAbsent(t, m, "No API listener", "api-listen")
}

func TestHeartbeatFormRejectsBadPeriodAndKeepsStore(t *testing.T) {
	s, _, m := hbModel(t, true, 120, 40)
	m = openHeartbeatForm(t, m)
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "nightly")
	m, _ = press(t, m, keyTab, keyTab) // Schedule, then Period
	m = replaceField(t, m, "24h", "30s")
	m, cmd := press(t, m, keyEnter)
	if cmd != nil {
		t.Fatal("an invalid period saved")
	}
	requireText(t, m, "Cannot save monitor: period must be between 1m and 744h")
	if got, _ := s.Checks(context.Background()); len(got) != 1 {
		t.Fatalf("checks = %+v", got)
	}
}

func TestHeartbeatFormBoxStaysPutAcrossTypesAndFocus(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}} {
		_, _, m := hbModel(t, false, size[0], size[1])
		m, _ = press(t, m, keyN, keyShiftTab)
		want := boxGeometry(t, m, "New monitor")
		for i := range 5 {
			m, _ = press(t, m, keyRight)
			if got := boxGeometry(t, m, "New monitor"); got != want {
				t.Fatalf("%v: switch %d: box %v, was %v:\n%s", size, i, got, want, view(m))
			}
		}
		m, _ = press(t, m, keyRight, keyRight) // Heartbeat
		for i := range 8 {
			m, _ = press(t, m, keyTab)
			if got := boxGeometry(t, m, "New monitor"); got != want {
				t.Fatalf("%v: tab %d: box %v, was %v:\n%s", size, i, got, want, view(m))
			}
		}
		requireWidth(t, m, size[0])
	}
}

func TestHeartbeatSaveShowsTheURLOnceUntilEsc(t *testing.T) {
	s, path, m := hbModel(t, true, 160, 50)
	m, cmd := createHeartbeat(t, m, "nightly")
	requireAbsent(t, m, "uphb_")
	m = run(t, m, cmd)
	got, ok := storedByName(t, s, "nightly")
	if !ok || got.Kind != config.CheckHeartbeat || got.Period != 2*time.Hour || got.Grace != 15*time.Minute ||
		got.URL != "" || got.Interval != 0 || got.Timeout != 0 || got.ExpectedStatus != 0 || got.Host != "" {
		t.Fatalf("saved = %+v, %v", got, ok)
	}
	token := revealed(t, s, path, "nightly")
	requireText(t, m, "Ping URL · nightly", "Saved. The job sends a POST to this URL when it ends:", "URL    "+hbBase+token,
		"Add /fail to the URL when the job fails.", "Shown in full only now; v in the detail shows it again.", "esc close")
	_ = boxGeometry(t, m, "Ping URL · nightly")
	// A refresh keeps it on screen; other keys do nothing.
	m = reload(t, m)
	m, _ = press(t, m, keyDown, keyV)
	requireText(t, m, hbBase+token)
	m, _ = press(t, m, keyEsc)
	requireAbsent(t, m, token, "Ping URL")
	requireText(t, m, "nightly")
	if m.form != nil {
		t.Fatal("the form is still open")
	}
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "URL          "+hbBase+hbMask)
	requireAbsent(t, m, token)
	if found, err := s.PingHeartbeat(context.Background(), token, false, now); err != nil || !found {
		t.Fatalf("the shown token does not ping: %v, %v", found, err)
	}
}

func TestHeartbeatURLAtEightyColumnsIsTwoUnbrokenLines(t *testing.T) {
	s, path, m := hbModel(t, true, 80, 24)
	m = openHeartbeatForm(t, m)
	_ = boxGeometry(t, m, "New monitor")
	requireWidth(t, m, 80)
	m, _ = press(t, m, keyEsc)
	m, cmd := createHeartbeat(t, m, "nightly")
	m = run(t, m, cmd)
	token := revealed(t, s, path, "nightly")
	_ = boxGeometry(t, m, "Ping URL · nightly")
	requireText(t, m, "URL    "+hbBase+" ", "Token  "+token+" ", "Join them for the full URL.")
	requireAbsent(t, m, hbBase+token)
	requireWidth(t, m, 80)
	m, _ = press(t, m, keyEsc)
	requireAbsent(t, m, token)
}

func TestHeartbeatRegenerateFailureAfterCreateSaysHowToGetAURL(t *testing.T) {
	s, path := heartbeatStore(t)
	if err := s.SetAPIListener(context.Background(), config.APIListener{Address: ":8080"}); err != nil {
		t.Fatal(err)
	}
	f := failingTokens{Store: s, local: NewLocalHeartbeatTokens(s, path)}
	m := sizedModel(t, New(f, func() time.Time { return now }, Options{DatabasePath: path, Hostname: "box"}), 100, 40)
	m, cmd := createHeartbeat(t, m, "nightly")
	m = run(t, m, cmd)
	if _, ok := storedByName(t, s, "nightly"); !ok {
		t.Fatal("the monitor was not created")
	}
	if m.form != nil {
		t.Fatal("the form is still open")
	}
	requireText(t, m, "Saved nightly, but its ping URL cannot be shown: server unavailable. Use Token: new in Edit monitor to get one.")
	requireAbsent(t, m, "uphb_", "Ping URL ·")
}

// failingTokens reveals locally but cannot replace a token.
type failingTokens struct {
	*store.Store
	local LocalHeartbeatTokens
}

func (f failingTokens) RevealHeartbeatToken(ctx context.Context, name string) (string, error) {
	return f.local.RevealHeartbeatToken(ctx, name)
}

func (f failingTokens) RegenerateHeartbeatToken(context.Context, string) (string, error) {
	return "", errors.New("server unavailable")
}

func lineIndex(t *testing.T, m Model, prefix string) int {
	t.Helper()
	for i, line := range strings.Split(view(m), "\n") {
		if strings.HasPrefix(line, prefix) {
			return i
		}
	}
	t.Fatalf("no line starting with %q:\n%s", prefix, view(m))
	return -1
}

func TestHeartbeatDetailRevealsWithVAndEscHides(t *testing.T) {
	s, path, m := hbModel(t, true, 100, 40)
	token := revealed(t, s, path, "backup")
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "PING URL", "URL          "+hbBase+hbMask, "v show URL")
	requireAbsent(t, m, token)
	incidents, lines := lineIndex(t, m, "INCIDENTS"), strings.Count(view(m), "\n")
	m, cmd := press(t, m, keyV)
	m = run(t, m, cmd)
	requireText(t, m, "URL          "+hbBase+token, "v hide URL")
	if lineIndex(t, m, "INCIDENTS") != incidents || strings.Count(view(m), "\n") != lines {
		t.Fatalf("revealing moved the detail:\n%s", view(m))
	}
	m, _ = press(t, m, keyV)
	requireText(t, m, hbBase+hbMask)
	requireAbsent(t, m, token)

	m, cmd = press(t, m, keyV)
	m = run(t, m, cmd)
	requireText(t, m, token)
	m, _ = press(t, m, keyEsc)
	requireAbsent(t, m, token)
	m, _ = press(t, m, keyEnter)
	requireText(t, m, hbBase+hbMask)
	requireAbsent(t, m, token)
}

func TestHeartbeatDetailRevealAtEightyColumns(t *testing.T) {
	s, path, m := hbModel(t, true, 80, 24)
	token := revealed(t, s, path, "backup")
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "URL          "+hbBase, "Token        "+hbMask)
	incidents := lineIndex(t, m, "INCIDENTS")
	m, cmd := press(t, m, keyV)
	m = run(t, m, cmd)
	requireText(t, m, "Token        "+token)
	requireAbsent(t, m, hbBase+token)
	if lineIndex(t, m, "INCIDENTS") != incidents {
		t.Fatalf("revealing moved the detail:\n%s", view(m))
	}
	requireWidth(t, m, 80)
}

func TestHeartbeatRevealWithoutTheKeyShowsTheHint(t *testing.T) {
	s, path := heartbeatStore(t)
	elsewhere := filepath.Join(t.TempDir(), "up.db")
	m := sizedModel(t, New(s, func() time.Time { return now }, Options{DatabasePath: elsewhere, Hostname: "box"}), 100, 40)
	token := revealed(t, s, path, "backup")
	m, _ = press(t, m, keyEnter)
	m, cmd := press(t, m, keyV)
	m = run(t, m, cmd)
	box := boxText(t, m, "Ping URL · backup")
	for _, want := range []string{"the ping URL cannot be shown here: cannot read the encryption key " + elsewhere + ".key",
		"run up tui as the user that runs up serve, or use up tui --remote", "esc close"} {
		if !strings.Contains(squeeze(box), squeeze(want)) {
			t.Fatalf("box lacks %q:\n%s", want, view(m))
		}
	}
	requireWidth(t, m, 100)
	requireAbsent(t, m, token)
	m, _ = press(t, m, keyEsc)
	requireAbsent(t, m, "Ping URL ·")
	requireText(t, m, "URL          /api/v1/heartbeat/"+hbMask, "PING URL  needs --api-listen")
}

// boxText is the text inside the modal titled title, its lines joined.
func boxText(t *testing.T, m Model, title string) string {
	t.Helper()
	g := boxGeometry(t, m, title)
	var b strings.Builder
	for _, line := range strings.Split(view(m), "\n")[g[0]+1 : g[1]] {
		inner := ansi.Cut(line, g[2]+1, g[2]+g[3]-1)
		b.WriteString(strings.TrimSpace(inner) + "\n")
	}
	return b.String()
}

// squeeze drops all white space, so wrapped text compares with its source.
func squeeze(s string) string {
	return strings.Join(strings.Fields(s), "")
}

func TestHeartbeatSplitLayoutRevealsInThePane(t *testing.T) {
	s, path, m := hbModel(t, true, 220, 40)
	if !m.splitMode() {
		t.Fatal("not split")
	}
	token := revealed(t, s, path, "backup")
	requireText(t, m, "PING URL", hbBase+hbMask)
	m, cmd := press(t, m, keyV)
	m = run(t, m, cmd)
	requireText(t, m, hbBase+token)
	m, _ = press(t, m, keyEsc)
	requireAbsent(t, m, token)
}

func TestHeartbeatEditReplacesTheTokenOnlyAfterConfirming(t *testing.T) {
	s, path, m := hbModel(t, true, 120, 40)
	old := revealed(t, s, path, "backup")
	m, _ = press(t, m, keyE)
	requireText(t, m, "Edit monitor", "Type: ‹ Heartbeat ›", "Name: backup", "Period: 1h", "Grace: 10m", "Token: ‹ keep ›")
	box := boxGeometry(t, m, "Edit monitor")
	m, _ = press(t, m, keyShiftTab, keyShiftTab, keyShiftTab, keyRight) // Schedule, Type, Token
	requireText(t, m, "Token: ‹ new ›")
	m, cmd := press(t, m, keyEnter)
	if cmd != nil {
		t.Fatal("new saved without confirming")
	}
	requireText(t, m, "Replace the token? The current URL stops working now;", "jobs that still use it look missed until updated.", "y replace · n keep the token")
	requireAbsent(t, m, "enter save")
	if got := boxGeometry(t, m, "Edit monitor"); got != box {
		t.Fatalf("confirming moved the box: %v, was %v", got, box)
	}
	if got := revealed(t, s, path, "backup"); got != old {
		t.Fatal("asking replaced the token")
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	requireText(t, m, "Token: ‹ keep ›", "enter save")
	requireAbsent(t, m, "Replace the token?")
	m, cmd = press(t, m, keyEnter)
	m = run(t, m, cmd)
	if m.form != nil || revealed(t, s, path, "backup") != old {
		t.Fatalf("keep: form %+v, token changed %v", m.form, revealed(t, s, path, "backup") != old)
	}
	requireAbsent(t, m, "uphb_", "Ping URL")

	m, _ = press(t, m, keyE, keyShiftTab, keyShiftTab, keyShiftTab, keyRight, keyEnter)
	m, cmd = press(t, m, keyY)
	m = run(t, m, cmd)
	fresh := revealed(t, s, path, "backup")
	if fresh == old {
		t.Fatal("y did not replace the token")
	}
	if found, _ := s.PingHeartbeat(context.Background(), old, false, now); found {
		t.Fatal("the old token still works")
	}
	requireText(t, m, "Ping URL · backup", "Saved. The old URL no longer works. The new one:", hbBase+fresh)
	requireAbsent(t, m, old)
	m, _ = press(t, m, keyEsc)
	requireAbsent(t, m, fresh, old)
}

func TestHeartbeatEditOfAnotherKindHasNoTokenRow(t *testing.T) {
	s, _, m := hbModel(t, true, 120, 40)
	if err := s.AddCheck(context.Background(), testChecks[0]); err != nil {
		t.Fatal(err)
	}
	m = reload(t, m)
	m, _ = press(t, m, keyDown, keyE)
	requireText(t, m, "Edit monitor", "HTTP/HTTPS URL")
	requireAbsent(t, m, "Token:")
}

// heartbeatRow is the list line of the monitor called name.
func heartbeatRow(t *testing.T, m Model, name string) string {
	t.Helper()
	for _, line := range strings.Split(view(m), "\n") {
		if strings.Contains(line, " "+name+" ") {
			return line
		}
	}
	t.Fatalf("no row %q:\n%s", name, view(m))
	return ""
}

func requireRow(t *testing.T, row string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(row, want) {
			t.Fatalf("row %q lacks %q", row, want)
		}
	}
}

func TestHeartbeatListRows(t *testing.T) {
	ctx := context.Background()
	s, path, m := hbModel(t, false, 100, 20)
	if err := s.AddCheck(ctx, testChecks[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ctx, up("api", "03:44:50")); err != nil {
		t.Fatal(err)
	}
	m = reload(t, m)
	row := heartbeatRow(t, m, "backup")
	requireRow(t, row, "backup", " hb ", "WAITING", "—", "needs --api-listen")
	requireRow(t, heartbeatRow(t, m, "api"), "UP", "84ms", "10s ago")
	if strings.Contains(heartbeatRow(t, m, "api"), " hb ") {
		t.Fatal("http row tagged hb")
	}
	if err := s.SetAPIListener(ctx, config.APIListener{Address: ":8080"}); err != nil {
		t.Fatal(err)
	}
	m = reload(t, m)
	requireRow(t, heartbeatRow(t, m, "backup"), "WAITING", "no ping yet")

	token := revealed(t, s, path, "backup")
	if _, err := s.PingHeartbeat(ctx, token, false, now.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	m = reload(t, m)
	requireRow(t, heartbeatRow(t, m, "backup"), "UP", "—", "▂", "30m ago", "next by 04:25")

	if _, err := s.PingHeartbeat(ctx, token, true, now.Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	m = reload(t, m)
	requireRow(t, heartbeatRow(t, m, "backup"), "DOWN", "▂█", "30m ago", "job sent /fail")
}

// missedStore has backup pinged at 00:45 and missed twice since (deadlines
// 01:55 and 03:05; the next is due 04:15).
func missedStore(t *testing.T) (*store.Store, Model) {
	t.Helper()
	ctx := context.Background()
	s, path, m := hbModel(t, true, 100, 40)
	ping := now.Add(-3 * time.Hour)
	if _, err := s.PingHeartbeat(ctx, revealed(t, s, path, "backup"), false, ping); err != nil {
		t.Fatal(err)
	}
	c, _ := storedByName(t, s, "backup")
	for _, r := range check.Overdue(c, ping, ping, now, time.UTC) {
		if ok, err := s.RecordMissed(ctx, c, ping, r); err != nil || !ok {
			t.Fatalf("RecordMissed = %v, %v", ok, err)
		}
		ping = r.At
	}
	return s, reload(t, m)
}

func TestHeartbeatMissedRowAndDetail(t *testing.T) {
	_, m := missedStore(t)
	requireRow(t, heartbeatRow(t, m, "backup"), "DOWN", "▂██", "3h ago", "missed · due by 04:15")
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "backup  heartbeat · every 1h · grace 10m", "last ping 3h ago", "Next due     before 04:15 (in 30m)",
		"PINGS", "one cell per period, newest right", "▂░░", "3 periods · 1 on time · ░ 2 missed · █ 0 failed",
		"Period       1h", "Grace        10m", "INCIDENTS", "RECENT ALERTS", "no ping since 00:45 UTC")
	requireAbsent(t, m, "LATENCY", "Interval", "Timeout", "Expected")
}

func TestHeartbeatWaitingDetail(t *testing.T) {
	_, _, m := hbModel(t, true, 100, 40)
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "WAITING", "no ping yet", "Next due     after the first ping", "PINGS", "No pings yet.")
}

func TestHTTPAndTCPFormsAndRowsBehaveAsBefore(t *testing.T) {
	s, m := formStore(t)
	if err := s.AddCheck(context.Background(), tcpCheckFixture); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCheck(context.Background(), testChecks[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(context.Background(), up("api", "03:44:50")); err != nil {
		t.Fatal(err)
	}
	m = sizedModel(t, m, 100, 40)
	list := strings.Split(view(m), "\n")
	wantRows := []string{
		"    MONITOR       STATUS    LATENCY  TREND (16)        LAST CHECK  MUTED",
		"▌ ● database tcp  PENDING",
		"  ● api           UP           84ms                 ▅  10s ago",
	}
	for i, want := range wantRows {
		if got := strings.TrimRight(list[3+i], " "); got != strings.TrimRight(want, " ") {
			t.Errorf("row %d = %q, want %q", i, got, want)
		}
	}
	m, _ = press(t, m, keyN)
	httpForm := []string{"Type: ‹ HTTP ›", "Name: │", "HTTP/HTTPS URL: ", "Interval: 30s", "Timeout: 5s", "Expected status: 200",
		"Channel: ‹ none ›", "Alert after: ‹ 1 › failures in a row", "Recover after: ‹ 1 › successes in a row", "TLS expiry: ‹ on ›   HTTPS only",
		"▸ Advanced  optional · space to open", "tab/shift+tab focus · enter save · esc cancel · ctrl+c quit"}
	requireText(t, m, httpForm...)
	requireAbsent(t, m, "Period", "Grace", "ping", "Token:", "api-listen")
	m, _ = press(t, m, keyShiftTab, keyRight)
	requireText(t, m, "Type: ‹ TCP ›", "Host: ", "Port: ", "Interval: 30s", "Timeout: 5s", "Checks that the port accepts a connection.",
		"Alert after: ‹ 1 › failures in a row", "Recover after: ‹ 1 › successes in a row")
	requireAbsent(t, m, "Period", "Grace", "ping", "Token:", "TLS expiry")
}

func TestHelpListsTheURLKey(t *testing.T) {
	_, _, m := hbModel(t, true, 80, 24)
	m, _ = press(t, m, keyHelp)
	box := boxText(t, m, "keys")
	if !strings.Contains(box, "v        ping URL") {
		t.Fatalf("popup lacks v:\n%s", view(m))
	}
	requireText(t, m, "database: ")
	if keys := ansi.Strip(renderKeys()); !strings.Contains(keys, "v        ping URL") {
		t.Fatalf("key list lacks v:\n%s", keys)
	}
}

func TestHeartbeatDuplicateShowsItsOwnURL(t *testing.T) {
	s, path, m := hbModel(t, true, 100, 40)
	m, _ = press(t, m, keyY)
	requireText(t, m, "New monitor · copy of backup", "Type: ‹ Heartbeat ›", "Name: backup copy", "Period: 1h", "Grace: 10m")
	requireAbsent(t, m, "Token:")
	m, cmd := press(t, m, keyEnter)
	m = run(t, m, cmd)
	copied := revealed(t, s, path, "backup copy")
	if copied == revealed(t, s, path, "backup") {
		t.Fatal("the copy shares the token")
	}
	requireText(t, m, "Ping URL · backup copy", hbBase+copied)
}
