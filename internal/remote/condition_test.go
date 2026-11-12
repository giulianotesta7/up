package remote

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/tui"
)

const conditionsJSON = `"conditions":[{"kind":"json","path":"services.0.status","value":"ok"},{"kind":"regex","pattern":"(?i)ready"},{"kind":"response_time","within_ms":800},{"kind":"header","name":"X-Version","value":"3"}]`

var wantConditions = []config.Condition{
	{Kind: config.ConditionJSON, Path: "services.0.status", Value: "ok"},
	{Kind: config.ConditionRegex, Pattern: "(?i)ready"},
	{Kind: config.ConditionResponseTime, Within: 800 * time.Millisecond},
	{Kind: config.ConditionHeader, Name: "X-Version", Value: "3"},
}

func TestCheckRoutesStoreConditionsInOrder(t *testing.T) {
	h, s := foundationServer(t)
	rec := send(h, "POST", "/api/v1/checks", `{`+plainCheck+`,`+conditionsJSON+`}`)
	if rec.Code != 201 {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	if got := storedCheck(t, s, "api").Conditions; !reflect.DeepEqual(got, wantConditions) {
		t.Fatalf("stored conditions = %+v", got)
	}
	var echo CheckDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &echo); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.Body.String(), conditionsJSON) {
		t.Fatalf("echo lacks conditions: %s", rec.Body)
	}
	// An update without conditions removes them; a copy keeps the ones it sends.
	if rec := send(h, "PUT", "/api/v1/checks/api", `{`+plainCheck+`}`); rec.Code != 204 {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	}
	if got := storedCheck(t, s, "api").Conditions; got != nil {
		t.Fatalf("conditions after update without them = %+v", got)
	}
	copyBody := `{"name":"api copy","copy_from":"api","url":"https://example.com","interval_ms":60000,"timeout_ms":1000,"expected_status":200,` + conditionsJSON + `}`
	if rec := send(h, "POST", "/api/v1/checks", copyBody); rec.Code != 201 {
		t.Fatalf("copy = %d %s", rec.Code, rec.Body)
	}
	if got := storedCheck(t, s, "api copy").Conditions; !reflect.DeepEqual(got, wantConditions) {
		t.Fatalf("copied conditions = %+v", got)
	}
}

func TestCheckRoutesRejectBadConditionsWithoutChangingAnything(t *testing.T) {
	h, s := foundationServer(t)
	if rec := send(h, "POST", "/api/v1/checks", `{`+plainCheck+`,`+conditionsJSON+`}`); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	before := storedCheck(t, s, "api")
	for _, tc := range []struct{ label, conds, message string }{
		{"invalid regex", `[{"kind":"regex","pattern":"("}]`, `condition 1: body regex "(" is not valid: missing closing )`},
		{"malformed path", `[{"kind":"json","path":"a..b","value":"1"}]`, `condition 1: JSON path "a..b" is not valid`},
		{"empty header name", `[{"kind":"header","value":"1"}]`, "condition 1: header name is required"},
		{"time not below timeout", `[{"kind":"response_time","within_ms":1000}]`, "condition 1: max response time (1s) must be lower than timeout (1s)"},
		{"time out of range", `[{"kind":"response_time","within_ms":9223372036854775807}]`, "conditions[0].within_ms is out of range"},
		{"json with a pattern", `[{"kind":"json","path":"a","value":"1","pattern":"x"}]`, "condition 1: pattern is not used by JSON value conditions"},
		{"time with value and path", `[{"kind":"response_time","within_ms":500,"value":"1","path":"a"}]`, "condition 1: path and value are not used by max response time conditions"},
		{"header with a limit", `[{"kind":"header","name":"X-A","within_ms":500}]`, "condition 1: max response time is not used by response header conditions"},
		{"unknown field", `[{"kind":"json","path":"a","value":"1","op":"eq"}]`, "invalid request body"},
	} {
		rec := send(h, "PUT", "/api/v1/checks/api", `{`+plainCheck+`,"conditions":`+tc.conds+`}`)
		var e struct{ Error string }
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if rec.Code != 400 || !strings.Contains(e.Error, tc.message) {
			t.Errorf("%s: %d %q, want 400 containing %q", tc.label, rec.Code, e.Error, tc.message)
		}
	}
	if got := storedCheck(t, s, "api"); !got.SameDefinition(before) {
		t.Fatalf("a rejected request changed the check: %+v", got)
	}
}

func TestSnapshotCarriesConditions(t *testing.T) {
	c := config.Check{Name: "api", URL: "https://example.com", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200, Conditions: wantConditions}
	dto := NewSnapshotDTO(tui.Snapshot{Services: []tui.Service{{Check: c}, {Check: config.Check{Name: "plain", URL: "https://example.org", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200}}}}, time.Now())
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), conditionsJSON) {
		t.Fatalf("snapshot lacks conditions:\n%s", raw)
	}
	var back SnapshotDTO
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got := back.Snapshot()
	if !reflect.DeepEqual(got.Services[0].Check.Conditions, wantConditions) || got.Services[1].Check.Conditions != nil {
		t.Fatalf("round trip conditions = %+v / %+v", got.Services[0].Check.Conditions, got.Services[1].Check.Conditions)
	}
}
