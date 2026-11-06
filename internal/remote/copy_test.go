package remote

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const copyPlain = `"url":"https://example.com","interval_ms":60000,"timeout_ms":1000,"expected_status":200,"method":"POST","expect_text":"pong"`

func TestCreateCopiesTheSourceSealedValuesWithoutEchoingThem(t *testing.T) {
	h, s := foundationServer(t)
	if rec := send(h, "POST", "/api/v1/checks", advancedCheckBody("api")); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	rec := send(h, "POST", "/api/v1/checks", `{"name":"api copy","copy_from":"api",`+copyPlain+`,"headers":[{"name":"X-Token"},{"name":"Accept"}]}`)
	if rec.Code != 201 {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	src, got := storedCheck(t, s, "api"), storedCheck(t, s, "api copy")
	if !reflect.DeepEqual(got.Body, src.Body) || !reflect.DeepEqual(got.Headers[0].Value, src.Headers[0].Value) || !got.Headers[1].Value.Empty() || got.Method != "POST" {
		t.Fatalf("copy = %+v, source = %+v", got, src)
	}
	var echo CheckDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &echo); err != nil {
		t.Fatal(err)
	}
	if echo.Name != "api copy" || !echo.BodySet || !echo.Headers[0].ValueSet || echo.Headers[1].ValueSet {
		t.Fatalf("echo = %+v", echo)
	}
	for _, leaked := range []string{`"sealed"`, `"data"`, `"copy_from"`, base64.StdEncoding.EncodeToString(serverKey)} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Fatalf("response leaks %q: %s", leaked, rec.Body)
		}
	}
}

func TestCreateCopyReplaceAndClearOverrideTheSource(t *testing.T) {
	h, s := foundationServer(t)
	if rec := send(h, "POST", "/api/v1/checks", advancedCheckBody("api")); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	rec := send(h, "POST", "/api/v1/checks", `{"name":"api copy","copy_from":"api",`+copyPlain+`,"headers":[{"name":"X-Token","value":`+replaceJSON(9)+`},{"name":"Accept","value":{"action":"clear"}}],"body":{"action":"clear"}}`)
	if rec.Code != 201 {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	got := storedCheck(t, s, "api copy")
	if !reflect.DeepEqual(got.Headers[0].Value.Data, []byte{9}) || !got.Body.Empty() {
		t.Fatalf("copy = %+v", got)
	}
	if src := storedCheck(t, s, "api"); !reflect.DeepEqual(src.Body.Data, []byte{2, 2, 2}) || !reflect.DeepEqual(src.Headers[0].Value.Data, []byte{1, 1}) {
		t.Fatalf("source changed: %+v", src)
	}
}

func TestCreateCopyFromMissingSourceStoresNothing(t *testing.T) {
	h, s := foundationServer(t)
	rec := send(h, "POST", "/api/v1/checks", `{"name":"api copy","copy_from":"gone",`+copyPlain+`}`)
	if rec.Code != 404 || rec.Body.String() != `{"error":"store: add check: source monitor \"gone\" not found"}`+"\n" {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	if checks, _ := s.Checks(t.Context()); len(checks) != 0 {
		t.Fatalf("stored %+v", checks)
	}
}

func TestCreateWithoutCopyFromBehavesAsBefore(t *testing.T) {
	h, s := foundationServer(t)
	if rec := send(h, "POST", "/api/v1/checks", advancedCheckBody("api")); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	// Keep on a plain create stores nothing: there is no source to keep from.
	rec := send(h, "POST", "/api/v1/checks", `{"name":"plain",`+copyPlain+`,"headers":[{"name":"X-Token"}]}`)
	if rec.Code != 201 {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	if got := storedCheck(t, s, "plain"); !got.Body.Empty() || !got.Headers[0].Value.Empty() {
		t.Fatalf("plain create took values: %+v", got)
	}
	if rec := send(h, "POST", "/api/v1/checks", `{"name":"x","copy_from":"","url":"https://example.com"}`); rec.Code != 400 {
		t.Fatalf("empty copy_from = %d %s", rec.Code, rec.Body)
	}
}

func TestCopyFromIsOnlyForCreate(t *testing.T) {
	h, _ := foundationServer(t)
	if rec := send(h, "POST", "/api/v1/checks", advancedCheckBody("api")); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	if rec := send(h, "PUT", "/api/v1/checks/api", `{"copy_from":"api",`+copyPlain+`}`); rec.Code != 400 {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	}
}
