package remote

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
	"github.com/heohlee/up/internal/store"
	"github.com/heohlee/up/internal/tui"
)

func TestSnapshotDTO(t *testing.T) {
	now := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	k, _, err := secret.LoadOrCreate(t.TempDir() + "/key")
	if err != nil {
		t.Fatal(err)
	}
	oldKey, _, err := secret.LoadOrCreate(t.TempDir() + "/key")
	if err != nil {
		t.Fatal(err)
	}
	key, old := k.PublicKey(), oldKey.PublicKey()
	a, err := secret.Seal(key, "password one")
	if err != nil {
		t.Fatal(err)
	}
	b, err := secret.Seal(old, "password two")
	if err != nil {
		t.Fatal(err)
	}
	r := check.Result{CheckName: "site", At: now, Status: check.StatusDown, Latency: 123 * time.Millisecond, StatusCode: 503, Error: "down"}
	s := tui.Snapshot{PublicKey: key, HasKey: true, LastBeat: now, HasBeat: true, Uptime: 87.5, HasUptime: true, Incidents: 3,
		Services: []tui.Service{{Check: config.Check{Name: "site", URL: "https://example.com", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200, Channel: "ops", AlertAfter: 2, RecoverAfter: 3, TLSOff: true}, Latest: r, HasResult: true, DownSince: now, Recent: []check.Result{r}, Outages: []store.Outage{{Start: now, End: now, Ongoing: true, Error: "down"}}, Streak: check.Streak{Alerting: true, Failures: 2, Successes: 1}, Mute: config.Mute{Check: "site", Until: now, WasAlerting: true}, HasMute: true, Cert: config.Cert{Check: "site", Host: "host", Issuer: "issuer", Serial: "serial", NotAfter: now, Observed: now, Notified: 7}, HasCert: true,
			Incidents: []tui.Incident{{Outage: store.Outage{Start: now, Ongoing: true, Error: "down"}, Alert: store.OutcomeFailed, Channel: "pager", Reason: "status 502"}, {Outage: store.Outage{Start: now.Add(-time.Hour), End: now.Add(-50 * time.Minute), Error: "timeout"}}},
			PeriodUp:  29 * 24 * time.Hour, PeriodObserved: 30 * 24 * time.Hour, PeriodIncidents: 4}, {Check: config.Check{Name: "second"}}},
		SMTPServers: []config.SMTPServer{{Name: "current", Host: "smtp", Port: 465, TLS: config.TLSImplicit, Username: "user", Sender: "sender", Password: a}, {Name: "old", Password: b}, {Name: "empty"}}, Channels: []config.Channel{{Name: "ops", Kind: "email", SMTPServer: "current", Recipients: []string{"a@example.com"}}}}
	dto := NewSnapshotDTO(s, now)
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range [][]byte{a.Data, b.Data, old} {
		if bytes.Contains(raw, v) || bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(v))) {
			t.Fatalf("secret leaked: %s", raw)
		}
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if bytes.Count(raw, []byte(encoded)) != 1 {
		t.Fatalf("public key occurrences: %s", raw)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["public_key"] != encoded {
		t.Fatalf("public key: %v", doc["public_key"])
	}
	servers := doc["smtp_servers"].([]any)
	for i, want := range []string{"set", "needs_reentry", "none"} {
		if servers[i].(map[string]any)["password_state"] != want {
			t.Fatalf("state %d: %v", i, servers[i])
		}
	}
	if !strings.Contains(string(raw), `"server_time":"2025-02-03T04:05:06Z"`) || !strings.Contains(string(raw), `"interval_ms":60000`) || !strings.Contains(string(raw), `"latency_ms":123`) {
		t.Fatalf("wire format: %s", raw)
	}
	got := dto.Snapshot()
	for i := range got.SMTPServers {
		p := got.SMTPServers[i].Password
		switch i {
		case 0:
			if p.Empty() || !bytes.Equal(p.Key, key) {
				t.Fatal("set placeholder")
			}
		case 1:
			if p.Empty() || bytes.Equal(p.Key, key) {
				t.Fatal("old placeholder")
			}
		case 2:
			if !p.Empty() {
				t.Fatal("empty placeholder")
			}
		}
		got.SMTPServers[i].Password = s.SMTPServers[i].Password
	}
	if !reflect.DeepEqual(got, s) {
		t.Fatalf("roundtrip:\n%#v\n%#v", got, s)
	}
	s.HasKey = false
	raw, _ = json.Marshal(NewSnapshotDTO(s, now))
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	servers = doc["smtp_servers"].([]any)
	if servers[0].(map[string]any)["password_state"] != "needs_reentry" {
		t.Fatal("missing key must require reentry for the formerly current key")
	}
}

func TestSnapshotRoute(t *testing.T) {
	calls := 0
	fail := false
	routes := SnapshotRoutes(func(ctx context.Context, now time.Time) (tui.Snapshot, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second || time.Until(deadline) < 9*time.Second {
			t.Error("missing timeout")
		}
		if fail {
			return tui.Snapshot{}, errors.New("private database error")
		}
		return tui.Snapshot{}, nil
	})
	h, _ := testHandler(t, func(m *http.ServeMux) { m.Handle("/api/v1/snapshot", routes) })
	if r := do(h, "GET", "/api/v1/snapshot", nil, nil); r.Code != 401 || calls != 0 {
		t.Fatalf("auth: %d calls %d", r.Code, calls)
	}
	if r := do(h, "POST", "/api/v1/snapshot", nil, bearer(testToken)); r.Code != 405 || r.Header().Get("Allow") != "GET" || calls != 0 {
		t.Fatalf("method: %d %s", r.Code, r.Header().Get("Allow"))
	}
	if r := do(h, "GET", "/api/v1/snapshot?ignored=yes", nil, bearer(testToken)); r.Code != 200 || calls != 1 {
		t.Fatalf("get: %d %s", r.Code, r.Body)
	}
	fail = true
	if r := do(h, "GET", "/api/v1/snapshot", nil, bearer(testToken)); r.Code != 500 || r.Body.String() != "{\"error\":\"could not read the database\"}\n" {
		t.Fatalf("error: %d %s", r.Code, r.Body)
	}
	if r := do(h, "GET", "/other", nil, bearer(testToken)); r.Code != 404 {
		t.Fatalf("unknown: %d", r.Code)
	}
	if r := do(h, "GET", "/api/v1/ping", nil, bearer(testToken)); r.Code != 200 {
		t.Fatalf("ping: %d", r.Code)
	}
}
