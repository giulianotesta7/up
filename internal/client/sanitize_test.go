package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/remote"
	"github.com/heohlee/up/internal/store"
	"github.com/heohlee/up/internal/tui"
)

// hostile is what a compromised server could put in any text field: color and
// cursor sequences, an OSC clipboard write, a C1 control, a bell, newlines.
const hostile = "\x1b[31mred\x1b[0m\x1b]52;c;ZXZpbA==\x07\u009b2Jtext\a\r\n\x7f"

func unsafeText(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return true
		}
	}
	return strings.Contains(s, "\x1b")
}

// walkStrings reports every string reachable from v, so a new field added to
// the snapshot is covered without touching this test.
func walkStrings(v reflect.Value, path string, visit func(path, s string)) {
	switch v.Kind() {
	case reflect.String:
		visit(path, v.String())
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				walkStrings(v.Field(i), path+"."+v.Type().Field(i).Name, visit)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkStrings(v.Index(i), path+"[]", visit)
		}
	}
}

func hostileSnapshot() tui.Snapshot {
	now := time.Now()
	h := hostile
	return tui.Snapshot{
		Services: []tui.Service{{
			Check:     config.Check{Name: "api" + h, URL: "https://example.com/" + h, Channel: "ops" + h},
			HasResult: true,
			Latest:    check.Result{CheckName: "api", At: now, Status: check.StatusDown, Error: "boom" + h},
			Recent:    []check.Result{{CheckName: "api", At: now, Error: "old" + h}},
			Outages:   []store.Outage{{Start: now, Error: "down" + h}},
			HasCert:   true, Cert: config.Cert{Host: "host" + h, Issuer: "issuer" + h, Serial: "serial" + h},
		}},
		SMTPServers: []config.SMTPServer{{Name: "smtp" + h, Host: "smtp.example.com" + h, Username: "user" + h, Sender: "a@example.com" + h}},
		Channels:    []config.Channel{{Name: "chan" + h, Kind: "email" + h, SMTPServer: "smtp" + h, Recipients: []string{"a@example.com" + h}}},
		Defaults:    config.Defaults{Zone: "Europe/Madrid" + h, Channel: "chan" + h},
	}
}

func TestServerTextCannotInjectTerminalSequences(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(remote.NewSnapshotDTO(hostileSnapshot(), time.Now()))
	}))
	defer srv.Close()
	snap, err := New(srv.URL, testToken).LoadSnapshot(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var seen int
	walkStrings(reflect.ValueOf(snap), "Snapshot", func(path, s string) {
		seen++
		if unsafeText(s) {
			t.Errorf("%s keeps control characters: %q", path, s)
		}
	})
	if seen < 17 {
		t.Fatalf("the walk saw only %d strings: the test is not covering the snapshot", seen)
	}
	// Readable text survives: only the sequences and control characters go.
	if got := snap.Services[0].Check.Name; !strings.HasPrefix(got, "api") || !strings.Contains(got, "red") {
		t.Errorf("legitimate text was lost: %q", got)
	}
}

func TestOrdinaryTextIsLeftAlone(t *testing.T) {
	clean := tui.Snapshot{Services: []tui.Service{{Check: config.Check{Name: "café ☕ 日本", URL: "https://example.com/a?b=c&d=é"}}},
		SMTPServers: []config.SMTPServer{{Name: "Gmail (work)", Sender: "up <alerts@example.com>"}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(remote.NewSnapshotDTO(clean, time.Now()))
	}))
	defer srv.Close()
	snap, err := New(srv.URL, testToken).LoadSnapshot(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Services[0].Check.Name != "café ☕ 日本" || snap.Services[0].Check.URL != "https://example.com/a?b=c&d=é" ||
		snap.SMTPServers[0].Name != "Gmail (work)" || snap.SMTPServers[0].Sender != "up <alerts@example.com>" {
		t.Errorf("ordinary text must not change: %+v", snap)
	}
}

func TestServerErrorTextCannotInjectTerminalSequences(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad name" + hostile})
	}))
	defer srv.Close()
	c := New(srv.URL, testToken)
	_, snapErr := c.LoadSnapshot(context.Background(), time.Now())
	for label, err := range map[string]error{"write": c.DeleteCheck(context.Background(), "x"), "snapshot": snapErr} {
		if err == nil || unsafeText(err.Error()) || !strings.HasPrefix(err.Error(), "bad name") {
			t.Errorf("%s: %q", label, err)
		}
	}
}
