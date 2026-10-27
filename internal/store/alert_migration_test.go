package store

import (
	"context"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

func TestMigratedCheckChannel(t *testing.T) {
	ctx := context.Background()
	s, err := Open(v1Database(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checks, err := s.Checks(ctx)
	if err != nil || len(checks) != 2 || checks[0].Channel != "" {
		t.Fatal(checks, err)
	}
	server := config.SMTPServer{Name: "smtp", Host: "example.com", Port: 25, TLS: config.TLSStartTLS, Sender: "a@example.com"}
	if err := s.AddSMTPServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(ctx, config.Channel{Name: "ops", Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"b@example.com"}}); err != nil {
		t.Fatal(err)
	}
	checks[0].Channel = "ops"
	if err := s.UpdateCheck(ctx, checks[0]); err != nil {
		t.Fatal(err)
	}
	got, err := s.Checks(ctx)
	if err != nil || got[0].Channel != "ops" {
		t.Fatal(got, err)
	}
	if err := s.SetChecks(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, err = s.Checks(ctx)
	if err != nil || got[0].Channel != "ops" {
		t.Fatal(got, err)
	}
}

func TestCheckAlertThresholdsPersist(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	c := config.Check{Name: "api", URL: "https://example.com", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200, AlertAfter: 3, RecoverAfter: 2}
	if err := s.AddCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.Checks(ctx)
	if err != nil || len(got) != 1 || got[0] != c {
		t.Fatalf("Checks() = %+v, %v, want %+v", got, err, c)
	}
	c.AlertAfter, c.RecoverAfter = 5, 1
	if err := s.UpdateCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got, err = s.Checks(ctx); err != nil || got[0] != c {
		t.Fatalf("after update = %+v, %v, want %+v", got, err, c)
	}
	c.AlertAfter = 11
	if err := s.UpdateCheck(ctx, c); err == nil {
		t.Fatal("UpdateCheck accepted alert after 11")
	}
	if got, _ = s.Checks(ctx); got[0].AlertAfter != 5 {
		t.Fatalf("rejected update changed stored data: %+v", got[0])
	}
}
