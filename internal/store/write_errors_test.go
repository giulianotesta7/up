package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
)

func TestAlertWriteErrorKinds(t *testing.T) {
	s, err := Open(t.TempDir() + "/alert.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, tc := range []struct {
		run  func() error
		kind error
		text string
	}{
		{func() error { return s.AddSMTPServer(ctx, config.SMTPServer{}) }, ErrValidation, ""},
		{func() error { return s.DeleteSMTPServer(ctx, "missing") }, ErrNotFound, `store: delete SMTP server: name "missing" not found`},
		{func() error { return s.DeleteChannel(ctx, "missing") }, ErrNotFound, `store: delete channel: name "missing" not found`},
	} {
		err := tc.run()
		if !errors.Is(err, tc.kind) || (tc.text != "" && err.Error() != tc.text) {
			t.Errorf("got %v, want kind %v and text %q", err, tc.kind, tc.text)
		}
	}
}

func TestWriteErrorKinds(t *testing.T) {
	s, err := Open(t.TempDir() + "/checks.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	c := config.Check{Name: "site", URL: "https://example.com", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200}
	cases := []struct {
		run  func() error
		kind error
		want string
	}{
		{func() error { return s.AddCheck(ctx, config.Check{}) }, ErrValidation, "store: add check: name is required\nurl is required\ninterval must be greater than 0\ntimeout must be greater than 0\nexpected status 0 must be between 100 and 599"},
		{func() error { return s.AddCheck(ctx, c) }, nil, ""},
		{func() error { return s.AddCheck(ctx, c) }, ErrExists, `store: add check: name "site" already exists`},
		{func() error { x := c; x.Channel = "missing"; return s.UpdateCheck(ctx, x) }, ErrValidation, `store: update check: channel "missing" does not exist`},
		{func() error { x := c; x.Name = "missing"; return s.UpdateCheck(ctx, x) }, ErrNotFound, `store: update check: name "missing" not found`},
		{func() error { return s.DeleteCheck(ctx, "missing") }, ErrNotFound, `store: delete check: name "missing" not found`},
		{func() error { return s.SetMute(ctx, "missing", time.Now(), false) }, ErrNotFound, `store: set mute: name "missing" not found`},
	}
	for _, tc := range cases {
		err := tc.run()
		if tc.kind == nil {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if !errors.Is(err, tc.kind) || err.Error() != tc.want {
			t.Errorf("got %v, want %s", err, tc.want)
		}
	}
}
