package store_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
)

func smtp(name string) config.SMTPServer {
	return config.SMTPServer{Name: name, Host: "smtp.example.com", Port: 587, TLS: config.TLSStartTLS, Username: "u", Password: secret.Sealed{Data: []byte{1, 2}, Key: []byte{3, 4}}, Sender: "a@example.com"}
}
func channel(name string) config.Channel {
	return config.Channel{Name: name, Kind: config.KindEmail, SMTPServer: "s", Recipients: []string{"b@example.com", "a@example.com"}}
}
func unchanged(t *testing.T, path string, f func() error, want string) {
	t.Helper()
	before := diskState(t, path)
	err := f()
	if err == nil || err.Error() != want {
		t.Fatalf("error %v want %s", err, want)
	}
	if !reflect.DeepEqual(before, diskState(t, path)) {
		t.Fatal("rejection changed bytes")
	}
}
func TestAlertStore(t *testing.T) {
	s, path := seeded(t)
	if _, ok, err := s.SMTPServer(ctx, "missing"); ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, ok, err := s.Channel(ctx, "missing"); ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, ok, err := s.PublicKey(ctx); ok || err != nil {
		t.Fatal(ok, err)
	}
	for _, v := range [][]byte{{1}, {2, 3}} {
		if err := s.SetPublicKey(ctx, v); err != nil {
			t.Fatal(err)
		}
		got, ok, err := s.PublicKey(ctx)
		if err != nil || !ok || !reflect.DeepEqual(got, v) {
			t.Fatal(got, ok, err)
		}
	}
	for _, n := range []string{"z", "s"} {
		if err := s.AddSMTPServer(ctx, smtp(n)); err != nil {
			t.Fatal(err)
		}
	}
	servers, err := s.SMTPServers(ctx)
	if err != nil || !reflect.DeepEqual(servers, []config.SMTPServer{smtp("s"), smtp("z")}) {
		t.Fatal(servers, err)
	}
	got, ok, err := s.SMTPServer(ctx, "s")
	if err != nil || !ok || !reflect.DeepEqual(got, smtp("s")) {
		t.Fatal(got, ok, err)
	}
	unchanged(t, path, func() error { return s.AddSMTPServer(ctx, smtp("s")) }, `store: add SMTP server: name "s" already exists`)
	unchanged(t, path, func() error { return s.UpdateSMTPServer(ctx, smtp("missing")) }, `store: update SMTP server: name "missing" not found`)
	unchanged(t, path, func() error { return s.DeleteSMTPServer(ctx, "missing") }, `store: delete SMTP server: name "missing" not found`)
	v := smtp("s")
	v.Password = secret.Sealed{}
	if err := s.UpdateSMTPServer(ctx, v); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.SMTPServer(ctx, "s")
	if !reflect.DeepEqual(got.Password, smtp("s").Password) {
		t.Fatal(got.Password)
	}
	v.Password = secret.Sealed{Data: []byte{8}, Key: []byte{9}}
	if err := s.UpdateSMTPServer(ctx, v); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.SMTPServer(ctx, "s")
	if !reflect.DeepEqual(got.Password, v.Password) {
		t.Fatal(got.Password)
	}
	v.Username = ""
	if err := s.UpdateSMTPServer(ctx, v); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.SMTPServer(ctx, "s")
	if !got.Password.Empty() || len(got.Password.Key) != 0 {
		t.Fatal(got.Password)
	}
	for _, n := range []string{"z", "a"} {
		if err := s.AddChannel(ctx, channel(n)); err != nil {
			t.Fatal(err)
		}
	}
	channels, err := s.Channels(ctx)
	if err != nil || !reflect.DeepEqual(channels, []config.Channel{channel("a"), channel("z")}) {
		t.Fatal(channels, err)
	}
	c, ok, err := s.Channel(ctx, "a")
	if err != nil || !ok || !reflect.DeepEqual(c, channel("a")) {
		t.Fatal(c, ok, err)
	}
	unchanged(t, path, func() error { return s.AddChannel(ctx, channel("a")) }, `store: add channel: name "a" already exists`)
	unchanged(t, path, func() error { return s.UpdateChannel(ctx, channel("missing")) }, `store: update channel: name "missing" not found`)
	unchanged(t, path, func() error { return s.DeleteChannel(ctx, "missing") }, `store: delete channel: name "missing" not found`)
	c.SMTPServer = "missing"
	unchanged(t, path, func() error { return s.UpdateChannel(ctx, c) }, `store: update channel: SMTP server "missing" does not exist`)
	c.Name = "new"
	unchanged(t, path, func() error { return s.AddChannel(ctx, c) }, `store: add channel: SMTP server "missing" does not exist`)
	c = channel("a")
	c.Recipients = []string{"c@example.com"}
	if err := s.UpdateChannel(ctx, c); err != nil {
		t.Fatal(err)
	}
	gotC, _, _ := s.Channel(ctx, "a")
	if !reflect.DeepEqual(c, gotC) {
		t.Fatal(gotC)
	}
	before := diskState(t, path)
	err = s.DeleteSMTPServer(ctx, "s")
	var in *store.InUseError
	if !errors.As(err, &in) || in.Reason() != "used by channels a, z" || err.Error() != `store: delete SMTP server: "s" is used by channels a, z` || !reflect.DeepEqual(before, diskState(t, path)) {
		t.Fatal(err, in)
	}
	if err := s.DeleteChannel(ctx, "z"); err != nil {
		t.Fatal(err)
	}
	err = s.DeleteSMTPServer(ctx, "s")
	if !errors.As(err, &in) || in.Reason() != "used by channel a" {
		t.Fatal(err)
	}
	bad := definition("new")
	bad.Channel = "missing"
	unchanged(t, path, func() error { return s.AddCheck(ctx, bad) }, `store: add check: channel "missing" does not exist`)
	bad.Name = "api"
	unchanged(t, path, func() error { return s.UpdateCheck(ctx, bad) }, `store: update check: channel "missing" does not exist`)
	for _, n := range []string{"api", "web"} {
		v := definition(n)
		v.Channel = "a"
		if err := s.UpdateCheck(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	vcheck := definition("new")
	vcheck.Channel = "a"
	if err := s.AddCheck(ctx, vcheck); err != nil {
		t.Fatal(err)
	}
	checks, err := s.Checks(ctx)
	if err != nil || len(checks) != 3 || checks[0].Channel != "a" || checks[2] != vcheck {
		t.Fatal(checks, err)
	}
	r := check.Result{CheckName: "api", At: at, Status: check.StatusUp}
	stored, err := s.RecordDefined(ctx, definition("api"), r)
	if err != nil || !stored {
		t.Fatal(stored, err)
	}
	before = diskState(t, path)
	err = s.DeleteChannel(ctx, "a")
	if !errors.As(err, &in) || in.Reason() != "used by monitors api, new, web" || err.Error() != `store: delete channel: "a" is used by monitors api, new, web` || !reflect.DeepEqual(before, diskState(t, path)) {
		t.Fatal(err, in)
	}
	for _, n := range []string{"api", "web"} {
		if err := s.DeleteCheck(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteCheck(ctx, "new"); err != nil {
		t.Fatal(err)
	}
	invalid := smtp("bad")
	invalid.TLS, invalid.Username = config.TLSNone, "user"
	unchanged(t, path, func() error { return s.AddSMTPServer(ctx, invalid) }, `store: add SMTP server: TLS "none" cannot be used with a username (the password would travel in plain text)`)
	noRecipients := channel("bad")
	noRecipients.Recipients = nil
	unchanged(t, path, func() error { return s.AddChannel(ctx, noRecipients) }, `store: add channel: at least one recipient is required`)
	single := definition("new")
	single.Channel = "a"
	if err := s.AddCheck(ctx, single); err != nil {
		t.Fatal(err)
	}
	before = diskState(t, path)
	err = s.DeleteChannel(ctx, "a")
	if !errors.As(err, &in) || in.Reason() != "used by monitor new" || !reflect.DeepEqual(before, diskState(t, path)) {
		t.Fatal(err, in)
	}
	if err := s.DeleteCheck(ctx, "new"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteChannel(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSMTPServer(ctx, "s"); err != nil {
		t.Fatal(err)
	}
}
