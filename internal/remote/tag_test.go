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

// tagServer is foundationServer with the tag routes mounted the way cmd/up
// mounts them.
func tagServer(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h, _ := testHandler(t, func(m *http.ServeMux) {
		writes := WriteRoutes(s)
		for _, p := range []string{"/api/v1/checks", "/api/v1/checks/"} {
			m.Handle(p, writes)
		}
		maintenance := MaintenanceRoutes(s)
		m.Handle("/api/v1/checks/{name}/maintenance", maintenance)
		m.Handle("/api/v1/checks/{name}/maintenance/{id}", maintenance)
		tags := TagRoutes(s)
		for _, p := range TagPatterns {
			m.Handle(p, tags)
		}
	})
	return h, s
}

func taggedBody(name, tags string) string {
	return `{"name":"` + name + `","url":"https://example.com","interval_ms":60000,"timeout_ms":1000,"expected_status":200,"tags":` + tags + `}`
}

func TestCheckRoutesCarryTagsAndRejectInvalidOnes(t *testing.T) {
	h, s := tagServer(t)
	rec := send(h, "POST", "/api/v1/checks", taggedBody("api", `["NAS","home","nas"]`))
	var echo CheckDTO
	if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &echo) != nil || !reflect.DeepEqual(echo.Tags, []string{"nas", "home"}) {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	if got := storedCheck(t, s, "api").Tags; !reflect.DeepEqual(got, []string{"nas", "home"}) {
		t.Fatalf("stored tags = %q", got)
	}
	if rec := send(h, "PUT", "/api/v1/checks/api", taggedBody("api", `["home","prod"]`)); rec.Code != 204 {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	}
	before, _ := s.Checks(context.Background())
	for _, tc := range []struct{ method, path, body, msg string }{
		{"POST", "/api/v1/checks", taggedBody("new", `["a","b","c","d","e","f"]`), "at most 5 tags are allowed"},
		{"POST", "/api/v1/checks", taggedBody("new", `["bad tag"]`), `tag \"bad tag\" may only use letters a-z, digits and -`},
		{"PUT", "/api/v1/checks/api", taggedBody("api", `["bad tag"]`), `tag \"bad tag\" may only use letters a-z, digits and -`},
	} {
		if rec := send(h, tc.method, tc.path, tc.body); rec.Code != 400 || !strings.Contains(rec.Body.String(), tc.msg) {
			t.Errorf("%s %s = %d %s, want 400 %q", tc.method, tc.path, rec.Code, rec.Body, tc.msg)
		}
	}
	if after, _ := s.Checks(context.Background()); !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected writes changed checks: %+v", after)
	}
	// A body without tags clears them, like any other plain field.
	if rec := send(h, "PUT", "/api/v1/checks/api", `{`+plainCheck+`}`); rec.Code != 204 || storedCheck(t, s, "api").Tags != nil {
		t.Fatalf("PUT without tags = %d %s, tags %q", rec.Code, rec.Body, storedCheck(t, s, "api").Tags)
	}
}

func TestTagMuteRoutes(t *testing.T) {
	h, s := tagServer(t)
	if rec := send(h, "POST", "/api/v1/checks", taggedBody("api", `["nas"]`)); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	bg := context.Background()
	for _, tc := range []struct {
		label, method, path, body string
		status                    int
	}{
		{"no duration", "PUT", "/api/v1/tags/nas/mute", `{}`, 400},
		{"too short", "PUT", "/api/v1/tags/nas/mute", `{"duration_ms":59999}`, 400},
		{"too long", "PUT", "/api/v1/tags/nas/mute", `{"duration_ms":604800001}`, 400},
		{"invalid tag", "PUT", "/api/v1/tags/bad%20tag/mute", `{"duration_ms":60000}`, 400},
		{"unused tag", "PUT", "/api/v1/tags/ghost/mute", `{"duration_ms":60000}`, 404},
		{"unknown field", "PUT", "/api/v1/tags/nas/mute", `{"duration_ms":60000,"was_alerting":true}`, 400},
		{"method", "GET", "/api/v1/tags/nas/mute", "", 405},
		{"unknown route", "PUT", "/api/v1/tags/nas/other", `{}`, 404},
	} {
		if rec := send(h, tc.method, tc.path, tc.body); rec.Code != tc.status {
			t.Errorf("%s: %d %s, want %d", tc.label, rec.Code, rec.Body, tc.status)
		}
	}
	if mutes, _ := s.TagMutes(bg); len(mutes) != 0 {
		t.Fatalf("rejected requests stored a mute: %+v", mutes)
	}
	if rec := do(h, "PUT", "/api/v1/tags/nas/mute", strings.NewReader(`{"duration_ms":3600000}`), map[string]string{"Content-Type": "application/json"}); rec.Code != 401 {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
	if rec := send(h, "PUT", "/api/v1/tags/NAS/mute", `{"duration_ms":3600000}`); rec.Code != 204 {
		t.Fatalf("PUT mute = %d %s", rec.Code, rec.Body)
	}
	m, ok, err := s.Mute(bg, "api")
	if err != nil || !ok || m.Tag != "nas" || time.Until(m.Until) < 59*time.Minute {
		t.Fatalf("Mute(api) = %+v, %v, %v", m, ok, err)
	}
	if rec := send(h, "DELETE", "/api/v1/tags/nas/mute", ""); rec.Code != 204 {
		t.Fatalf("DELETE mute = %d %s", rec.Code, rec.Body)
	}
	if _, ok, _ := s.Mute(bg, "api"); ok {
		t.Fatal("still muted after DELETE")
	}
}

func TestTagMaintenanceRoutes(t *testing.T) {
	h, s := tagServer(t)
	if rec := send(h, "POST", "/api/v1/checks", taggedBody("api", `["nas"]`)); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	rec := send(h, "POST", "/api/v1/tags/nas/maintenance", windowJSON("UTC"))
	var created MaintenanceDTO
	if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &created) != nil || created.ID == 0 || created.Tag != "nas" || created.StartMinute != 90 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	id := "/api/v1/tags/nas/maintenance/" + jsonNumber(created.ID)
	if rec := send(h, "PUT", id, `{"weekdays":[0],"start_minute":0,"duration_ms":60000,"zone":"UTC"}`); rec.Code != 204 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	rec = send(h, "GET", "/api/v1/tags/nas/maintenance", "")
	var list []MaintenanceDTO
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list) != 1 || list[0].DurationMS != 60000 || list[0].Tag != "nas" {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	// The member's own list shows the tag's window, marked with its tag.
	rec = send(h, "GET", "/api/v1/checks/api/maintenance", "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list) != 1 || list[0].Tag != "nas" {
		t.Fatalf("member list: %d %s", rec.Code, rec.Body)
	}
	before, _ := s.TagMaintenanceWindows(context.Background(), "nas")
	for _, tc := range []struct {
		label, method, path, body string
		status                    int
	}{
		{"invalid zone", "POST", "/api/v1/tags/nas/maintenance", windowJSON("Mars/Base"), 400},
		{"invalid tag", "POST", "/api/v1/tags/bad%20tag/maintenance", windowJSON("UTC"), 400},
		{"unused tag", "POST", "/api/v1/tags/ghost/maintenance", windowJSON("UTC"), 404},
		{"another tag", "PUT", "/api/v1/tags/home/maintenance/" + jsonNumber(created.ID), windowJSON("UTC"), 404},
		{"delete under another tag", "DELETE", "/api/v1/tags/home/maintenance/" + jsonNumber(created.ID), "", 404},
		{"id not a number", "DELETE", "/api/v1/tags/nas/maintenance/abc", "", 404},
		{"collection method", "DELETE", "/api/v1/tags/nas/maintenance", "", 405},
		{"item method", "GET", id, "", 405},
	} {
		if rec := send(h, tc.method, tc.path, tc.body); rec.Code != tc.status {
			t.Errorf("%s: %d %s, want %d", tc.label, rec.Code, rec.Body, tc.status)
		}
	}
	if after, _ := s.TagMaintenanceWindows(context.Background(), "nas"); !reflect.DeepEqual(after, before) {
		t.Fatal("rejected requests changed the windows")
	}
	if rec := send(h, "DELETE", id, ""); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, "GET", "/api/v1/tags/nas/maintenance", ""); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty list = %q", rec.Body)
	}
}

// The snapshot carries each monitor's tags and which mute and windows come
// from a tag, and converts back to the same values.
func TestSnapshotCarriesTagsAndTagHolds(t *testing.T) {
	_, s := tagServer(t)
	bg := context.Background()
	if err := s.AddCheck(bg, config.Check{Name: "api", URL: "https://example.com", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200, Tags: []string{"nas", "home"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 1, 7, 10, 30, 0, 0, time.UTC)
	if err := s.SetTagMute(bg, "nas", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMaintenanceWindow(bg, config.MaintenanceWindow{Tag: "home", Weekdays: []time.Weekday{time.Monday}, StartMinute: 600, Duration: time.Hour, Zone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	snap, err := tui.Load(bg, s, now)
	if err != nil {
		t.Fatal(err)
	}
	dto := NewSnapshotDTO(snap, now)
	b, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"tags":["nas","home"]`, `"mute":{"until":"2030-01-07T11:30:00Z","was_alerting":false,"tag":"nas"}`, `"tag":"home"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("snapshot JSON lacks %s: %s", want, b)
		}
	}
	var wire SnapshotDTO
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	got := wire.Snapshot().Services[0]
	if !reflect.DeepEqual(got.Check.Tags, []string{"nas", "home"}) || got.Mute.Tag != "nas" || got.Mute.Check != "api" || !got.Muted(now) {
		t.Fatalf("service = %+v", got)
	}
	if len(got.Maintenance) != 1 || got.Maintenance[0].Tag != "home" || got.Maintenance[0].Check != "api" || !got.InMaintenance.Active {
		t.Fatalf("maintenance = %+v, %+v", got.Maintenance, got.InMaintenance)
	}
}
