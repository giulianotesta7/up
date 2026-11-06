package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/store"
)

// near allows for the few milliseconds of skew between the client's clock
// reading and the server's.
func near(a, b time.Time) bool { return a.Sub(b).Abs() < 2*time.Second }

func TestDeliveriesThroughTheRealAPI(t *testing.T) {
	c, s, _ := realServer(t)
	ctx := context.Background()
	if err := c.AddCheck(ctx, newCheck("api")); err != nil {
		t.Fatal(err)
	}
	if err := c.AddCheck(ctx, newCheck("web")); err != nil {
		t.Fatal(err)
	}
	// Recent enough that the clock-skew shift is far below the assertions.
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	a1 := store.Attempt{At: base, Monitor: "api", Channel: "ops", Event: store.EventDown, Outcome: store.OutcomeFailed, Reason: "status 502"}
	a2 := store.Attempt{At: base.Add(time.Minute), Monitor: "web", Channel: "pager", Event: store.EventCert, Outcome: store.OutcomeHeld, Reason: "muted"}
	for _, a := range []store.Attempt{a1, a2} {
		if err := s.RecordAttempt(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	got, err := c.Deliveries(ctx, store.AttemptFilter{})
	if err != nil || len(got) != 2 || !near(got[0].At, a2.At) || !near(got[1].At, a1.At) || got[0].Reason != "muted" || got[1].Event != store.EventDown {
		t.Fatalf("Deliveries() = %+v, %v", got, err)
	}
	if got, err := c.Deliveries(ctx, store.AttemptFilter{Channel: "ops"}); err != nil || len(got) != 1 || got[0].Monitor != "api" {
		t.Fatalf("by channel = %+v, %v", got, err)
	}
	if got, err := c.Deliveries(ctx, store.AttemptFilter{Monitor: "web", Limit: 1}); err != nil || len(got) != 1 || got[0].Channel != "pager" {
		t.Fatalf("by monitor = %+v, %v", got, err)
	}
	snap, err := c.LoadSnapshot(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Attempts) != 2 || len(snap.LastAttempts) != 2 || len(snap.Services[0].Attempts) != 1 || snap.Services[0].Attempts[0].Reason != "status 502" {
		t.Fatalf("snapshot attempts = %+v / %+v / %+v", snap.Attempts, snap.LastAttempts, snap.Services[0].Attempts)
	}
}

func TestDeliveriesShiftByClockSkewAndCleanText(t *testing.T) {
	// The server's clock is 7 minutes behind this one.
	skew := 7 * time.Minute
	server := time.Now().UTC().Add(-skew).Truncate(time.Second)
	body := `{"server_time":"` + server.Format(time.RFC3339) + `","attempts":[{"at":"` + server.Format(time.RFC3339) + `","monitor":"api","channel":"ops","event":"monitor.down","outcome":"failed","reason":"bad\u001b[31m text"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	got, err := New(srv.URL, testToken).Deliveries(context.Background(), store.AttemptFilter{})
	if err != nil || len(got) != 1 {
		t.Fatalf("Deliveries() = %+v, %v", got, err)
	}
	if shift := got[0].At.Sub(server); shift < skew-time.Second || shift > skew+time.Second {
		t.Fatalf("shifted by %v, want about %v", shift, skew)
	}
	if got[0].Reason != "bad text" {
		t.Fatalf("reason = %q, want control sequences removed", got[0].Reason)
	}
}

func TestDeliveriesErrorsAreSanitized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", 500) }))
	defer srv.Close()
	got, err := New(srv.URL, testToken).Deliveries(context.Background(), store.AttemptFilter{})
	if err == nil || len(got) != 0 {
		t.Fatalf("Deliveries() = %+v, %v, want an error", got, err)
	}
	noToken(t, "deliveries", err)
	if !reflect.DeepEqual(got, []store.Attempt(nil)) {
		t.Fatalf("got %+v", got)
	}
}
