package tui

import (
	"bytes"
	"context"
	"testing"

	"github.com/giulianotesta7/up/internal/config"
)

func TestLoadAlertConfiguration(t *testing.T) {
	s, _ := formStore(t)
	ctx := context.Background()
	server := config.SMTPServer{Name: "gmail", Host: "smtp.example.com", Port: 587, TLS: config.TLSStartTLS, Sender: "up@example.com"}
	if err := s.AddSMTPServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	channel := config.Channel{Name: "ops", Kind: config.KindEmail, SMTPServer: "gmail", Recipients: []string{"ops@example.com"}}
	if err := s.AddChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	key := []byte("public-key")
	if err := s.SetPublicKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	snap, err := Load(ctx, s, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.SMTPServers) != 1 || snap.SMTPServers[0].Name != "gmail" || len(snap.Channels) != 1 || snap.Channels[0].Name != "ops" || !snap.HasKey || !bytes.Equal(snap.PublicKey, key) {
		t.Fatalf("snapshot: %+v", snap)
	}
}

func TestLoadWithoutAlertConfiguration(t *testing.T) {
	s, _ := formStore(t)
	snap, err := Load(context.Background(), s, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.SMTPServers) != 0 || len(snap.Channels) != 0 || snap.HasKey || len(snap.PublicKey) != 0 {
		t.Fatalf("snapshot: %+v", snap)
	}
}
