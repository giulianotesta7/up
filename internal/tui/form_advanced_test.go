package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
)

var (
	keySpace = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	keyCtrlD = tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
)

func tabs(t *testing.T, m Model, n int) Model {
	t.Helper()
	for range n {
		m, _ = press(t, m, keyTab)
	}
	return m
}

// advancedModel is a keyed store with the new-monitor form filled and the
// focus on the Advanced row.
func advancedModel(t *testing.T) (*secret.Key, Model) {
	t.Helper()
	k, m := smtpSetup(t)
	m = fillForm(t, m, "api", "http://example.com", "30s", "5s", "200")
	return k, tabs(t, m, 5)
}

func openSealed(t *testing.T, k *secret.Key, v secret.Sealed) string {
	t.Helper()
	text, err := k.Open(v)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func TestPlainMonitorSavesWithoutOpeningAdvanced(t *testing.T) {
	s, m := formStore(t)
	m = fillForm(t, m, "api", "http://example.com", "30s", "5s", "200")
	requireText(t, m, "▸ Advanced")
	if strings.Contains(view(m), "Method") {
		t.Fatalf("fold is open by default:\n%s", view(m))
	}
	m, cmd := press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	checks, err := s.Checks(context.Background())
	if err != nil || len(checks) != 1 {
		t.Fatalf("checks %v %v", checks, err)
	}
	c := checks[0]
	if c.Method != "" || len(c.Headers) != 0 || !c.Body.Empty() || c.ExpectText != "" || c.Interval.String() != "30s" || c.Timeout.String() != "5s" || c.ExpectedStatus != 200 {
		t.Fatalf("plain monitor = %+v", c)
	}
}

func TestAdvancedFoldOpensAndSavesRequestSettings(t *testing.T) {
	k, m := advancedModel(t)
	m, _ = press(t, m, keySpace)
	requireText(t, m, "▾ Advanced", "Method: ‹ GET ›", "Request body", "Expected text", "space to add")
	// Method POST, one header, body, expected text.
	m, _ = press(t, m, keyTab, keyRight, keyRight)
	requireText(t, m, "Method: ‹ POST ›")
	m, _ = press(t, m, keyTab, keySpace)
	requireText(t, m, "Header 1 name")
	m = typeText(t, m, "Authorization")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "Bearer s3cret")
	if v := view(m); strings.Contains(v, "s3cret") || !strings.Contains(v, "•••••••••••••") {
		t.Fatalf("typed secret is visible or not masked:\n%s", v)
	}
	m, _ = press(t, m, keyTab, keyTab)
	m = typeText(t, m, `{"probe":true}`)
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "Ready")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	s := m.store.(interface {
		Checks(context.Context) ([]config.Check, error)
	})
	checks, err := s.Checks(context.Background())
	if err != nil || len(checks) != 1 {
		t.Fatalf("checks %v %v", checks, err)
	}
	c := checks[0]
	if c.Method != "POST" || c.ExpectText != "Ready" || len(c.Headers) != 1 || c.Headers[0].Name != "Authorization" {
		t.Fatalf("saved %+v", c)
	}
	if openSealed(t, k, c.Headers[0].Value) != "Bearer s3cret" || openSealed(t, k, c.Body) != `{"probe":true}` {
		t.Fatal("protected values were not sealed to the daemon key")
	}
	if strings.Contains(string(c.Body.Data), "probe") {
		t.Fatal("body stored in clear")
	}
}

func TestEditingConfiguredMonitorOpensAdvancedWithoutShowingSecrets(t *testing.T) {
	k, m := advancedModel(t)
	m, _ = press(t, m, keySpace, keyTab, keyRight, keyRight, keyTab, keySpace)
	m = typeText(t, m, "X-Token")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "hunter2")
	m, _ = press(t, m, keyTab, keyTab)
	m = typeText(t, m, "bodysecret")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	m, _ = press(t, m, keyE)
	v := view(m)
	for _, want := range []string{"▾ Advanced", "configured", "‹ POST ›", "X-Token", "set ‹ keep ›"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q:\n%s", want, v)
		}
	}
	for _, secret := range []string{"hunter2", "bodysecret"} {
		if strings.Contains(v, secret) {
			t.Fatalf("%q displayed:\n%s", secret, v)
		}
	}
	before, _ := m.store.(interface {
		Checks(context.Context) ([]config.Check, error)
	}).Checks(context.Background())

	// Keep: saving unchanged keeps the stored values.
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	after, _ := m.store.(interface {
		Checks(context.Context) ([]config.Check, error)
	}).Checks(context.Background())
	if !before[0].SameDefinition(after[0]) || openSealed(t, k, after[0].Headers[0].Value) != "hunter2" {
		t.Fatalf("keep changed the definition: %+v -> %+v", before[0], after[0])
	}

	// Replace the header value, clear the body.
	m, _ = press(t, m, keyE, keyTab, keyTab, keyTab, keyTab, keyTab, keyTab, keyTab, keyTab) // fold row
	m, _ = press(t, m, keyTab, keyTab, keyTab)                                               // header 1 value
	m = typeText(t, m, "newvalue")
	requireText(t, m, "‹ replace ›")
	m, _ = press(t, m, keyTab, keyTab, keyRight, keyRight)
	requireText(t, m, "Request body: set ‹ clear ›")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	final, _ := m.store.(interface {
		Checks(context.Context) ([]config.Check, error)
	}).Checks(context.Background())
	if openSealed(t, k, final[0].Headers[0].Value) != "newvalue" || !final[0].Body.Empty() {
		t.Fatalf("replace/clear failed: %+v", final[0])
	}
}

func TestAdvancedHeaderRemoveAndValidationErrorShown(t *testing.T) {
	_, m := advancedModel(t)
	m, _ = press(t, m, keySpace, keyTab, keyTab, keySpace)
	m = typeText(t, m, "bad name")
	m, cmd := press(t, m, keyEnter)
	if cmd != nil {
		t.Fatal("invalid header was saved")
	}
	requireText(t, m, "Cannot save monitor:", `header name "bad name" is not valid`)
	m, _ = press(t, m, keyCtrlD)
	if strings.Contains(view(m), "Header 1") {
		t.Fatalf("header not removed:\n%s", view(m))
	}
}

func TestAdvancedNeedsKeyForProtectedValues(t *testing.T) {
	_, m := formStore(t)
	m = fillForm(t, m, "api", "http://example.com", "30s", "5s", "200")
	m = tabs(t, m, 5)
	m, _ = press(t, m, keySpace, keyTab, keyTab, keySpace)
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "nope")
	requireText(t, m, "unavailable until `up serve` runs")
	if strings.Contains(view(m), "•") {
		t.Fatal("value accepted without a key")
	}
}

func TestAdvancedRenamedHeaderNeedsNewValue(t *testing.T) {
	c := config.Check{Name: "api", URL: "http://example.com", Interval: 30e9, Timeout: 5e9, ExpectedStatus: 200,
		Headers: []config.Header{{Name: "X-A", Value: secret.Sealed{Key: []byte{1}, Data: []byte{2}}}}}
	f := editMonitorForm(c, false)
	f.adv.headers[0].name = "X-B"
	if _, err := f.check(); err == nil || !strings.Contains(err.Error(), `header "X-B" was renamed`) {
		t.Fatalf("err = %v", err)
	}
}

func TestTallAdvancedFormScrollsAndKeepsFooterAt80x24(t *testing.T) {
	_, m := advancedModel(t)
	m.width, m.height = 80, 24
	m, _ = press(t, m, keySpace, keyTab, keyTab)
	for range 6 {
		m, _ = press(t, m, keySpace)
		m = typeText(t, m, "X-H")
		m, _ = press(t, m, keyTab, keyTab) // value, then the next add row
	}
	m, _ = press(t, m, keyShiftTab, keyShiftTab, keyShiftTab)
	out := view(m)
	lines := strings.Split(out, "\n")
	if len(lines) > 24 {
		t.Fatalf("%d lines exceed 24:\n%s", len(lines), out)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > 80 {
			t.Fatalf("line wider than 80: %q", line)
		}
	}
	for _, want := range []string{"╭─ New monitor", "╰", "enter save · esc cancel · ctrl+c quit", " more", "> "} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "Header 5 value") && !strings.Contains(out, "Header 6 name") {
		t.Fatalf("focused row scrolled out of view:\n%s", out)
	}
	// The same form is untouched when it fits.
	m.height = 40
	if strings.Contains(view(m), " more") {
		t.Fatalf("scroll marker shown without need:\n%s", view(m))
	}
}

func TestDetailShowsMethodAndExpectedTextWithoutSecrets(t *testing.T) {
	c := config.Check{Name: "api", URL: "https://example.com", Interval: 30e9, Timeout: 5e9, ExpectedStatus: 200,
		Method: "POST", ExpectText: "Ready", Headers: []config.Header{{Name: "X", Value: secret.Sealed{Key: []byte{1}, Data: []byte("sealed-data")}}}, Body: secret.Sealed{Key: []byte{1}, Data: []byte("sealed-body")}}
	out := ansi.Strip(renderDetail(Snapshot{}, Service{Check: c}, now, nil, 80))
	if !strings.Contains(out, "Method      POST") || !strings.Contains(out, `Text        contains "Ready"`) || strings.Contains(out, "sealed") {
		t.Fatalf("detail:\n%s", out)
	}
	plain := ansi.Strip(renderDetail(Snapshot{}, Service{Check: config.Check{Name: "api", URL: "https://example.com", Interval: 30e9, Timeout: 5e9, ExpectedStatus: 200}}, now, nil, 80))
	if strings.Contains(plain, "Method") || strings.Contains(plain, "Text ") {
		t.Fatalf("plain detail shows request fields:\n%s", plain)
	}
}

func TestAdvancedDuplicateHeaderShownInForm(t *testing.T) {
	_, m := advancedModel(t)
	m, _ = press(t, m, keySpace, keyTab, keyTab, keySpace)
	m = typeText(t, m, "X-A")
	m, _ = press(t, m, keyTab, keyTab, keySpace)
	m = typeText(t, m, "x-a")
	m, cmd := press(t, m, keyEnter)
	if cmd != nil {
		t.Fatal("duplicate headers were saved")
	}
	requireText(t, m, "Cannot save monitor:", `header "x-a" is repeated`)
}
