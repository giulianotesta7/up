package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

func TestCertLifecycle(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	for _, name := range []string{"web", "api"} {
		if err := s.AddCheck(ctx, config.Check{Name: name, URL: "https://example.com", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := s.Cert(ctx, "api"); err != nil || ok {
		t.Fatalf("missing cert: %v %v", ok, err)
	}
	at := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	a := config.Cert{Check: "api", Host: "example.com", Issuer: "issuer", Serial: "a", NotAfter: at.Add(24 * time.Hour), Observed: at, Notified: 7}
	b := a
	b.Check = "web"
	if err := s.SaveCert(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCert(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.Notified = 3
	if err := s.SaveCert(ctx, a); err != nil {
		t.Fatal(err)
	}
	before, err := s.Certs(ctx)
	if err != nil || !reflect.DeepEqual(before, []config.Cert{a, b}) {
		t.Fatalf("certs %v %v", before, err)
	}
	bad := a
	bad.Check = "missing"
	if err := s.SaveCert(ctx, bad); err == nil {
		t.Fatal("unknown check accepted")
	}
	after, err := s.Certs(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected write changed data: %v %v", after, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, ok, err := s.Cert(ctx, "api")
	if err != nil || !ok || !reflect.DeepEqual(got, a) {
		t.Fatalf("reopened cert %+v %v %v", got, ok, err)
	}
	if err := s.DeleteCheck(ctx, "api"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Cert(ctx, "api"); err != nil || ok {
		t.Fatalf("deleted cert %v %v", ok, err)
	}
}
