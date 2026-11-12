package store_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/store"
)

func TestConditionsRoundTripInOrderThroughEveryWrite(t *testing.T) {
	s, path := seeded(t)
	src := advanced("adv")
	if err := s.AddCheck(ctx, src); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, s, "adv").Conditions; !reflect.DeepEqual(got, src.Conditions) {
		t.Fatalf("AddCheck conditions = %+v, want %+v", got, src.Conditions)
	}
	// Plain monitors read no conditions.
	if got := stored(t, s, "api").Conditions; got != nil {
		t.Fatalf("plain monitor conditions = %#v", got)
	}
	// An edit replaces the list.
	next := keepValues(stored(t, s, "adv"), "adv")
	next.Conditions = []config.Condition{{Kind: config.ConditionHeader, Name: "X-B"}, next.Conditions[0]}
	if err := s.UpdateCheck(ctx, next); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, s, "adv").Conditions; !reflect.DeepEqual(got, next.Conditions) {
		t.Fatalf("UpdateCheck conditions = %+v, want %+v", got, next.Conditions)
	}
	// A copy keeps the conditions it is given.
	if err := s.AddCheckFrom(ctx, "adv", keepValues(stored(t, s, "adv"), "adv copy")); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, s, "adv copy").Conditions; !reflect.DeepEqual(got, next.Conditions) {
		t.Fatalf("AddCheckFrom conditions = %+v, want %+v", got, next.Conditions)
	}
	// SetChecks writes back what it read.
	all, err := s.Checks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetChecks(ctx, all); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, s, "adv").Conditions; !reflect.DeepEqual(got, next.Conditions) {
		t.Fatalf("SetChecks conditions = %+v", got)
	}
	// The values survive a reopen.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	checks, err := ro.Checks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range checks {
		if c.Name == "adv" && !reflect.DeepEqual(c.Conditions, next.Conditions) {
			t.Fatalf("reopened conditions = %+v", c.Conditions)
		}
	}
}

func TestInvalidConditionsAreRejectedUnchanged(t *testing.T) {
	s, path := seeded(t)
	if err := s.AddCheck(ctx, advanced("adv")); err != nil {
		t.Fatal(err)
	}
	before := diskState(t, path)
	for name, cond := range map[string]config.Condition{
		"regex":         {Kind: config.ConditionRegex, Pattern: "("},
		"path":          {Kind: config.ConditionJSON, Path: "a..b"},
		"header name":   {Kind: config.ConditionHeader},
		"response time": {Kind: config.ConditionResponseTime, Within: 5 * time.Second},
	} {
		c := keepValues(advanced("adv"), "adv")
		c.Conditions = []config.Condition{cond}
		if err := s.UpdateCheck(ctx, c); !errors.Is(err, store.ErrValidation) {
			t.Fatalf("%s: UpdateCheck = %v, want ErrValidation", name, err)
		}
		c.Name = "other"
		if err := s.AddCheck(ctx, c); !errors.Is(err, store.ErrValidation) {
			t.Fatalf("%s: AddCheck = %v, want ErrValidation", name, err)
		}
	}
	if !reflect.DeepEqual(diskState(t, path), before) {
		t.Fatal("rejected writes changed the database")
	}
}
