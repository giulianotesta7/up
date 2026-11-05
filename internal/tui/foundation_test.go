package tui

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
)

func TestLoadCarriesMaintenanceAndChannelTests(t *testing.T) {
	s, _ := formStore(t)
	ctx := context.Background()
	c := config.Check{Name: "api", URL: "https://example.com", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200,
		Method: "POST", Headers: []config.Header{{Name: "X", Value: secret.Sealed{Key: []byte{1}, Data: []byte{2}}, Edit: config.EditReplace}}}
	if err := s.AddCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCheck(ctx, config.Check{Name: "web", URL: "https://example.com/web", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200}); err != nil {
		t.Fatal(err)
	}
	// 2026-10-25 03:45 UTC is a Sunday: 03:00-05:00 UTC is inside the window.
	w, err := s.AddMaintenanceWindow(ctx, config.MaintenanceWindow{Check: "api", Weekdays: []time.Weekday{time.Sunday}, StartMinute: 3 * 60, Duration: 2 * time.Hour, Zone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(ctx, config.Channel{Name: "hook", Kind: config.KindWebhook, WebhookOrigin: "https://h.example.com", WebhookURL: secret.Sealed{Key: []byte{1}, Data: []byte{1}}, WebhookURLEdit: config.EditReplace}); err != nil {
		t.Fatal(err)
	}
	test, err := s.EnqueueChannelTest(ctx, "hook", now)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := Load(ctx, s, now)
	if err != nil {
		t.Fatal(err)
	}
	api, web := snap.Services[0], snap.Services[1]
	if api.Check.Method != "POST" || len(api.Check.Headers) != 1 || api.Check.Headers[0].Name != "X" {
		t.Fatalf("check fields missing from the snapshot: %+v", api.Check)
	}
	if !reflect.DeepEqual(api.Maintenance, []config.MaintenanceWindow{w}) || !api.InMaintenance.Active || !api.InMaintenance.End.Equal(now.Add(75*time.Minute)) {
		t.Fatalf("api maintenance = %+v, %+v", api.Maintenance, api.InMaintenance)
	}
	if len(web.Maintenance) != 0 || web.InMaintenance.Active {
		t.Fatalf("web maintenance = %+v, %+v", web.Maintenance, web.InMaintenance)
	}
	if len(snap.ChannelTests) != 1 || snap.ChannelTests[0].ID != test.ID || snap.ChannelTests[0].Status != config.TestPending {
		t.Fatalf("channel tests = %+v", snap.ChannelTests)
	}
	if snap.Channels[0].Kind != config.KindWebhook || snap.Channels[0].WebhookOrigin != "https://h.example.com" {
		t.Fatalf("channels = %+v", snap.Channels)
	}
}

// A store without the optional readers still loads, with no maintenance or tests.
func TestLoadWithoutOptionalReaders(t *testing.T) {
	s, _ := formStore(t)
	ctx := context.Background()
	if err := s.AddCheck(ctx, config.Check{Name: "api", URL: "https://example.com", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200}); err != nil {
		t.Fatal(err)
	}
	// Embedding the interface hides every method outside Store.
	snap, err := Load(ctx, struct{ Store }{s}, now)
	if err != nil || len(snap.Services) != 1 || len(snap.ChannelTests) != 0 || len(snap.Services[0].Maintenance) != 0 || snap.Services[0].InMaintenance.Active {
		t.Fatalf("snapshot = %+v, %v", snap, err)
	}
}

// The local store and the remote client both offer the optional write APIs.
var (
	_ MaintenanceStore  = (*store.Store)(nil)
	_ ChannelTester     = (*store.Store)(nil)
	_ MaintenanceReader = (*store.Store)(nil)
	_ ChannelTestReader = (*store.Store)(nil)
)

// The monitor form shows only some fields; editing must not wipe the rest.
func TestEditingAMonitorKeepsFieldsTheFormDoesNotShow(t *testing.T) {
	sealed := secret.Sealed{Key: []byte{1}, Data: []byte{2}}
	c := config.Check{Name: "api", URL: "https://example.com", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200,
		Method: "POST", Headers: []config.Header{{Name: "X", Value: sealed}}, Body: sealed, ExpectText: "pong"}
	f := editMonitorForm(c, false)
	f.values[1] = "https://example.com/changed"
	got, err := f.check()
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://example.com/changed" || got.Method != "POST" || got.ExpectText != "pong" || len(got.Headers) != 1 || got.Headers[0].Name != "X" || got.Headers[0].Edit != config.EditKeep || got.BodyEdit != config.EditKeep {
		t.Fatalf("check() = %+v", got)
	}
	// A new monitor starts from nothing.
	fresh := newMonitorForm(Snapshot{})
	fresh.values[0], fresh.values[1] = "web", "https://example.com"
	if got, err := fresh.check(); err != nil || got.Method != "" || got.Headers != nil || got.Kind != "" {
		t.Fatalf("new check() = %+v, %v", got, err)
	}
}
