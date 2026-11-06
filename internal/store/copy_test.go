package store_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

func keepValues(c config.Check, name string) config.Check {
	c.Name, c.BodyEdit = name, config.EditKeep
	c.Body = config.Check{}.Body
	headers := make([]config.Header, len(c.Headers))
	for i, h := range c.Headers {
		headers[i] = config.Header{Name: h.Name, Edit: config.EditKeep}
	}
	c.Headers = headers
	return c
}

func TestAddCheckFromCopiesSealedValuesFromTheSource(t *testing.T) {
	s, _ := seeded(t)
	src := advanced("adv")
	if err := s.AddCheck(ctx, src); err != nil {
		t.Fatal(err)
	}
	before := observe(t, s)
	if err := s.AddCheckFrom(ctx, "adv", keepValues(src, "new")); err != nil {
		t.Fatal(err)
	}
	got := stored(t, s, "new")
	want := src
	want.Name = "new"
	for i := range want.Headers {
		want.Headers[i].Edit = config.EditKeep
	}
	want.BodyEdit = config.EditKeep
	if !got.SameDefinition(want) {
		t.Fatalf("copy = %+v, want %+v", got, want)
	}
	if !stored(t, s, "adv").SameDefinition(src) {
		t.Fatalf("source changed: %+v", stored(t, s, "adv"))
	}
	after := observe(t, s)
	if len(after.Checks) != len(before.Checks)+1 || len(after.Recent["new"]) != 0 || len(after.Outages["new"]) != 0 {
		t.Fatalf("copy gained state: %+v", after)
	}
}

func TestAddCheckFromReplaceAndClearOverrideTheSource(t *testing.T) {
	s, _ := seeded(t)
	src := advanced("adv")
	if err := s.AddCheck(ctx, src); err != nil {
		t.Fatal(err)
	}
	c := keepValues(src, "new")
	c.Headers[0] = config.Header{Name: "X-Token", Value: seal(9), Edit: config.EditReplace}
	c.Headers[1].Edit = config.EditClear
	c.BodyEdit = config.EditClear
	if err := s.AddCheckFrom(ctx, "adv", c); err != nil {
		t.Fatal(err)
	}
	got := stored(t, s, "new")
	if len(got.Headers) != 2 || !reflect.DeepEqual(got.Headers[0].Value, seal(9)) || !got.Headers[1].Value.Empty() || !got.Body.Empty() {
		t.Fatalf("copy = %+v", got)
	}
	if !stored(t, s, "adv").SameDefinition(src) {
		t.Fatal("source changed")
	}
}

func TestAddCheckFromMissingSourceStoresNothing(t *testing.T) {
	s, _ := seeded(t)
	before := observe(t, s)
	err := s.AddCheckFrom(ctx, "gone", keepValues(advanced("x"), "new"))
	if !errors.Is(err, store.ErrNotFound) || err.Error() != `store: add check: source monitor "gone" not found` {
		t.Fatalf("err = %v", err)
	}
	assertUnchanged(t, s, before)
}

func TestAddCheckFromRejectsATakenNameAndInvalidCopy(t *testing.T) {
	s, _ := seeded(t)
	before := observe(t, s)
	if err := s.AddCheckFrom(ctx, "api", definition("web")); !errors.Is(err, store.ErrExists) {
		t.Fatalf("taken name err = %v", err)
	}
	bad := definition("new")
	bad.Interval = 0
	if err := s.AddCheckFrom(ctx, "api", bad); !errors.Is(err, store.ErrValidation) {
		t.Fatalf("invalid err = %v", err)
	}
	assertUnchanged(t, s, before)
}

func TestAddCheckFromNeverGivesATCPCheckHTTPValues(t *testing.T) {
	s, _ := seeded(t)
	if err := s.AddCheck(ctx, advanced("adv")); err != nil {
		t.Fatal(err)
	}
	c := tcp("new")
	c.BodyEdit = config.EditKeep
	if err := s.AddCheckFrom(ctx, "adv", c); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, s, "new"); !got.Body.Empty() || len(got.Headers) != 0 {
		t.Fatalf("tcp copy = %+v", got)
	}
}

func TestAddCheckFromWithAnotherTargetCopiesValuesAndLeavesTheSource(t *testing.T) {
	s, _ := seeded(t)
	src := advanced("adv")
	if err := s.AddCheck(ctx, src); err != nil {
		t.Fatal(err)
	}
	c := keepValues(src, "new")
	c.URL = "https://other.example.net/health"
	if err := s.AddCheckFrom(ctx, "adv", c); err != nil {
		t.Fatal(err)
	}
	got := stored(t, s, "new")
	if got.URL != c.URL || !reflect.DeepEqual(got.Body, src.Body) || len(got.Headers) != len(src.Headers) {
		t.Fatalf("copy = %+v", got)
	}
	for i, h := range got.Headers {
		if h.Name != src.Headers[i].Name || !reflect.DeepEqual(h.Value, src.Headers[i].Value) {
			t.Fatalf("header %d = %+v, want %+v", i, h, src.Headers[i])
		}
	}
	if !stored(t, s, "adv").SameDefinition(src) {
		t.Fatalf("source changed: %+v", stored(t, s, "adv"))
	}
}
