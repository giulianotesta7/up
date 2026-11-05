package store_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
)

func seal(b ...byte) secret.Sealed { return secret.Sealed{Key: []byte{7}, Data: b} }

func advanced(name string) config.Check {
	c := definition(name)
	c.Method = "POST"
	c.Headers = []config.Header{{Name: "X-Token", Value: seal(1), Edit: config.EditReplace}, {Name: "Accept", Value: seal(2), Edit: config.EditReplace}}
	c.Body, c.BodyEdit = seal(3, 3), config.EditReplace
	c.ExpectText = "pong"
	return c
}

func tcp(name string) config.Check {
	return config.Check{Name: name, Kind: config.CheckTCP, Host: "2001:db8::1", Port: 5432, Interval: 30 * time.Second, Timeout: 5 * time.Second}
}

func stored(t *testing.T, s *store.Store, name string) config.Check {
	t.Helper()
	checks, err := s.Checks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q not stored", name)
	return config.Check{}
}

func TestChecksRoundTripNewFields(t *testing.T) {
	s, _ := seeded(t)
	for _, c := range []config.Check{advanced("new"), tcp("db")} {
		if err := s.AddCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
		got := stored(t, s, c.Name)
		if !got.SameDefinition(c) {
			t.Fatalf("round trip lost a field:\n got %+v\nwant %+v", got, c)
		}
		for _, h := range got.Headers {
			if h.Edit != config.EditKeep {
				t.Fatalf("stored header %q carries edit %v", h.Name, h.Edit)
			}
		}
	}
	// Existing checks keep their zero-value form: HTTP, GET, nothing protected.
	if got := stored(t, s, "api"); !reflect.DeepEqual(got, definition("api")) {
		t.Fatalf("plain check = %+v", got)
	}
	// "http" and "GET" are spellings of the zero values.
	c := definition("spelled")
	c.Kind, c.Method = config.CheckHTTP, "GET"
	if err := s.AddCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, s, "spelled"); got.Kind != "" || got.Method != "" {
		t.Fatalf("stored kind %q method %q, want canonical empty", got.Kind, got.Method)
	}
	// SetChecks keeps sealed values read from the store.
	all, _ := s.Checks(ctx)
	if err := s.SetChecks(ctx, all); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, s, "new"); !got.SameDefinition(advanced("new")) {
		t.Fatalf("SetChecks lost sealed values: %+v", got)
	}
}

func TestAddCheckRejectsInvalidNewFieldsUnchanged(t *testing.T) {
	s, path := seeded(t)
	before := diskState(t, path)
	bad := advanced("bad")
	bad.Method = "TRACE"
	tcpBad := tcp("bad")
	tcpBad.Port = 0
	for _, c := range []config.Check{bad, tcpBad} {
		if err := s.AddCheck(ctx, c); !errors.Is(err, store.ErrValidation) {
			t.Fatalf("AddCheck(%+v) = %v, want ErrValidation", c, err)
		}
		if err := s.UpdateCheck(ctx, c); !errors.Is(err, store.ErrValidation) {
			t.Fatalf("UpdateCheck = %v, want ErrValidation", err)
		}
	}
	if !reflect.DeepEqual(diskState(t, path), before) {
		t.Fatal("rejected writes changed the database")
	}
}

func TestUpdateCheckProtectedValueEdits(t *testing.T) {
	s, _ := seeded(t)
	if err := s.AddCheck(ctx, advanced("new")); err != nil {
		t.Fatal(err)
	}
	update := func(edit func(*config.Check)) config.Check {
		t.Helper()
		c := stored(t, s, "new")
		edit(&c)
		if err := s.UpdateCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
		return stored(t, s, "new")
	}
	// Keep (the zero edit): values read from the store, even if the caller
	// holds different bytes, stay as stored.
	got := update(func(c *config.Check) {
		c.Headers[0].Value = seal(99)
		c.Body = seal(99)
		c.ExpectText = "changed"
	})
	if !reflect.DeepEqual(got.Headers[0].Value, seal(1)) || !reflect.DeepEqual(got.Body, seal(3, 3)) || got.ExpectText != "changed" {
		t.Fatalf("keep did not retain stored values: %+v", got)
	}
	// Replace one header and the body; keep the other, matched by name.
	got = update(func(c *config.Check) {
		c.Headers = []config.Header{{Name: "ACCEPT", Edit: config.EditKeep}, {Name: "X-Token", Value: seal(5), Edit: config.EditReplace}}
		c.Body, c.BodyEdit = seal(6), config.EditReplace
	})
	if got.Headers[0].Name != "ACCEPT" || !reflect.DeepEqual(got.Headers[0].Value, seal(2)) || !reflect.DeepEqual(got.Headers[1].Value, seal(5)) || !reflect.DeepEqual(got.Body, seal(6)) {
		t.Fatalf("replace/keep by name: %+v", got)
	}
	// Clear a header value and the body; a header absent from the list goes.
	got = update(func(c *config.Check) {
		c.Headers = []config.Header{{Name: "X-Token", Edit: config.EditClear}}
		c.BodyEdit = config.EditClear
	})
	if len(got.Headers) != 1 || !got.Headers[0].Value.Empty() || !got.Body.Empty() {
		t.Fatalf("clear: %+v", got)
	}
	// Keep with nothing stored behind it is an empty value, not an error.
	got = update(func(c *config.Check) { c.Headers = []config.Header{{Name: "New", Edit: config.EditKeep}} })
	if len(got.Headers) != 1 || !got.Headers[0].Value.Empty() {
		t.Fatalf("keep of an unknown header: %+v", got)
	}
	// An update that fails validation leaves the stored values alone.
	before := stored(t, s, "new")
	bad := before
	bad.Headers = []config.Header{{Name: "X", Edit: config.EditReplace}}
	if err := s.UpdateCheck(ctx, bad); !errors.Is(err, store.ErrValidation) || !stored(t, s, "new").SameDefinition(before) {
		t.Fatalf("invalid replace: %v", err)
	}
	// Switching to TCP drops HTTP-only values (the caller sends none).
	got = update(func(c *config.Check) { *c = tcp("new") })
	if got.Kind != config.CheckTCP || got.Headers != nil || !got.Body.Empty() {
		t.Fatalf("switch to tcp: %+v", got)
	}
}

// Header names are unique without regard to case: keep-by-name could
// otherwise keep the wrong value after a row is removed.
func TestDuplicateHeaderNamesAreRejected(t *testing.T) {
	s, _ := seeded(t)
	c := definition("dup")
	c.Headers = []config.Header{{Name: "X", Value: seal(1), Edit: config.EditReplace}, {Name: "x", Value: seal(2), Edit: config.EditReplace}}
	if err := s.AddCheck(ctx, c); !errors.Is(err, store.ErrValidation) {
		t.Fatalf("AddCheck = %v, want ErrValidation", err)
	}
	checks, err := s.Checks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range checks {
		if got.Name == "dup" {
			t.Fatal("rejected check was stored")
		}
	}
}

// Editing any executable field restarts the check: the old definition's
// in-flight result is refused and the new definition's is stored.
func TestEditingEachNewFieldDropsAStaleResult(t *testing.T) {
	edits := map[string]func(*config.Check){
		"method":       func(c *config.Check) { c.Method = "PUT" },
		"header name":  func(c *config.Check) { c.Headers[0].Name = "X-Other" },
		"header value": func(c *config.Check) { c.Headers[0].Value, c.Headers[0].Edit = seal(42), config.EditReplace },
		"header added": func(c *config.Check) {
			c.Headers = append(c.Headers, config.Header{Name: "Z", Value: seal(9), Edit: config.EditReplace})
		},
		"header order": func(c *config.Check) { c.Headers[0], c.Headers[1] = c.Headers[1], c.Headers[0] },
		"body":         func(c *config.Check) { c.Body, c.BodyEdit = seal(43), config.EditReplace },
		"body cleared": func(c *config.Check) { c.BodyEdit = config.EditClear },
		"expect text":  func(c *config.Check) { c.ExpectText = "other" },
		"kind":         func(c *config.Check) { *c = tcp(c.Name) },
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			s, _ := seeded(t)
			if err := s.AddCheck(ctx, advanced("new")); err != nil {
				t.Fatal(err)
			}
			started := stored(t, s, "new")
			next := started
			next.Headers = append([]config.Header(nil), started.Headers...)
			for i := range next.Headers {
				next.Headers[i].Edit = config.EditKeep
			}
			edit(&next)
			if err := s.UpdateCheck(ctx, next); err != nil {
				t.Fatal(err)
			}
			r := check.Result{CheckName: "new", At: at, Status: check.StatusUp}
			if ok, err := s.RecordDefined(ctx, started, r); err != nil || ok {
				t.Fatalf("stale RecordDefined = %v, %v, want refused", ok, err)
			}
			if ok, err := s.RecordDefined(ctx, stored(t, s, "new"), r); err != nil || !ok {
				t.Fatalf("current RecordDefined = %v, %v, want stored", ok, err)
			}
			if started.SameDefinition(stored(t, s, "new")) {
				t.Fatal("SameDefinition missed the edit")
			}
		})
	}
}

func TestRecordDefinedAcceptsUnchangedAdvancedDefinition(t *testing.T) {
	s, _ := seeded(t)
	if err := s.AddCheck(ctx, advanced("new")); err != nil {
		t.Fatal(err)
	}
	r := check.Result{CheckName: "new", At: at, Status: check.StatusDown, Error: "x"}
	if ok, err := s.RecordDefined(ctx, stored(t, s, "new"), r); err != nil || !ok {
		t.Fatalf("RecordDefined = %v, %v", ok, err)
	}
	// Channel and alert settings do not change a measurement.
	c := stored(t, s, "new")
	c.AlertAfter = 3
	if err := s.UpdateCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.RecordDefined(ctx, stored(t, s, "new"), r); err != nil || !ok {
		t.Fatalf("RecordDefined after alert edit = %v, %v", ok, err)
	}
	other := advanced("new")
	other.Body = seal(77)
	if ok, err := s.RecordDefined(ctx, other, r); err != nil || ok {
		t.Fatalf("RecordDefined with another body = %v, %v, want refused", ok, err)
	}
}
