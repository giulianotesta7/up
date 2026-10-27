package store

import (
	"context"
	"testing"

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
