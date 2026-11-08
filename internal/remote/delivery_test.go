package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/store"
	"github.com/heohlee/up/internal/tui"
)

type attemptReader struct {
	got  []store.AttemptFilter
	list []store.Attempt
	err  error
}

func (r *attemptReader) Attempts(_ context.Context, f store.AttemptFilter) ([]store.Attempt, error) {
	r.got = append(r.got, f)
	return r.list, r.err
}

var sample = []store.Attempt{
	{At: time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC), Monitor: "api", Channel: "ops", Event: store.EventDown, Outcome: store.OutcomeFailed, Reason: "webhook https://hooks.example.com: answered 502 Bad Gateway"},
	{At: time.Date(2025, 2, 3, 4, 0, 0, 0, time.UTC), Monitor: "web", Channel: "pager", Event: store.EventRecovered, Outcome: store.OutcomeHeld, Reason: "muted"},
}

func TestDeliveryRoute(t *testing.T) {
	reader := &attemptReader{list: sample}
	h, _ := testHandler(t, func(m *http.ServeMux) { m.Handle("/api/v1/deliveries", DeliveryRoutes(reader)) })
	if r := do(h, "GET", "/api/v1/deliveries", nil, nil); r.Code != 401 || len(reader.got) != 0 {
		t.Fatalf("auth: %d, reads %d", r.Code, len(reader.got))
	}
	if r := do(h, "POST", "/api/v1/deliveries", nil, bearer(testToken)); r.Code != 405 || r.Header().Get("Allow") != "GET" || len(reader.got) != 0 {
		t.Fatalf("method: %d %q", r.Code, r.Header().Get("Allow"))
	}
	r := do(h, "GET", "/api/v1/deliveries", nil, bearer(testToken))
	if r.Code != 200 || !reflect.DeepEqual(reader.got, []store.AttemptFilter{{}}) {
		t.Fatalf("get: %d %s filters %+v", r.Code, r.Body, reader.got)
	}
	var out struct {
		ServerTime time.Time    `json:"server_time"`
		Attempts   []AttemptDTO `json:"attempts"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil || out.ServerTime.IsZero() {
		t.Fatalf("body %s: %v", r.Body, err)
	}
	want := []AttemptDTO{
		{At: sample[0].At, Monitor: "api", Channel: "ops", Event: "monitor.down", Outcome: "failed", Reason: sample[0].Reason},
		{At: sample[1].At, Monitor: "web", Channel: "pager", Event: "monitor.recovered", Outcome: "held", Reason: "muted"},
	}
	if !reflect.DeepEqual(out.Attempts, want) {
		t.Fatalf("attempts = %+v, want %+v", out.Attempts, want)
	}
	if !strings.Contains(r.Body.String(), `"monitor":"api","channel":"ops","event":"monitor.down","outcome":"failed"`) {
		t.Fatalf("wire format: %s", r.Body)
	}
}

func TestDeliveryRouteFiltersAndLimit(t *testing.T) {
	reader := &attemptReader{}
	h, _ := testHandler(t, func(m *http.ServeMux) { m.Handle("/api/v1/deliveries", DeliveryRoutes(reader)) })
	r := do(h, "GET", "/api/v1/deliveries?channel=ops&monitor=api%20v2&limit=7", nil, bearer(testToken))
	if r.Code != 200 || !reflect.DeepEqual(reader.got, []store.AttemptFilter{{Channel: "ops", Monitor: "api v2", Limit: 7}}) {
		t.Fatalf("%d %s filters %+v", r.Code, r.Body, reader.got)
	}
	if !strings.Contains(r.Body.String(), `"attempts":[]`) {
		t.Fatalf("empty list must be []: %s", r.Body)
	}
	reader.got = nil
	for _, limit := range []string{"0", "-1", "x", "1.5", ""} {
		r := do(h, "GET", "/api/v1/deliveries?limit="+limit, nil, bearer(testToken))
		if limit == "" {
			if r.Code != 200 {
				t.Errorf("empty limit: %d", r.Code)
			}
			continue
		}
		if r.Code != 400 || r.Body.String() != "{\"error\":\"limit must be a positive number\"}\n" {
			t.Errorf("limit %q: %d %s", limit, r.Code, r.Body)
		}
	}
	if len(reader.got) != 1 {
		t.Fatalf("a bad limit still read the store: %+v", reader.got)
	}
}

func TestDeliveryRouteHidesDatabaseErrors(t *testing.T) {
	h, _ := testHandler(t, func(m *http.ServeMux) {
		m.Handle("/api/v1/deliveries", DeliveryRoutes(&attemptReader{err: errors.New("private database error")}))
	})
	if r := do(h, "GET", "/api/v1/deliveries", nil, bearer(testToken)); r.Code != 500 || r.Body.String() != "{\"error\":\"could not read the database\"}\n" {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
}

func TestSnapshotCarriesAttemptsRoundTrip(t *testing.T) {
	now := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	s := tui.Snapshot{Attempts: sample, LastAttempts: sample[:1], Services: []tui.Service{{Attempts: sample[1:]}}}
	raw, err := json.Marshal(NewSnapshotDTO(s, now))
	if err != nil {
		t.Fatal(err)
	}
	var dto SnapshotDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatal(err)
	}
	got := dto.Snapshot()
	if !reflect.DeepEqual(got.Attempts, s.Attempts) || !reflect.DeepEqual(got.LastAttempts, s.LastAttempts) || !reflect.DeepEqual(got.Services[0].Attempts, s.Services[0].Attempts) {
		t.Fatalf("round trip:\n%+v\n%+v", got, s)
	}
	for _, key := range []string{`"attempts":[`, `"last_attempts":[`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("missing %s in %s", key, raw)
		}
	}
	empty, _ := json.Marshal(NewSnapshotDTO(tui.Snapshot{}, now))
	if !strings.Contains(string(empty), `"attempts":[]`) || !strings.Contains(string(empty), `"last_attempts":[]`) {
		t.Fatalf("empty snapshot must carry [] lists: %s", empty)
	}
}
