package remote

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/store"
	"github.com/heohlee/up/internal/tui"
)

func defaultsServer(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	handler, _ := testHandler(t, func(m *http.ServeMux) {
		m.Handle("/api/v1/defaults", DefaultsRoutes(s))
	})
	return handler, s
}

const defaultsBody = `{"zone":"America/Argentina/Buenos_Aires","channel":"","interval_ms":60000,"timeout_ms":10000,"expected_status":204,"alert_after":3,"recover_after":2}`

func TestDefaultsRoutes(t *testing.T) {
	h, s := defaultsServer(t)
	ctx := context.Background()
	rec := send(h, "GET", "/api/v1/defaults", "")
	var got DefaultsDTO
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got != (DefaultsDTO{Zone: "UTC", IntervalMS: 30000, TimeoutMS: 5000, ExpectedStatus: 200, AlertAfter: 1, RecoverAfter: 1}) {
		t.Fatalf("GET empty = %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, "PUT", "/api/v1/defaults", defaultsBody); rec.Code != 200 {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	} else if json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Zone != "America/Argentina/Buenos_Aires" || got.IntervalMS != 60000 {
		t.Fatalf("PUT body = %s", rec.Body)
	}
	want := config.Defaults{Zone: "America/Argentina/Buenos_Aires", Interval: time.Minute, Timeout: 10 * time.Second, ExpectedStatus: 204, AlertAfter: 3, RecoverAfter: 2}
	if d, _ := s.Defaults(ctx); d != want {
		t.Fatalf("stored = %+v, want %+v", d, want)
	}
	rec = send(h, "GET", "/api/v1/defaults", "")
	if json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.AlertAfter != 3 {
		t.Fatalf("GET after PUT = %s", rec.Body)
	}
	if rec := do(h, "GET", "/api/v1/defaults", nil, nil); rec.Code != 401 {
		t.Fatalf("unauthenticated = %d", rec.Code)
	}
}

func TestDefaultsRoutesRejectionChangesNothing(t *testing.T) {
	h, s := defaultsServer(t)
	ctx := context.Background()
	before := config.BuiltinDefaults()
	for _, tc := range []struct {
		label, method, body string
		status              int
		msg                 string
	}{
		{"timeout above interval", "PUT", `{"zone":"UTC","channel":"","interval_ms":5000,"timeout_ms":10000,"expected_status":200,"alert_after":1,"recover_after":1}`, 400, "timeout (10s) must not exceed interval (5s)"},
		{"bad zone", "PUT", strings.Replace(defaultsBody, "America/Argentina/Buenos_Aires", "Mars/Base", 1), 400, `zone \"Mars/Base\" is not an IANA time zone`},
		{"missing channel", "PUT", strings.Replace(defaultsBody, `"channel":""`, `"channel":"ghost"`, 1), 400, `channel \"ghost\" does not exist`},
		{"unknown field", "PUT", strings.Replace(defaultsBody, `"zone"`, `"extra":1,"zone"`, 1), 400, ""},
		{"duration overflow", "PUT", strings.Replace(defaultsBody, `60000`, `9223372036854775807`, 1), 400, "interval_ms is out of range"},
		{"not an object", "PUT", `[]`, 400, ""},
		{"method", "DELETE", "", 405, ""},
	} {
		rec := send(h, tc.method, "/api/v1/defaults", tc.body)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.msg) {
			t.Errorf("%s: %d %s, want %d %q", tc.label, rec.Code, rec.Body, tc.status, tc.msg)
		}
	}
	if d, _ := s.Defaults(ctx); d != before {
		t.Fatalf("rejected requests changed defaults: %+v", d)
	}
}

func TestSnapshotCarriesDefaults(t *testing.T) {
	d := config.Defaults{Zone: "Europe/Madrid", Channel: "ops", Interval: time.Minute, Timeout: 10 * time.Second, ExpectedStatus: 204, AlertAfter: 3, RecoverAfter: 2}
	dto := NewSnapshotDTO(tui.Snapshot{Defaults: d}, time.Now())
	b, _ := json.Marshal(dto)
	var back SnapshotDTO
	if err := json.Unmarshal(b, &back); err != nil || !strings.Contains(string(b), `"defaults":{"zone":"Europe/Madrid"`) {
		t.Fatalf("snapshot json = %s, %v", b, err)
	}
	if got := back.Snapshot().Defaults; !reflect.DeepEqual(got, d) {
		t.Fatalf("Snapshot().Defaults = %+v, want %+v", got, d)
	}
}
