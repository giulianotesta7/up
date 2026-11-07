package client

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/tui"
)

var kY = tea.KeyPressMsg{Code: 'y', Text: "y"}

// duplicateBackend seeds a monitor with sealed values, history, a mute, a
// maintenance window and an alert streak, plus a second monitor that sorts
// after it, and returns the driver on a loaded model.
func duplicateBackend(t *testing.T, backend func(*testing.T) webhookBackend) (webhookBackend, *formDriver, config.Check) {
	t.Helper()
	b := backend(t)
	ctx := context.Background()
	seal := func(text string) secret.Sealed {
		v, err := secret.Seal(b.key.PublicKey(), text)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	src := config.Check{Name: "api", URL: "https://example.com/health", Interval: 45 * time.Second, Timeout: 7 * time.Second, ExpectedStatus: 204,
		AlertAfter: 3, RecoverAfter: 2, TLSOff: true, Method: "POST", ExpectText: "pong",
		Headers: []config.Header{{Name: "Authorization", Value: seal("Bearer s3cret"), Edit: config.EditReplace}, {Name: "X-Empty"}},
		Body:    seal("body-s3cret"), BodyEdit: config.EditReplace, Conditions: sourceConditions}
	other := config.Check{Name: "web", URL: "https://example.org", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200}
	for _, c := range []config.Check{src, other} {
		if err := b.s.AddCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := b.s.Record(ctx, check.Result{CheckName: "api", At: now.Add(-time.Minute), Status: check.StatusDown, StatusCode: 503, Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	if err := b.s.SetMute(ctx, "api", now.Add(time.Hour), true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.s.AddMaintenanceWindow(ctx, config.MaintenanceWindow{Check: "api", Weekdays: []time.Weekday{time.Monday}, StartMinute: 60, Duration: time.Hour, Zone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	if err := b.s.SetAlertState(ctx, "api", true); err != nil {
		t.Fatal(err)
	}
	m := b.m
	next, _ := m.Update(m.Init()())
	return b, &formDriver{t: t, m: next.(tui.Model)}, src
}

func (b webhookBackend) stored(t *testing.T, name string) (config.Check, bool) {
	t.Helper()
	list, err := b.s.Checks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.Name == name {
			return c, true
		}
	}
	return config.Check{}, false
}

func (b webhookBackend) count(t *testing.T) int {
	t.Helper()
	list, err := b.s.Checks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

func TestDuplicateFormShowsCopyAndNeverRendersSecrets(t *testing.T) {
	for name, backend := range map[string]func(*testing.T) webhookBackend{"local": localBackend, "remote": remoteBackend} {
		t.Run(name, func(t *testing.T) {
			_, d, src := duplicateBackend(t, backend)
			d.press(kY)
			out := d.screen()
			for _, want := range []string{"New monitor · copy of api", "Name: api copy│", "https://example.com/health", "45s", "7s", "204", "‹ 3 › failures", "‹ 2 › successes", "‹ off ›", "▾ Advanced", "Method: ‹ POST ›", "Header 1 name: Authorization", "Header 1 value: set ‹ keep ›", "Header 2 value: ", "Request body: set ‹ keep ›", "Expected text: pong"} {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q:\n%s", want, out)
				}
			}
			for _, secretText := range []string{"s3cret", base64.StdEncoding.EncodeToString(src.Body.Data), string(src.Body.Data)} {
				if strings.Contains(out, secretText) {
					t.Errorf("form shows %q:\n%s", secretText, out)
				}
			}
		})
	}
}

func TestDuplicateSavesACopyWithSealedValuesAndNoState(t *testing.T) {
	for name, backend := range map[string]func(*testing.T) webhookBackend{"local": localBackend, "remote": remoteBackend} {
		t.Run(name, func(t *testing.T) {
			b, d, _ := duplicateBackend(t, backend)
			ctx := context.Background()
			original, _ := b.stored(t, "api")
			d.press(kY)
			d.save()
			got, ok := b.stored(t, "api copy")
			if !ok || b.count(t) != 3 {
				t.Fatalf("copy missing, %d monitors", b.count(t))
			}
			want := original
			want.Name = "api copy"
			if !got.SameDefinition(want) {
				t.Fatalf("copy = %+v\nwant %+v", got, want)
			}
			// The same sealed bytes, still readable with the server key.
			if string(got.Body.Data) != string(original.Body.Data) || b.open(t, got.Headers[0].Value) != "Bearer s3cret" || b.open(t, got.Body) != "body-s3cret" || !got.Headers[1].Value.Empty() {
				t.Fatalf("sealed values = %+v", got)
			}
			if recent, _ := b.s.Recent(ctx, "api copy", 10); len(recent) != 0 {
				t.Fatalf("history copied: %+v", recent)
			}
			if outages, _ := b.s.Outages(ctx, "api copy", 10); len(outages) != 0 {
				t.Fatalf("outages copied: %+v", outages)
			}
			if mutes, _ := b.s.Mutes(ctx); len(mutes) != 1 || mutes[0].Check != "api" {
				t.Fatalf("mutes = %+v", mutes)
			}
			if windows, _ := b.s.AllMaintenanceWindows(ctx); len(windows) != 1 || windows[0].Check != "api" {
				t.Fatalf("windows = %+v", windows)
			}
			if known, _ := b.s.AlertStates(ctx); known["api copy"] || !known["api"] {
				t.Fatalf("alert states = %v", known)
			}
			if after, _ := b.stored(t, "api"); !after.SameDefinition(original) {
				t.Fatal("the original changed")
			}
			// A second copy of the same monitor takes the next free name.
			d.press(tea.KeyPressMsg{Code: tea.KeyUp}, tea.KeyPressMsg{Code: tea.KeyUp}, kY)
			if out := d.screen(); !strings.Contains(out, "copy of api ─") || !strings.Contains(out, "Name: api copy 2│") {
				t.Fatalf("second copy name:\n%s", out)
			}
		})
	}
}

func TestDuplicateCancelLeavesEverythingUnchanged(t *testing.T) {
	for name, backend := range map[string]func(*testing.T) webhookBackend{"local": localBackend, "remote": remoteBackend} {
		t.Run(name, func(t *testing.T) {
			b, d, _ := duplicateBackend(t, backend)
			d.press(kY, kTab)
			d.typ("changed")
			d.press(kEsc)
			if out := d.screen(); strings.Contains(out, "copy of") || strings.Contains(out, "New monitor") {
				t.Fatalf("form still open:\n%s", out)
			}
			if _, ok := b.stored(t, "api copy"); ok || b.count(t) != 2 {
				t.Fatalf("cancel stored a monitor (%d)", b.count(t))
			}
		})
	}
}

func TestDuplicateReplaceAndClearOverrideTheOriginal(t *testing.T) {
	for name, backend := range map[string]func(*testing.T) webhookBackend{"local": localBackend, "remote": remoteBackend} {
		t.Run(name, func(t *testing.T) {
			b, d, _ := duplicateBackend(t, backend)
			original, _ := b.stored(t, "api")
			d.press(kY)
			// Name, URL, interval, timeout, status, channel, alert, recover,
			// TLS, Advanced, Method, Header 1 name → Header 1 value.
			for range 12 {
				d.press(kTab)
			}
			d.typ("new-secret")
			// Body: past the second header (name, value), add row, then body.
			d.press(kTab, kTab, kTab, kTab, kRight, kRight)
			d.save()
			got, ok := b.stored(t, "api copy")
			if !ok {
				t.Fatalf("copy missing:\n%s", d.screen())
			}
			if b.open(t, got.Headers[0].Value) != "new-secret" || !got.Body.Empty() {
				t.Fatalf("copy = %+v", got)
			}
			if after, _ := b.stored(t, "api"); !after.SameDefinition(original) || b.open(t, after.Body) != "body-s3cret" {
				t.Fatal("the original changed")
			}
		})
	}
}

func TestDuplicateOfAMonitorDeletedBeforeSavingStoresNothing(t *testing.T) {
	for name, backend := range map[string]func(*testing.T) webhookBackend{"local": localBackend, "remote": remoteBackend} {
		t.Run(name, func(t *testing.T) {
			b, d, _ := duplicateBackend(t, backend)
			d.press(kY)
			if err := b.s.DeleteCheck(context.Background(), "api"); err != nil {
				t.Fatal(err)
			}
			out := d.save()
			if !strings.Contains(out, "Cannot save monitor") || !strings.Contains(out, `source monitor "api" not found`) || !strings.Contains(out, "copy of api") {
				t.Fatalf("form after a missing source:\n%s", out)
			}
			if _, ok := b.stored(t, "api copy"); ok || b.count(t) != 1 {
				t.Fatalf("stored %d monitors", b.count(t))
			}
		})
	}
}

func TestNewAndEditFormsStillBehaveAsBefore(t *testing.T) {
	for name, backend := range map[string]func(*testing.T) webhookBackend{"local": localBackend, "remote": remoteBackend} {
		t.Run(name, func(t *testing.T) {
			b, d, _ := duplicateBackend(t, backend)
			d.press(kN)
			if out := d.screen(); !strings.Contains(out, "New monitor") || strings.Contains(out, "copy of") || !strings.Contains(out, "Name: │") {
				t.Fatalf("new form:\n%s", out)
			}
			d.press(kEsc, kE)
			out := d.screen()
			if !strings.Contains(out, "Edit monitor") || strings.Contains(out, "copy") || !strings.Contains(out, "Header 1 value: set ‹ keep ›") {
				t.Fatalf("edit form:\n%s", out)
			}
			d.save()
			if got, _ := b.stored(t, "api"); b.open(t, got.Body) != "body-s3cret" || b.count(t) != 2 {
				t.Fatalf("edit keep changed the monitor: %+v", got)
			}
		})
	}
}
