package remote

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/tui"
)

func TestPingAndDNSChecksThroughTheAPIAndSnapshot(t *testing.T) {
	h, s, _ := tokenServer(t)
	ping := `{"name":"router","kind":"ping","host":"192.168.1.1","interval_ms":30000,"timeout_ms":2000}`
	dns := `{"name":"nas","kind":"dns","domain":"nas.home.arpa","record":"A","resolver":"[fd00::53]:53","expected":"203.0.113.10","interval_ms":60000,"timeout_ms":2000,"alert_after":2}`
	for _, body := range []string{ping, dns} {
		if rec := send(h, "POST", "/api/v1/checks", body); rec.Code != 201 {
			t.Fatalf("create %s = %d %s", body, rec.Code, rec.Body)
		}
	}
	wantPing := config.Check{Name: "router", Kind: config.CheckPing, Host: "192.168.1.1", Interval: 30 * time.Second, Timeout: 2 * time.Second}
	wantDNS := config.Check{Name: "nas", Kind: config.CheckDNS, Domain: "nas.home.arpa", Record: "A", Resolver: "[fd00::53]:53", Expected: "203.0.113.10", Interval: time.Minute, Timeout: 2 * time.Second, AlertAfter: 2}
	if got := storedCheck(t, s, "router"); !reflect.DeepEqual(got, wantPing) {
		t.Fatalf("stored %+v, want %+v", got, wantPing)
	}
	if got := storedCheck(t, s, "nas"); !reflect.DeepEqual(got, wantDNS) {
		t.Fatalf("stored %+v, want %+v", got, wantDNS)
	}
	// Edit over the API, and validation messages reach the client.
	edited := `{"name":"nas","kind":"dns","domain":"example.com","record":"MX","expected":"mail.example.com","interval_ms":60000,"timeout_ms":2000}`
	if rec := send(h, "PUT", "/api/v1/checks/nas", edited); rec.Code != 204 {
		t.Fatalf("update = %d %s", rec.Code, rec.Body)
	}
	if got := storedCheck(t, s, "nas"); got.Domain != "example.com" || got.Record != "MX" || got.Resolver != "" || got.Expected != "mail.example.com" {
		t.Fatalf("edited %+v", got)
	}
	for body, want := range map[string]string{
		`{"name":"x","kind":"dns","domain":"nas.home.arpa","record":"SRV","interval_ms":60000,"timeout_ms":2000}`:         `record \"SRV\" must be one of A, AAAA, CNAME, MX, TXT`,
		`{"name":"x","kind":"ping","host":"192.168.1.1","port":22,"interval_ms":60000,"timeout_ms":2000}`:                 "port is only for tcp checks",
		`{"name":"x","kind":"dns","domain":"a.b","record":"A","resolver":"fd00::53","interval_ms":1000,"timeout_ms":500}`: "must put an IPv6 address in brackets",
	} {
		if rec := send(h, "POST", "/api/v1/checks", body); rec.Code != 400 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("create %s = %d %s, want 400 with %q", body, rec.Code, rec.Body, want)
		}
	}

	at := time.Date(2030, 1, 7, 2, 14, 0, 0, time.UTC)
	answered := check.Result{CheckName: "nas", At: at, Status: check.StatusUp, Latency: 4 * time.Millisecond, Answer: "mail.example.com", Answers: 2}
	if err := s.Record(context.Background(), answered); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPingPermission(context.Background(), config.PingDenied); err != nil {
		t.Fatal(err)
	}
	snap, err := tui.Load(context.Background(), s, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Pings != config.PingDenied {
		t.Fatalf("snapshot pings = %q", snap.Pings)
	}
	b, err := json.Marshal(NewSnapshotDTO(snap, at.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`"pings":"denied"`, `"kind":"ping"`, `"domain":"example.com","record":"MX","resolver":"","expected":"mail.example.com"`, `"answer":"mail.example.com","answers":2`} {
		if !strings.Contains(string(b), part) {
			t.Errorf("snapshot JSON lacks %s:\n%s", part, b)
		}
	}
	var dto SnapshotDTO
	if err := json.Unmarshal(b, &dto); err != nil {
		t.Fatal(err)
	}
	back := dto.Snapshot()
	if back.Pings != config.PingDenied {
		t.Fatalf("pings round trip = %q", back.Pings)
	}
	for i := range snap.Services {
		if !reflect.DeepEqual(back.Services[i].Check, snap.Services[i].Check) {
			t.Fatalf("service %d round trip = %+v, want %+v", i, back.Services[i].Check, snap.Services[i].Check)
		}
		if snap.Services[i].Check.Name == "nas" && (back.Services[i].Latest != answered || back.Services[i].Recent[0] != answered) {
			t.Fatalf("answer round trip = %+v, want %+v", back.Services[i].Latest, answered)
		}
	}
}

// A snapshot from a daemon that predates ping monitors has no "pings": its
// permission is unknown, not denied.
func TestSnapshotWithoutPingsIsUnknown(t *testing.T) {
	var dto SnapshotDTO
	if err := json.Unmarshal([]byte(`{"server_time":"2030-01-07T02:14:00Z","services":[]}`), &dto); err != nil {
		t.Fatal(err)
	}
	if got := dto.Snapshot().Pings; got != config.PingUnknown {
		t.Fatalf("pings = %q, want unknown", got)
	}
	// An unexpected value is also unknown rather than trusted.
	if err := json.Unmarshal([]byte(`{"pings":"sometimes"}`), &dto); err != nil {
		t.Fatal(err)
	}
	if got := dto.Snapshot().Pings; got != config.PingUnknown {
		t.Fatalf("pings = %q, want unknown", got)
	}
}
