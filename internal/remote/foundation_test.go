package remote

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
	"github.com/giulianotesta7/up/internal/tui"
)

var serverKey = bytes.Repeat([]byte{7}, 32)

// foundationServer serves every write route over a real store, mounted the
// way cmd/up mounts them.
func foundationServer(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.SetPublicKey(context.Background(), serverKey); err != nil {
		t.Fatal(err)
	}
	h, _ := testHandler(t, func(m *http.ServeMux) {
		writes, alerts := WriteRoutes(s), AlertWriteRoutes(s)
		for _, p := range []string{"/api/v1/checks", "/api/v1/checks/"} {
			m.Handle(p, writes)
		}
		for _, p := range []string{"/api/v1/smtp-servers", "/api/v1/smtp-servers/", "/api/v1/channels", "/api/v1/channels/"} {
			m.Handle(p, alerts)
		}
		maintenance := MaintenanceRoutes(s)
		m.Handle("/api/v1/checks/{name}/maintenance", maintenance)
		m.Handle("/api/v1/checks/{name}/maintenance/{id}", maintenance)
		m.Handle("/api/v1/channels/{name}/test", ChannelTestRoutes(s))
	})
	return h, s
}

func send(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	return do(h, method, path, strings.NewReader(body), jsonHeaders())
}

func sealedJSON(key []byte, data ...byte) string {
	b64 := base64.StdEncoding.EncodeToString
	return `{"key":"` + b64(key) + `","data":"` + b64(data) + `"}`
}

func replaceJSON(data ...byte) string {
	return `{"action":"replace","sealed":` + sealedJSON(serverKey, data...) + `}`
}

const plainCheck = `"name":"api","url":"https://example.com","interval_ms":60000,"timeout_ms":1000,"expected_status":200`

func advancedCheckBody(name string) string {
	return `{"name":"` + name + `","url":"https://example.com","interval_ms":60000,"timeout_ms":1000,"expected_status":200,
"method":"POST","headers":[{"name":"X-Token","value":` + replaceJSON(1, 1) + `},{"name":"Accept"}],
"body":` + replaceJSON(2, 2, 2) + `,"expect_text":"pong"}`
}

func storedCheck(t *testing.T, s *store.Store, name string) config.Check {
	t.Helper()
	checks, err := s.Checks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q missing", name)
	return config.Check{}
}

func TestCheckRoutesStoreAdvancedFieldsAndNeverEchoThem(t *testing.T) {
	h, s := foundationServer(t)
	rec := send(h, "POST", "/api/v1/checks", advancedCheckBody("api"))
	if rec.Code != 201 {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	got := storedCheck(t, s, "api")
	if got.Method != "POST" || got.ExpectText != "pong" || !reflect.DeepEqual(got.Body, secret.Sealed{Key: serverKey, Data: []byte{2, 2, 2}}) ||
		len(got.Headers) != 2 || got.Headers[0].Name != "X-Token" || !reflect.DeepEqual(got.Headers[0].Value.Data, []byte{1, 1}) || got.Headers[1].Name != "Accept" || !got.Headers[1].Value.Empty() {
		t.Fatalf("stored = %+v", got)
	}
	// The response reports only that values are set.
	var echo CheckDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &echo); err != nil {
		t.Fatal(err)
	}
	if !echo.BodySet || !echo.Headers[0].ValueSet || echo.Headers[1].ValueSet || echo.Body != nil || echo.Headers[0].Value != nil || echo.Kind != "http" || echo.Method != "POST" {
		t.Fatalf("echo = %+v", echo)
	}
	for _, leaked := range []string{`"sealed"`, `"data"`, base64.StdEncoding.EncodeToString(serverKey), "AgIC"} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Fatalf("response leaks %q: %s", leaked, rec.Body)
		}
	}
}

func TestCheckRoutesProtectedValueEdits(t *testing.T) {
	h, s := foundationServer(t)
	if rec := send(h, "POST", "/api/v1/checks", advancedCheckBody("api")); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	put := func(body string) *httptest.ResponseRecorder { return send(h, "PUT", "/api/v1/checks/api", body) }
	base := `"url":"https://example.com","interval_ms":60000,"timeout_ms":1000,"expected_status":200,"method":"POST","expect_text":"pong"`
	// Absent fields keep; header values are kept by name.
	if rec := put(`{` + base + `,"headers":[{"name":"x-token"},{"name":"Accept"}]}`); rec.Code != 204 {
		t.Fatalf("keep: %d %s", rec.Code, rec.Body)
	}
	got := storedCheck(t, s, "api")
	if !reflect.DeepEqual(got.Headers[0].Value.Data, []byte{1, 1}) || !reflect.DeepEqual(got.Body.Data, []byte{2, 2, 2}) {
		t.Fatalf("absent did not keep: %+v", got)
	}
	// Explicit keep, replace and clear.
	if rec := put(`{` + base + `,"headers":[{"name":"x-token","value":{"action":"keep"}},{"name":"Accept","value":` + replaceJSON(9) + `}],"body":{"action":"clear"}}`); rec.Code != 204 {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	got = storedCheck(t, s, "api")
	if !reflect.DeepEqual(got.Headers[0].Value.Data, []byte{1, 1}) || !reflect.DeepEqual(got.Headers[1].Value.Data, []byte{9}) || !got.Body.Empty() {
		t.Fatalf("explicit edits: %+v", got)
	}
	if rec := put(`{` + base + `,"headers":[{"name":"x-token","value":{"action":"clear"}}]}`); rec.Code != 204 {
		t.Fatal(rec.Body)
	}
	if got = storedCheck(t, s, "api"); len(got.Headers) != 1 || !got.Headers[0].Value.Empty() {
		t.Fatalf("clear: %+v", got)
	}
}

func TestCheckRoutesRejectBadInputWithoutChangingAnything(t *testing.T) {
	h, s := foundationServer(t)
	if rec := send(h, "POST", "/api/v1/checks", advancedCheckBody("api")); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	before := storedCheck(t, s, "api")
	huge := base64.StdEncoding.EncodeToString(make([]byte, config.MaxSealedHeaderValue+1))
	for _, tc := range []struct {
		label, method, path, body, message string
	}{
		{"unknown field", "PUT", "/api/v1/checks/api", `{` + plainCheck + `,"surprise":1}`, "invalid request body"},
		{"bad action", "PUT", "/api/v1/checks/api", `{` + plainCheck + `,"body":{"action":"drop"}}`, `body.action: edit "drop" must be keep, replace or clear`},
		{"replace without sealed", "PUT", "/api/v1/checks/api", `{` + plainCheck + `,"body":{"action":"replace"}}`, "body.sealed is required to replace"},
		{"sealed with clear", "PUT", "/api/v1/checks/api", `{` + plainCheck + `,"body":{"action":"clear","sealed":` + sealedJSON(serverKey, 1) + `}}`, "body.sealed is only for replace"},
		{"sealed to another key", "PUT", "/api/v1/checks/api", `{` + plainCheck + `,"headers":[{"name":"X","value":{"action":"replace","sealed":` + sealedJSON(bytes.Repeat([]byte{9}, 32), 1) + `}}]}`, "headers[0].value was sealed to a different key"},
		{"header value too large", "PUT", "/api/v1/checks/api", `{` + plainCheck + `,"headers":[{"name":"X","value":{"action":"replace","sealed":{"key":"` + base64.StdEncoding.EncodeToString(serverKey) + `","data":"` + huge + `"}}}]}`, "headers[0].value.sealed.data is too large"},
		{"bad method", "PUT", "/api/v1/checks/api", `{` + plainCheck + `,"method":"TRACE"}`, `method "TRACE" must be one of`},
		{"duplicate header", "PUT", "/api/v1/checks/api", `{` + plainCheck + `,"headers":[{"name":"X-A"},{"name":"x-a"}]}`, `header "x-a" is repeated`},
		{"HEAD with expected text", "PUT", "/api/v1/checks/api", `{` + plainCheck + `,"method":"HEAD","expect_text":"ok"}`, "HEAD does not"},
		{"bad header name", "PUT", "/api/v1/checks/api", `{` + plainCheck + `,"headers":[{"name":"Bad Name"}]}`, `header name "Bad Name" is not valid`},
		{"tcp without host", "PUT", "/api/v1/checks/api", `{"kind":"tcp","interval_ms":60000,"timeout_ms":1000,"port":80}`, "host is required"},
		{"tcp port range", "PUT", "/api/v1/checks/api", `{"kind":"tcp","host":"h","port":70000,"interval_ms":60000,"timeout_ms":1000}`, "port 70000 must be between 1 and 65535"},
		{"unknown kind", "PUT", "/api/v1/checks/api", `{` + plainCheck + `,"kind":"udp"}`, `kind "udp" must be http, tcp or heartbeat`},
		{"create invalid", "POST", "/api/v1/checks", `{"name":"new",` + plainCheck[len(`"name":"api",`):] + `,"method":"TRACE"}`, "method"},
	} {
		rec := send(h, tc.method, tc.path, tc.body)
		var e struct{ Error string }
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if rec.Code != 400 || !strings.Contains(e.Error, tc.message) {
			t.Errorf("%s: %d %q, want 400 containing %q", tc.label, rec.Code, e.Error, tc.message)
		}
	}
	if !storedCheck(t, s, "api").SameDefinition(before) {
		t.Fatal("a rejected request changed the stored check")
	}
	if checks, _ := s.Checks(context.Background()); len(checks) != 1 {
		t.Fatalf("a rejected create stored a check: %d", len(checks))
	}
}

func TestTCPCheckThroughTheAPI(t *testing.T) {
	h, s := foundationServer(t)
	if rec := send(h, "POST", "/api/v1/checks", `{"name":"db","kind":"tcp","host":"2001:db8::1","port":5432,"interval_ms":60000,"timeout_ms":1000}`); rec.Code != 201 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got := storedCheck(t, s, "db")
	if got.Kind != config.CheckTCP || got.Host != "2001:db8::1" || got.Port != 5432 || got.URL != "" {
		t.Fatalf("stored = %+v", got)
	}
}

func TestWebhookChannelRoutes(t *testing.T) {
	h, s := foundationServer(t)
	create := `{"name":"hook","kind":"webhook","webhook_origin":"https://hooks.example.com","webhook_url":` + replaceJSON(1) + `,"webhook_auth_name":"Authorization","webhook_auth":` + replaceJSON(2) + `}`
	rec := send(h, "POST", "/api/v1/channels", create)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	for _, leaked := range []string{`"sealed"`, `"data"`, "AQ=="} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Fatalf("response leaks %q: %s", leaked, rec.Body)
		}
	}
	var dto ChannelDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil || dto.Kind != "webhook" || dto.WebhookOrigin != "https://hooks.example.com" || !dto.WebhookURLSet || !dto.WebhookAuthSet || dto.WebhookAuthName != "Authorization" {
		t.Fatalf("dto = %+v, %v", dto, err)
	}
	ch, _, _ := s.Channel(context.Background(), "hook")
	if !reflect.DeepEqual(ch.WebhookURL.Data, []byte{1}) || !reflect.DeepEqual(ch.WebhookAuth.Data, []byte{2}) {
		t.Fatalf("stored = %+v", ch)
	}
	// Absent keeps, explicit clear clears, the URL cannot be cleared.
	if rec := send(h, "PUT", "/api/v1/channels/hook", `{"kind":"webhook","webhook_origin":"https://hooks.example.com","webhook_auth_name":"Authorization","webhook_auth":{"action":"clear"}}`); rec.Code != 204 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	ch, _, _ = s.Channel(context.Background(), "hook")
	if !reflect.DeepEqual(ch.WebhookURL.Data, []byte{1}) || !ch.WebhookAuth.Empty() {
		t.Fatalf("after update = %+v", ch)
	}
	for label, body := range map[string]string{
		"clear url":      `{"kind":"webhook","webhook_origin":"https://hooks.example.com","webhook_url":{"action":"clear"}}`,
		"origin missing": `{"kind":"webhook"}`,
		"bad origin":     `{"kind":"webhook","webhook_origin":"https://hooks.example.com/path"}`,
		"other key":      `{"kind":"webhook","webhook_origin":"https://h.example.com","webhook_url":{"action":"replace","sealed":` + sealedJSON(bytes.Repeat([]byte{1}, 32), 1) + `}}`,
		"unknown field":  `{"kind":"webhook","webhook_origin":"https://h.example.com","url":"https://h.example.com/secret"}`,
	} {
		if rec := send(h, "PUT", "/api/v1/channels/hook", body); rec.Code != 400 {
			t.Errorf("%s: %d %s", label, rec.Code, rec.Body)
		}
	}
	if after, _, _ := s.Channel(context.Background(), "hook"); !reflect.DeepEqual(after, ch) {
		t.Fatal("rejected updates changed the channel")
	}
}

func windowJSON(zone string) string {
	return `{"weekdays":[6,1],"start_minute":90,"duration_ms":7200000,"zone":"` + zone + `"}`
}

func TestMaintenanceRoutes(t *testing.T) {
	h, s := foundationServer(t)
	if rec := send(h, "POST", "/api/v1/checks", `{`+plainCheck+`}`); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	rec := send(h, "POST", "/api/v1/checks/api/maintenance", windowJSON("America/New_York"))
	var created MaintenanceDTO
	if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &created) != nil || created.ID == 0 || !reflect.DeepEqual(created.Weekdays, []int{1, 6}) || created.DurationMS != 7200000 || created.Zone != "America/New_York" || created.StartMinute != 90 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	id := "/api/v1/checks/api/maintenance/" + jsonNumber(created.ID)
	if rec := send(h, "PUT", id, `{"weekdays":[0],"start_minute":0,"duration_ms":60000,"zone":"UTC"}`); rec.Code != 204 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	rec = send(h, "GET", "/api/v1/checks/api/maintenance", "")
	var list []MaintenanceDTO
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list) != 1 || list[0].Zone != "UTC" || list[0].DurationMS != 60000 || !reflect.DeepEqual(list[0].Weekdays, []int{0}) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	before, _ := s.AllMaintenanceWindows(context.Background())
	for _, tc := range []struct {
		label, method, path, body string
		status                    int
	}{
		{"invalid zone", "POST", "/api/v1/checks/api/maintenance", windowJSON("Mars/Base"), 400},
		{"no weekdays", "POST", "/api/v1/checks/api/maintenance", `{"weekdays":[],"duration_ms":60000,"zone":"UTC"}`, 400},
		{"weekday out of range", "POST", "/api/v1/checks/api/maintenance", `{"weekdays":[7],"duration_ms":60000,"zone":"UTC"}`, 400},
		{"duration too short", "POST", "/api/v1/checks/api/maintenance", `{"weekdays":[1],"duration_ms":59999,"zone":"UTC"}`, 400},
		{"duration overflow", "POST", "/api/v1/checks/api/maintenance", `{"weekdays":[1],"duration_ms":9223372036854775807,"zone":"UTC"}`, 400},
		{"unknown field", "POST", "/api/v1/checks/api/maintenance", `{"weekdays":[1],"duration_ms":60000,"zone":"UTC","check":"x"}`, 400},
		{"unknown check", "POST", "/api/v1/checks/ghost/maintenance", windowJSON("UTC"), 404},
		{"unknown id", "PUT", "/api/v1/checks/api/maintenance/999", windowJSON("UTC"), 404},
		{"delete unknown id", "DELETE", "/api/v1/checks/api/maintenance/999", "", 404},
		{"id not a number", "DELETE", "/api/v1/checks/api/maintenance/abc", "", 404},
		{"collection method", "DELETE", "/api/v1/checks/api/maintenance", "", 405},
		{"item method", "GET", id, "", 405},
	} {
		if rec := send(h, tc.method, tc.path, tc.body); rec.Code != tc.status {
			t.Errorf("%s: %d %s, want %d", tc.label, rec.Code, rec.Body, tc.status)
		}
	}
	if after, _ := s.AllMaintenanceWindows(context.Background()); !reflect.DeepEqual(after, before) {
		t.Fatal("rejected requests changed the windows")
	}
	if rec := do(h, "GET", "/api/v1/checks/api/maintenance", nil, nil); rec.Code != 401 {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
	if rec := send(h, "DELETE", id, ""); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, "GET", "/api/v1/checks/api/maintenance", ""); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty list = %q", rec.Body)
	}
}

func jsonNumber(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestChannelTestRoutes(t *testing.T) {
	h, _ := foundationServer(t)
	if rec := send(h, "POST", "/api/v1/channels", `{"name":"hook","kind":"webhook","webhook_origin":"https://h.example.com","webhook_url":`+replaceJSON(1)+`}`); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	if rec := send(h, "GET", "/api/v1/channels/hook/test", ""); rec.Code != 404 {
		t.Fatalf("no test yet: %d %s", rec.Code, rec.Body)
	}
	rec := send(h, "POST", "/api/v1/channels/hook/test", "")
	var dto ChannelTestDTO
	if rec.Code != 202 || json.Unmarshal(rec.Body.Bytes(), &dto) != nil || dto.ID == 0 || dto.Channel != "hook" || dto.Status != "pending" || dto.Error != "" {
		t.Fatalf("enqueue: %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, "POST", "/api/v1/channels/hook/test", ""); rec.Code != 409 {
		t.Fatalf("second active test: %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, "POST", "/api/v1/channels/ghost/test", ""); rec.Code != 404 {
		t.Fatalf("unknown channel: %d", rec.Code)
	}
	rec = send(h, "GET", "/api/v1/channels/hook/test", "")
	var latest ChannelTestDTO
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &latest) != nil || latest.ID != dto.ID || latest.Status != "pending" {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, "DELETE", "/api/v1/channels/hook/test", ""); rec.Code != 405 || rec.Header().Get("Allow") != "GET, POST" {
		t.Fatalf("method: %d %q", rec.Code, rec.Header().Get("Allow"))
	}
	if rec := do(h, "POST", "/api/v1/channels/hook/test", nil, nil); rec.Code != 401 {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
	// The existing channel routes still reject extra path segments.
	if rec := send(h, "PUT", "/api/v1/channels/hook/other", `{}`); rec.Code != 404 {
		t.Fatalf("other sub-path: %d", rec.Code)
	}
}

func TestSnapshotDTOCarriesTheFoundationFieldsWithoutSecrets(t *testing.T) {
	now := time.Date(2026, 3, 2, 14, 30, 0, 0, time.UTC)
	sealed := func(b byte) secret.Sealed { return secret.Sealed{Key: serverKey, Data: bytes.Repeat([]byte{b}, 40)} }
	snap := tui.Snapshot{PublicKey: serverKey, HasKey: true,
		Services: []tui.Service{
			{Check: config.Check{Name: "api", URL: "https://example.com", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200, Method: "POST",
				Headers: []config.Header{{Name: "X-Token", Value: sealed(1)}, {Name: "Empty"}}, Body: sealed(2), ExpectText: "pong"},
				Maintenance:   []config.MaintenanceWindow{{ID: 4, Check: "api", Weekdays: []time.Weekday{1, 3}, StartMinute: 540, Duration: time.Hour, Zone: "UTC"}},
				InMaintenance: config.Maintenance{Active: true, End: now.Add(time.Hour)}, KnownAlerting: true},
			{Check: config.Check{Name: "db", Kind: config.CheckTCP, Host: "::1", Port: 5432, Interval: time.Minute, Timeout: time.Second}},
		},
		Channels: []config.Channel{{Name: "hook", Kind: config.KindWebhook, WebhookOrigin: "https://hooks.example.com",
			WebhookURL: sealed(3), WebhookAuthName: "Authorization", WebhookAuth: sealed(4)}, {Name: "bare", Kind: config.KindWebhook, WebhookOrigin: "http://x", WebhookURL: sealed(5)}},
		ChannelTests: []config.ChannelTest{{ID: 8, Channel: "hook", Status: config.TestError, Error: "refused", Requested: now, Started: now, Finished: now}}}
	dto := NewSnapshotDTO(snap, now)
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []byte{1, 2, 3, 4, 5} {
		v := bytes.Repeat([]byte{b}, 40)
		if bytes.Contains(raw, v) || bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(v))) {
			t.Fatalf("secret %d leaked: %s", b, raw)
		}
	}
	for _, leaked := range []string{`"sealed"`, `"data"`, `"action"`} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("snapshot has write-only field %s: %s", leaked, raw)
		}
	}
	for _, want := range []string{`"kind":"http"`, `"method":"POST"`, `"body_set":true`, `"value_set":true`, `"value_set":false`, `"expect_text":"pong"`,
		`"kind":"tcp"`, `"host":"::1"`, `"port":5432`, `"webhook_origin":"https://hooks.example.com"`, `"webhook_url_set":true`, `"webhook_auth_name":"Authorization"`, `"webhook_auth_set":true`, `"webhook_auth_set":false`,
		`"in_maintenance":true`, `"known_alerting":true`, `"maintenance_end":"2026-03-02T15:30:00Z"`, `"weekdays":[1,3]`, `"duration_ms":3600000`, `"channel_tests":[`, `"status":"error"`, `"error":"refused"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("snapshot lacks %s:\n%s", want, raw)
		}
	}
	// A secret URL never appears; only scheme and host are shown.
	if strings.Contains(string(raw), "/secret") {
		t.Fatal("path leaked")
	}
	// Round trip: the client rebuilds every plain field and "set" placeholders.
	got := dto.Snapshot()
	api := got.Services[0]
	if api.Check.Method != "POST" || api.Check.ExpectText != "pong" || len(api.Check.Headers) != 2 || api.Check.Headers[0].Name != "X-Token" || api.Check.Headers[0].Value.Empty() || !api.Check.Headers[1].Value.Empty() || api.Check.Body.Empty() || api.Check.Kind != "" {
		t.Fatalf("check = %+v", api.Check)
	}
	if !reflect.DeepEqual(api.Maintenance, snap.Services[0].Maintenance) || !api.KnownAlerting || !api.InMaintenance.Active || !api.InMaintenance.End.Equal(now.Add(time.Hour)) {
		t.Fatalf("maintenance = %+v %+v", api.Maintenance, api.InMaintenance)
	}
	if db := got.Services[1].Check; db.Kind != config.CheckTCP || db.Host != "::1" || db.Port != 5432 || db.Method != "" {
		t.Fatalf("tcp = %+v", db)
	}
	if h := got.Channels[0]; h.Kind != config.KindWebhook || h.WebhookOrigin != "https://hooks.example.com" || h.WebhookURL.Empty() || h.WebhookAuth.Empty() || h.WebhookAuthName != "Authorization" || !got.Channels[1].WebhookAuth.Empty() {
		t.Fatalf("channel = %+v", h)
	}
	if !reflect.DeepEqual(got.ChannelTests, snap.ChannelTests) {
		t.Fatalf("channel tests = %+v", got.ChannelTests)
	}
}
