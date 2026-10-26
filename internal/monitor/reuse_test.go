package monitor

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

// Gate discovery, not storage: mutations and result insertion still use the
// public SQLite APIs. Each permit releases exactly one discovery snapshot.
type gatedDiscovery struct {
	*store.Store
	initial bool
	poll    chan struct{}
}

func (s *gatedDiscovery) Checks(ctx context.Context) ([]config.Check, error) {
	if !s.initial {
		s.initial = true
	} else {
		select {
		case <-s.poll:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.Store.Checks(ctx)
}

// Schema v1 cannot distinguish deletion/recreation with an IDENTICAL
// definition during one in-flight check: that measurement may be retained.
// A different definition must never inherit the old in-flight measurement.
func TestRunRejectsObsoleteInflightAndRestartsFinishedLoop(t *testing.T) {
	for _, mutation := range []string{"different recreate", "edit", "identical recreate after rejection"} {
		t.Run(mutation, func(t *testing.T) {
			s := openStore(t)
			bg := context.Background()
			original := definition("api")
			if err := s.AddCheck(bg, original); err != nil {
				t.Fatal(err)
			}
			gated := &gatedDiscovery{Store: s, poll: make(chan struct{})}
			r := &changingRunner{blockURL: original.URL, release: make(chan struct{})}
			ctx, cancel := context.WithCancel(bg)
			done := make(chan error, 1)
			go func() {
				done <- Run(ctx, Options{Store: gated, Runner: r, DiscoveryInterval: time.Millisecond,
					HeartbeatInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			}()
			released := false
			unblock := func() {
				if !released {
					close(r.release)
					released = true
				}
			}
			t.Cleanup(func() {
				cancel()
				unblock()
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(5 * time.Second):
					t.Error("Run failed to join")
				}
			})
			waitFor(t, "in-flight original", func() bool { calls, _ := r.observed("api"); return len(calls) == 1 })
			changed := original
			if mutation == "edit" {
				changed.URL += "/edited"
				if err := s.UpdateCheck(bg, changed); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := s.DeleteCheck(bg, original.Name); err != nil {
					t.Fatal(err)
				}
				if mutation == "different recreate" {
					changed.URL += "/replacement"
					if err := s.AddCheck(bg, changed); err != nil {
						t.Fatal(err)
					}
				}
			}
			unblock()
			waitFor(t, "obsolete result rejection and loop exit before discovery", func() bool {
				calls, _ := r.observed("api")
				return calls[0].ctx.Err() != nil
			})
			if rs, err := s.Recent(bg, original.Name, 100); err != nil || len(rs) != 0 {
				t.Fatalf("obsolete history = %v, %v", rs, err)
			}
			if outages, err := s.Outages(bg, original.Name, 100); err != nil || len(outages) != 0 {
				t.Fatalf("obsolete outages = %v, %v", outages, err)
			}
			if mutation == "identical recreate after rejection" {
				if err := s.AddCheck(bg, original); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case gated.poll <- struct{}{}:
			case <-time.After(5 * time.Second):
				t.Fatal("discovery not polling")
			}
			waitFor(t, "fresh result after one discovery poll", func() bool {
				_, ok, _ := s.Latest(bg, original.Name)
				return ok
			})
			calls, starts := r.observed("api")
			if len(calls) != 2 || starts != 2 || calls[1].def != changed {
				t.Fatalf("not a fresh loop: %+v, starts %d", calls, starts)
			}
			rs, err := s.Recent(bg, original.Name, 100)
			if err != nil || len(rs) != 1 || rs[0].Status != check.StatusUp {
				t.Fatalf("fresh history = %v, %v", rs, err)
			}
		})
	}
}
