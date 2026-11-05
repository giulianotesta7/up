package store_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
)

func webhook(name string) config.Channel {
	return config.Channel{Name: name, Kind: config.KindWebhook, WebhookOrigin: "https://hooks.example.com",
		WebhookURL: seal(1, 1), WebhookURLEdit: config.EditReplace,
		WebhookAuthName: "Authorization", WebhookAuth: seal(2, 2), WebhookAuthEdit: config.EditReplace}
}

func storedChannel(t *testing.T, s *store.Store, name string) config.Channel {
	t.Helper()
	c, ok, err := s.Channel(ctx, name)
	if err != nil || !ok {
		t.Fatalf("Channel(%q) = %v, %v", name, ok, err)
	}
	return c
}

func TestWebhookChannelRoundTripAndEdits(t *testing.T) {
	s, path := seeded(t)
	if err := s.AddChannel(ctx, webhook("hook")); err != nil {
		t.Fatal(err)
	}
	got := storedChannel(t, s, "hook")
	want := webhook("hook")
	want.WebhookURLEdit, want.WebhookAuthEdit = config.EditKeep, config.EditKeep
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, want)
	}
	all, err := s.Channels(ctx)
	if err != nil || len(all) != 1 || !reflect.DeepEqual(all[0], want) {
		t.Fatalf("Channels() = %+v, %v", all, err)
	}
	update := func(edit func(*config.Channel)) config.Channel {
		t.Helper()
		c := storedChannel(t, s, "hook")
		edit(&c)
		if err := s.UpdateChannel(ctx, c); err != nil {
			t.Fatal(err)
		}
		return storedChannel(t, s, "hook")
	}
	// Keep retains stored values even when the caller holds other bytes.
	got = update(func(c *config.Channel) {
		c.WebhookURL, c.WebhookAuth = seal(9), seal(9)
		c.WebhookOrigin = "https://other.example.com"
	})
	if !reflect.DeepEqual(got.WebhookURL, seal(1, 1)) || !reflect.DeepEqual(got.WebhookAuth, seal(2, 2)) || got.WebhookOrigin != "https://other.example.com" {
		t.Fatalf("keep: %+v", got)
	}
	got = update(func(c *config.Channel) { c.WebhookURL, c.WebhookURLEdit = seal(5), config.EditReplace })
	if !reflect.DeepEqual(got.WebhookURL, seal(5)) || !reflect.DeepEqual(got.WebhookAuth, seal(2, 2)) {
		t.Fatalf("replace url: %+v", got)
	}
	got = update(func(c *config.Channel) { c.WebhookAuthEdit = config.EditClear })
	if !got.WebhookAuth.Empty() || got.WebhookAuthName != "Authorization" || !reflect.DeepEqual(got.WebhookURL, seal(5)) {
		t.Fatalf("clear auth: %+v", got)
	}
	// The URL cannot be cleared, and a rejected write changes nothing.
	before := diskState(t, path)
	bad := storedChannel(t, s, "hook")
	bad.WebhookURLEdit = config.EditClear
	if err := s.UpdateChannel(ctx, bad); !errors.Is(err, store.ErrValidation) || !strings.Contains(err.Error(), "webhook URL cannot be cleared") {
		t.Fatalf("clear url: %v", err)
	}
	if !reflect.DeepEqual(diskState(t, path), before) {
		t.Fatal("rejected update changed the database")
	}
}

func TestChannelKindSwitchesNeedTheirFields(t *testing.T) {
	s, _ := seeded(t)
	if err := s.AddSMTPServer(ctx, config.SMTPServer{Name: "s", Host: "h", Port: 25, TLS: config.TLSNone, Sender: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	// A webhook is created with a URL; keep without one has nothing to keep.
	noURL := webhook("hook")
	noURL.WebhookURL, noURL.WebhookURLEdit = secret.Sealed{}, config.EditKeep
	if err := s.AddChannel(ctx, noURL); !errors.Is(err, store.ErrValidation) || !strings.Contains(err.Error(), "webhook URL is required") {
		t.Fatalf("AddChannel without URL = %v", err)
	}
	// An email channel cannot become a webhook without supplying the URL.
	email := config.Channel{Name: "mail", Kind: config.KindEmail, SMTPServer: "s", Recipients: []string{"a@example.com"}}
	if err := s.AddChannel(ctx, email); err != nil {
		t.Fatal(err)
	}
	to := webhook("mail")
	to.WebhookURL, to.WebhookURLEdit = secret.Sealed{}, config.EditKeep
	if err := s.UpdateChannel(ctx, to); !errors.Is(err, store.ErrValidation) {
		t.Fatalf("keep url on a former email channel = %v", err)
	}
	if err := s.UpdateChannel(ctx, webhook("mail")); err != nil {
		t.Fatal(err)
	}
	if got := storedChannel(t, s, "mail"); got.Kind != config.KindWebhook || got.SMTPServer != "" || got.Recipients != nil {
		t.Fatalf("converted channel = %+v", got)
	}
	// And back: webhook values are dropped.
	if err := s.UpdateChannel(ctx, email); err != nil {
		t.Fatal(err)
	}
	if got := storedChannel(t, s, "mail"); !reflect.DeepEqual(got, email) {
		t.Fatalf("channel back to email = %+v", got)
	}
	// A webhook channel does not need an SMTP server, and the server can go.
	if err := s.AddChannel(ctx, webhook("hook")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteChannel(ctx, "mail"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSMTPServer(ctx, "s"); err != nil {
		t.Fatalf("webhook channel blocked deleting an SMTP server: %v", err)
	}
}

func TestMaintenanceWindowCRUD(t *testing.T) {
	s, path := seeded(t)
	w := config.MaintenanceWindow{Check: "api", Weekdays: []time.Weekday{time.Saturday, time.Monday}, StartMinute: 90, Duration: 2 * time.Hour, Zone: "America/New_York"}
	added, err := s.AddMaintenanceWindow(ctx, w)
	if err != nil || added.ID == 0 {
		t.Fatalf("AddMaintenanceWindow = %+v, %v", added, err)
	}
	if want := (config.MaintenanceWindow{ID: added.ID, Check: "api", Weekdays: []time.Weekday{time.Monday, time.Saturday}, StartMinute: 90, Duration: 2 * time.Hour, Zone: "America/New_York"}); !reflect.DeepEqual(added, want) {
		t.Fatalf("added = %+v, want %+v (weekdays sorted)", added, want)
	}
	other, err := s.AddMaintenanceWindow(ctx, config.MaintenanceWindow{Check: "web", Weekdays: []time.Weekday{0}, Duration: time.Hour, Zone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.MaintenanceWindows(ctx, "api")
	if err != nil || !reflect.DeepEqual(got, []config.MaintenanceWindow{added}) {
		t.Fatalf("MaintenanceWindows(api) = %+v, %v", got, err)
	}
	all, err := s.AllMaintenanceWindows(ctx)
	if err != nil || !reflect.DeepEqual(all, []config.MaintenanceWindow{added, other}) {
		t.Fatalf("AllMaintenanceWindows = %+v, %v", all, err)
	}
	added.Duration, added.Weekdays = 30*time.Minute, []time.Weekday{time.Friday}
	if err := s.UpdateMaintenanceWindow(ctx, added); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.MaintenanceWindows(ctx, "api"); !reflect.DeepEqual(got, []config.MaintenanceWindow{added}) {
		t.Fatalf("after update = %+v", got)
	}

	// Rejections change nothing and use the store's error kinds.
	before := diskState(t, path)
	invalid := added
	invalid.Zone = "Mars/Base"
	missingCheck := added
	missingCheck.Check = "ghost"
	wrongOwner := added
	wrongOwner.Check = "web"
	missingID := added
	missingID.ID = 999
	for name, tc := range map[string]struct {
		err  error
		kind error
	}{
		"add invalid":        {func() error { _, e := s.AddMaintenanceWindow(ctx, invalid); return e }(), store.ErrValidation},
		"add unknown check":  {func() error { _, e := s.AddMaintenanceWindow(ctx, missingCheck); return e }(), store.ErrNotFound},
		"update invalid":     {s.UpdateMaintenanceWindow(ctx, invalid), store.ErrValidation},
		"update wrong check": {s.UpdateMaintenanceWindow(ctx, wrongOwner), store.ErrNotFound},
		"update unknown id":  {s.UpdateMaintenanceWindow(ctx, missingID), store.ErrNotFound},
		"delete wrong check": {s.DeleteMaintenanceWindow(ctx, "web", added.ID), store.ErrNotFound},
		"delete unknown id":  {s.DeleteMaintenanceWindow(ctx, "api", 999), store.ErrNotFound},
	} {
		if !errors.Is(tc.err, tc.kind) {
			t.Errorf("%s: %v, want %v", name, tc.err, tc.kind)
		}
	}
	if !reflect.DeepEqual(diskState(t, path), before) {
		t.Fatal("rejected writes changed the database")
	}

	// IDs are never reused, and deleting a check deletes its windows.
	if err := s.DeleteMaintenanceWindow(ctx, "api", added.ID); err != nil {
		t.Fatal(err)
	}
	again, err := s.AddMaintenanceWindow(ctx, w)
	if err != nil || again.ID <= other.ID {
		t.Fatalf("new id %d after delete, want greater than %d (%v)", again.ID, other.ID, err)
	}
	if err := s.DeleteCheck(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.MaintenanceWindows(ctx, "web"); err != nil || len(got) != 0 {
		t.Fatalf("windows of a deleted check = %+v, %v", got, err)
	}
}

func TestMaintenanceWindowsPerCheckLimit(t *testing.T) {
	s, _ := seeded(t)
	w := config.MaintenanceWindow{Check: "api", Weekdays: []time.Weekday{1}, Duration: time.Hour, Zone: "UTC"}
	for range store.MaxMaintenanceWindows {
		if _, err := s.AddMaintenanceWindow(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddMaintenanceWindow(ctx, w); !errors.Is(err, store.ErrValidation) || !strings.Contains(err.Error(), "at most 20 windows") {
		t.Fatalf("21st window = %v", err)
	}
	if _, err := s.AddMaintenanceWindow(ctx, config.MaintenanceWindow{Check: "web", Weekdays: []time.Weekday{1}, Duration: time.Hour, Zone: "UTC"}); err != nil {
		t.Fatalf("another check is limited too: %v", err)
	}
}

func TestChannelTestLifecycle(t *testing.T) {
	s, path := seeded(t)
	if err := s.AddChannel(ctx, webhook("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(ctx, webhook("b")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueChannelTest(ctx, "ghost", at); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown channel = %v", err)
	}
	if _, ok, err := s.LatestChannelTest(ctx, "a"); err != nil || ok {
		t.Fatalf("latest before any = %v, %v", ok, err)
	}
	a, err := s.EnqueueChannelTest(ctx, "a", at)
	if err != nil || a.ID == 0 || a.Status != config.TestPending || a.Channel != "a" || !a.Requested.Equal(at) || !a.Started.IsZero() || !a.Finished.IsZero() || a.Error != "" {
		t.Fatalf("enqueue = %+v, %v", a, err)
	}
	// One active request per channel, pending or sending; others are free.
	before := diskState(t, path)
	if _, err := s.EnqueueChannelTest(ctx, "a", at); !errors.Is(err, store.ErrExists) {
		t.Fatalf("second pending = %v", err)
	}
	if !reflect.DeepEqual(diskState(t, path), before) {
		t.Fatal("rejected enqueue changed the database")
	}
	b, err := s.EnqueueChannelTest(ctx, "b", at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	// Claim is oldest first and marks the request sending.
	claimed, ok, err := s.ClaimChannelTest(ctx, at.Add(time.Minute))
	if err != nil || !ok || claimed.ID != a.ID || claimed.Status != config.TestSending || !claimed.Started.Equal(at.Add(time.Minute)) {
		t.Fatalf("claim = %+v, %v, %v", claimed, ok, err)
	}
	if _, err := s.EnqueueChannelTest(ctx, "a", at); !errors.Is(err, store.ErrExists) {
		t.Fatalf("enqueue while sending = %v", err)
	}
	if next, ok, _ := s.ClaimChannelTest(ctx, at); !ok || next.ID != b.ID {
		t.Fatalf("second claim = %+v, %v", next, ok)
	}
	if _, ok, err := s.ClaimChannelTest(ctx, at); err != nil || ok {
		t.Fatalf("claim with nothing pending = %v, %v", ok, err)
	}
	// Completion: ok, then error with sanitized text; only a sending test completes.
	done := at.Add(2 * time.Minute)
	if err := s.CompleteChannelTest(ctx, a.ID, "", done); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.LatestChannelTest(ctx, "a")
	if err != nil || !ok || got.Status != config.TestOK || got.Error != "" || !got.Finished.Equal(done) || !got.Started.Equal(at.Add(time.Minute)) {
		t.Fatalf("after ok = %+v, %v, %v", got, ok, err)
	}
	if err := s.CompleteChannelTest(ctx, a.ID, "again", done); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("completing a finished test = %v", err)
	}
	if err := s.CompleteChannelTest(ctx, 999, "x", done); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("completing an unknown test = %v", err)
	}
	long := "dial tcp\x1b[31m 10.0.0.1:25:\n\tconnection refused " + strings.Repeat("x", 500)
	if err := s.CompleteChannelTest(ctx, b.ID, long, done); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.LatestChannelTest(ctx, "b")
	if got.Status != config.TestError || strings.ContainsAny(got.Error, "\x1b\n\t") || len([]rune(got.Error)) > store.MaxChannelTestError || !strings.HasPrefix(got.Error, "dial tcp [31m 10.0.0.1:25: connection refused") {
		t.Fatalf("error text = %q", got.Error)
	}
	if err := s.CompleteChannelTest(ctx, a.ID, "", done); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("finished test completed twice")
	}
	// Finished tests free the channel and history keeps the newest per channel.
	for i := range 3 {
		n, err := s.EnqueueChannelTest(ctx, "a", at.Add(time.Duration(10+i)*time.Minute))
		if err != nil {
			t.Fatalf("enqueue after finish: %v", err)
		}
		if _, ok, _ := s.ClaimChannelTest(ctx, at); !ok {
			t.Fatal("nothing to claim")
		}
		if err := s.CompleteChannelTest(ctx, n.ID, "", at); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := s.LatestChannelTests(ctx)
	if err != nil || len(latest) != 2 || latest[0].Channel != "a" || latest[1].Channel != "b" || latest[0].Status != config.TestOK {
		t.Fatalf("LatestChannelTests = %+v, %v", latest, err)
	}
	// An empty error text from a failure still reads as a failure.
	n, _ := s.EnqueueChannelTest(ctx, "a", at)
	s.ClaimChannelTest(ctx, at)
	if err := s.CompleteChannelTest(ctx, n.ID, "\x1b\x07", at); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.LatestChannelTest(ctx, "a"); got.Status != config.TestError || got.Error != "test failed" {
		t.Fatalf("control-only error = %+v", got)
	}
}

func TestChannelTestHistoryIsBoundedAndDeletedWithTheChannel(t *testing.T) {
	s, _ := seeded(t)
	if err := s.AddChannel(ctx, webhook("a")); err != nil {
		t.Fatal(err)
	}
	for range store.MaxChannelTestHistory + 5 {
		n, err := s.EnqueueChannelTest(ctx, "a", at)
		if err != nil {
			t.Fatal(err)
		}
		s.ClaimChannelTest(ctx, at)
		if err := s.CompleteChannelTest(ctx, n.ID, "", at); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.CountChannelTests(ctx, "a"); err != nil || n != store.MaxChannelTestHistory {
		t.Fatalf("history = %d, %v, want %d", n, err, store.MaxChannelTestHistory)
	}
	if _, err := s.EnqueueChannelTest(ctx, "a", at); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteChannel(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountChannelTests(ctx, "a"); err != nil || n != 0 {
		t.Fatalf("tests after deleting the channel = %d, %v", n, err)
	}
}

func TestFailSendingChannelTestsRecoversAfterACrash(t *testing.T) {
	s, _ := seeded(t)
	if err := s.AddChannel(ctx, webhook("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(ctx, webhook("b")); err != nil {
		t.Fatal(err)
	}
	s.EnqueueChannelTest(ctx, "a", at)
	s.ClaimChannelTest(ctx, at)
	s.EnqueueChannelTest(ctx, "b", at)
	n, err := s.FailSendingChannelTests(ctx, "interrupted by a restart", at.Add(time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("FailSendingChannelTests = %d, %v", n, err)
	}
	a, _, _ := s.LatestChannelTest(ctx, "a")
	b, _, _ := s.LatestChannelTest(ctx, "b")
	if a.Status != config.TestError || a.Error != "interrupted by a restart" || b.Status != config.TestPending {
		t.Fatalf("a = %+v, b = %+v", a, b)
	}
	if _, err := s.EnqueueChannelTest(ctx, "a", at); err != nil {
		t.Fatalf("channel stayed blocked: %v", err)
	}
}
