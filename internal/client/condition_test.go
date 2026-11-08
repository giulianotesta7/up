package client

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

// sourceConditions are the duplicate source's response conditions.
var sourceConditions = []config.Condition{
	{Kind: config.ConditionJSON, Path: "services.0.status", Value: "ok"},
	{Kind: config.ConditionRegex, Pattern: "(?i)ready"},
	{Kind: config.ConditionResponseTime, Within: 800 * time.Millisecond},
	{Kind: config.ConditionHeader, Name: "X-Version", Value: "3"},
}

func TestConditionsSurviveDuplicateAndEdit(t *testing.T) {
	for name, backend := range map[string]func(*testing.T) webhookBackend{"local": localBackend, "remote": remoteBackend} {
		t.Run(name, func(t *testing.T) {
			b, d, _ := duplicateBackend(t, backend)
			d.press(kY)
			d.save()
			if got, ok := b.stored(t, "api copy"); !ok || !reflect.DeepEqual(got.Conditions, sourceConditions) {
				t.Fatalf("copy conditions = %+v (stored %v)", got.Conditions, ok)
			}
			b2, d2, _ := duplicateBackend(t, backend)
			d2.press(kE)
			d2.save()
			if got, _ := b2.stored(t, "api"); !reflect.DeepEqual(got.Conditions, sourceConditions) {
				t.Fatalf("edited conditions = %+v", got.Conditions)
			}
		})
	}
}

func TestClientWritesConditions(t *testing.T) {
	c, s, _ := realServer(t)
	ctx := context.Background()
	v := config.Check{Name: "api", URL: "https://example.com", Interval: time.Minute, Timeout: 5 * time.Second, ExpectedStatus: 200, Conditions: sourceConditions}
	if err := c.AddCheck(ctx, v); err != nil {
		t.Fatal(err)
	}
	read := func() []config.Condition {
		t.Helper()
		checks, err := s.Checks(ctx)
		if err != nil || len(checks) != 1 {
			t.Fatalf("Checks() = %+v, %v", checks, err)
		}
		return checks[0].Conditions
	}
	if got := read(); !reflect.DeepEqual(got, sourceConditions) {
		t.Fatalf("added conditions = %+v", got)
	}
	v.Conditions = sourceConditions[2:]
	if err := c.UpdateCheck(ctx, v); err != nil {
		t.Fatal(err)
	}
	if got := read(); !reflect.DeepEqual(got, sourceConditions[2:]) {
		t.Fatalf("updated conditions = %+v", got)
	}
	v.Conditions = nil
	if err := c.UpdateCheck(ctx, v); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != nil {
		t.Fatalf("cleared conditions = %+v", got)
	}
}
