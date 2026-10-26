package monitor

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
)

type cancelRunner struct {
	started chan struct{}
	exited  chan struct{}
}

func (r *cancelRunner) Run(ctx context.Context, c config.Check) check.Result {
	close(r.started)
	<-ctx.Done()
	close(r.exited)
	return check.Result{CheckName: c.Name, Status: check.StatusCanceled}
}

func TestRunJoinsCanceledCheckAndStopsHeartbeat(t *testing.T) {
	s := openStore(t)
	bg := context.Background()
	if err := s.AddCheck(bg, definition("slow")); err != nil {
		t.Fatal(err)
	}
	runner := &cancelRunner{started: make(chan struct{}), exited: make(chan struct{})}
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Store: s, Runner: runner, HeartbeatInterval: time.Millisecond,
			DiscoveryInterval: time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("check never started")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not join canceled check")
	}
	select {
	case <-runner.exited:
	default:
		t.Fatal("Run returned before runner exited")
	}
	before, ok, err := s.LastBeat(bg)
	if err != nil || !ok {
		t.Fatalf("heartbeat = %v, %v, %v", before, ok, err)
	}
	time.Sleep(20 * time.Millisecond)
	after, _, err := s.LastBeat(bg)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("heartbeat changed after Run returned: %v -> %v (%v)", before, after, err)
	}
	results, err := s.Recent(bg, "slow", 100)
	if err != nil || len(results) != 0 {
		t.Fatalf("canceled result stored: %v (%v)", results, err)
	}
}
