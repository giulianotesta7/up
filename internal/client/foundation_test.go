package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
)

func sealTo(t *testing.T, key *secret.Key, plain string) secret.Sealed {
	t.Helper()
	v, err := secret.Seal(key.PublicKey(), plain)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Every new field travels client -> API -> store -> snapshot -> client, with
// protected values sealed on the way in and only "set" on the way back.
func TestAdvancedChecksRoundTripThroughTheRealAPI(t *testing.T) {
	c, s, key := realServer(t)
	ctx := context.Background()
	adv := newCheck("api")
	adv.Method = "PUT"
	adv.Headers = []config.Header{{Name: "Authorization", Value: sealTo(t, key, "Bearer hunter2"), Edit: config.EditReplace}, {Name: "Accept", Value: sealTo(t, key, "application/json"), Edit: config.EditReplace}}
	adv.Body, adv.BodyEdit = sealTo(t, key, `{"ping":true}`), config.EditReplace
	adv.ExpectText = "pong"
	tcp := config.Check{Name: "db", Kind: config.CheckTCP, Host: "2001:db8::1", Port: 5432, Interval: time.Hour, Timeout: time.Second}
	for _, v := range []config.Check{adv, tcp} {
		if err := c.AddCheck(ctx, v); err != nil {
			t.Fatalf("AddCheck(%s) = %v", v.Name, err)
		}
	}
	// The stored values are what the daemon can open; the plain text never reached the store.
	stored, _ := s.Checks(ctx)
	if got, err := key.Open(stored[0].Headers[0].Value); err != nil || got != "Bearer hunter2" {
		t.Fatalf("stored header = %q, %v", got, err)
	}
	if got, err := key.Open(stored[0].Body); err != nil || got != `{"ping":true}` {
		t.Fatalf("stored body = %q, %v", got, err)
	}
	snap, err := c.LoadSnapshot(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	api, db := snap.Services[0].Check, snap.Services[1].Check
	if api.Method != "PUT" || api.ExpectText != "pong" || len(api.Headers) != 2 || api.Headers[0].Name != "Authorization" || api.Headers[0].Value.Empty() || api.Body.Empty() {
		t.Fatalf("snapshot check = %+v", api)
	}
	if db.Kind != config.CheckTCP || db.Host != "2001:db8::1" || db.Port != 5432 {
		t.Fatalf("snapshot tcp = %+v", db)
	}
	for _, h := range api.Headers {
		if strings.Contains(string(h.Value.Data), "hunter2") {
			t.Fatal("snapshot carries a value")
		}
	}

	// Edit through the client with the snapshot's check: keep is the default.
	edited := api
	edited.Headers = []config.Header{{Name: "Authorization"}, {Name: "Accept", Edit: config.EditClear}}
	edited.ExpectText = "pong!"
	if err := c.UpdateCheck(ctx, edited); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Checks(ctx)
	if got, _ := key.Open(after[0].Headers[0].Value); got != "Bearer hunter2" || !after[0].Headers[1].Value.Empty() || after[0].Body.Empty() || after[0].ExpectText != "pong!" {
		t.Fatalf("after edit = %+v", after[0])
	}
	edited.Headers, edited.BodyEdit = []config.Header{{Name: "Authorization", Value: sealTo(t, key, "Bearer new"), Edit: config.EditReplace}}, config.EditClear
	if err := c.UpdateCheck(ctx, edited); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Checks(ctx)
	if got, _ := key.Open(after[0].Headers[0].Value); got != "Bearer new" || !after[0].Body.Empty() {
		t.Fatalf("after replace/clear = %+v", after[0])
	}
	// A server rejection reaches the caller and changes nothing.
	bad := edited
	bad.Method = "TRACE"
	if err := c.UpdateCheck(ctx, bad); err == nil || !strings.Contains(err.Error(), `method "TRACE" must be one of`) {
		t.Fatalf("invalid update = %v", err)
	}
	if again, _ := s.Checks(ctx); !again[0].SameDefinition(after[0]) {
		t.Fatal("a rejected update changed the check")
	}
}

func TestWebhookChannelAndChannelTestThroughTheClient(t *testing.T) {
	c, s, key := realServer(t)
	ctx := context.Background()
	hook := config.Channel{Name: "hook", Kind: config.KindWebhook, WebhookOrigin: "https://hooks.example.com",
		WebhookURL: sealTo(t, key, "https://hooks.example.com/services/T/B/SECRET"), WebhookURLEdit: config.EditReplace,
		WebhookAuthName: "Authorization", WebhookAuth: sealTo(t, key, "Bearer t"), WebhookAuthEdit: config.EditReplace}
	if err := c.AddChannel(ctx, hook); err != nil {
		t.Fatal(err)
	}
	snap, err := c.LoadSnapshot(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := snap.Channels[0]
	if got.Kind != config.KindWebhook || got.WebhookOrigin != "https://hooks.example.com" || got.WebhookURL.Empty() || got.WebhookAuth.Empty() || got.WebhookAuthName != "Authorization" {
		t.Fatalf("snapshot channel = %+v", got)
	}
	stored, _, _ := s.Channel(ctx, "hook")
	if url, err := key.Open(stored.WebhookURL); err != nil || !strings.HasSuffix(url, "SECRET") {
		t.Fatalf("stored url = %q, %v", url, err)
	}
	if err := c.UpdateChannel(ctx, config.Channel{Name: "hook", Kind: config.KindWebhook, WebhookOrigin: "https://hooks.example.com", WebhookAuthName: "Authorization", WebhookAuthEdit: config.EditClear}); err != nil {
		t.Fatal(err)
	}
	if stored, _, _ = s.Channel(ctx, "hook"); !stored.WebhookAuth.Empty() || stored.WebhookURL.Empty() {
		t.Fatalf("after update = %+v", stored)
	}

	// Channel tests: enqueue, one at a time, visible in the next snapshot.
	test, err := c.EnqueueChannelTest(ctx, "hook", time.Time{})
	if err != nil || test.ID == 0 || test.Status != config.TestPending || test.Channel != "hook" || test.Requested.IsZero() {
		t.Fatalf("EnqueueChannelTest = %+v, %v", test, err)
	}
	if _, err := c.EnqueueChannelTest(ctx, "hook", time.Time{}); err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("second enqueue = %v", err)
	}
	if _, err := c.EnqueueChannelTest(ctx, "ghost", time.Time{}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown channel = %v", err)
	}
	if _, err := c.EnqueueChannelTest(ctx, "..", time.Time{}); err == nil {
		t.Fatal("an unaddressable name was sent")
	}
	claimed, ok, _ := s.ClaimChannelTest(ctx, time.Now())
	if !ok || s.CompleteChannelTest(ctx, claimed.ID, "connection refused", time.Now()) != nil {
		t.Fatal("daemon side failed")
	}
	snap, _ = c.LoadSnapshot(ctx, time.Now())
	if len(snap.ChannelTests) != 1 || snap.ChannelTests[0].Status != config.TestError || snap.ChannelTests[0].Error != "connection refused" || snap.ChannelTests[0].Finished.IsZero() {
		t.Fatalf("snapshot tests = %+v", snap.ChannelTests)
	}
}

func TestMaintenanceWindowsThroughTheClient(t *testing.T) {
	c, s, _ := realServer(t)
	ctx := context.Background()
	if err := c.AddCheck(ctx, newCheck("api")); err != nil {
		t.Fatal(err)
	}
	w, err := c.AddMaintenanceWindow(ctx, config.MaintenanceWindow{Check: "api", Weekdays: []time.Weekday{time.Monday, time.Friday}, StartMinute: 120, Duration: 90 * time.Minute, Zone: "Europe/Madrid"})
	if err != nil || w.ID == 0 || w.Check != "api" || w.Zone != "Europe/Madrid" || len(w.Weekdays) != 2 {
		t.Fatalf("Add = %+v, %v", w, err)
	}
	w.Duration, w.Weekdays = time.Hour, []time.Weekday{time.Sunday}
	if err := c.UpdateMaintenanceWindow(ctx, w); err != nil {
		t.Fatal(err)
	}
	snap, err := c.LoadSnapshot(ctx, time.Now())
	if err != nil || len(snap.Services[0].Maintenance) != 1 || snap.Services[0].Maintenance[0].Duration != time.Hour || snap.Services[0].Maintenance[0].Weekdays[0] != time.Sunday {
		t.Fatalf("snapshot = %+v, %v", snap.Services[0].Maintenance, err)
	}
	if _, err := c.AddMaintenanceWindow(ctx, config.MaintenanceWindow{Check: "api", Weekdays: []time.Weekday{1}, Duration: time.Hour, Zone: "Mars/Base"}); err == nil || !strings.Contains(err.Error(), "IANA") {
		t.Fatalf("invalid window = %v", err)
	}
	if _, err := c.AddMaintenanceWindow(ctx, config.MaintenanceWindow{Check: "ghost", Weekdays: []time.Weekday{1}, Duration: time.Hour, Zone: "UTC"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown check = %v", err)
	}
	if err := c.DeleteMaintenanceWindow(ctx, "api", 999); err == nil {
		t.Fatal("deleted an unknown window")
	}
	if err := c.DeleteMaintenanceWindow(ctx, "api", w.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.AllMaintenanceWindows(ctx); len(all) != 0 {
		t.Fatalf("windows left: %+v", all)
	}
	if _, err := c.AddMaintenanceWindow(ctx, config.MaintenanceWindow{Check: "..", Weekdays: []time.Weekday{1}, Duration: time.Hour, Zone: "UTC"}); err == nil {
		t.Fatal("an unaddressable name was sent")
	}
}

func TestClockSkewShiftsMaintenanceEndAndChannelTests(t *testing.T) {
	server := time.Date(2026, 3, 2, 14, 0, 0, 0, time.UTC)
	skew := 7 * time.Minute
	body := `{"server_time":"` + server.Format(time.RFC3339) + `","services":[{"check":{"name":"api"},"in_maintenance":true,"maintenance_end":"` + server.Add(time.Hour).Format(time.RFC3339) +
		`","maintenance":[]}],"channel_tests":[{"id":1,"channel":"hook","status":"ok","requested":"` + server.Format(time.RFC3339) + `","started":"0001-01-01T00:00:00Z","finished":"` + server.Add(time.Minute).Format(time.RFC3339) + `"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	snap, err := New(srv.URL, testToken).LoadSnapshot(context.Background(), server.Add(skew))
	if err != nil {
		t.Fatal(err)
	}
	if end := snap.Services[0].InMaintenance.End; !end.Equal(server.Add(time.Hour + skew)) {
		t.Fatalf("maintenance end = %v", end)
	}
	if got := snap.ChannelTests[0]; !got.Requested.Equal(server.Add(skew)) || !got.Finished.Equal(server.Add(time.Minute+skew)) || !got.Started.IsZero() {
		t.Fatalf("test times = %+v", got)
	}
}

// The client reads its clock before the request, so the skew is slightly
// negative; a boundary on a whole minute still displays that minute.
func TestWholeMinuteBoundariesSurviveSmallClockSkew(t *testing.T) {
	server := time.Date(2026, 3, 2, 9, 50, 0, 0, time.UTC)
	end := server.Add(30 * time.Minute)
	body := `{"server_time":"` + server.Format(time.RFC3339) + `","services":[{"check":{"name":"api"},"in_maintenance":true,"maintenance_end":"` + end.Format(time.RFC3339) +
		`","mute":{"until":"` + end.Format(time.RFC3339) + `"},"cert":{"not_after":"` + end.Format(time.RFC3339) + `"},"maintenance":[]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	for _, skew := range []time.Duration{-37 * time.Millisecond, -1500 * time.Millisecond, 20 * time.Millisecond} {
		snap, err := New(srv.URL, testToken).LoadSnapshot(context.Background(), server.Add(skew))
		if err != nil {
			t.Fatal(err)
		}
		v := snap.Services[0]
		for name, got := range map[string]time.Time{"maintenance end": v.InMaintenance.End, "mute until": v.Mute.Until, "certificate expiry": v.Cert.NotAfter} {
			if got.Format("15:04") != "10:20" {
				t.Errorf("skew %v: %s shows %s, want 10:20 (%v)", skew, name, got.Format("15:04"), got)
			}
		}
	}
}
