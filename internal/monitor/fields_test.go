package monitor

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
)

func sealedValue(b byte) secret.Sealed { return secret.Sealed{Key: []byte{7}, Data: []byte{b}} }

// holdingRunner blocks every run of the original definition until released,
// so an edit lands while its measurement is in flight, and runs any other
// definition at once.
type holdingRunner struct {
	original config.Check
	release  chan struct{}
	mu       sync.Mutex
	ran      []config.Check
	loops    map[context.Context]bool
}

func (r *holdingRunner) Run(ctx context.Context, c config.Check) check.Result {
	r.mu.Lock()
	r.ran = append(r.ran, c)
	r.loops[ctx] = true
	r.mu.Unlock()
	if c.SameDefinition(r.original) {
		<-r.release
	}
	return check.Result{CheckName: c.Name, At: time.Now(), Status: check.StatusUp, StatusCode: c.ExpectedStatus}
}

func (r *holdingRunner) snapshot() ([]config.Check, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]config.Check(nil), r.ran...), len(r.loops)
}

// Editing any executable field restarts the check; the old loop's late result
// is never stored (the store-level guard is tested in package store).
func TestEditingEachNewFieldRestartsTheCheck(t *testing.T) {
	edits := map[string]func(*config.Check){
		"method":       func(c *config.Check) { c.Method = "PUT" },
		"header name":  func(c *config.Check) { c.Headers[0].Name = "X-Other" },
		"header value": func(c *config.Check) { c.Headers[0].Value, c.Headers[0].Edit = sealedValue(8), config.EditReplace },
		"header added": func(c *config.Check) { c.Headers = append(c.Headers, config.Header{Name: "Z"}) },
		"body":         func(c *config.Check) { c.Body, c.BodyEdit = sealedValue(9), config.EditReplace },
		"expect text":  func(c *config.Check) { c.ExpectText = "changed" },
		"kind tcp": func(c *config.Check) {
			*c = config.Check{Name: c.Name, Kind: config.CheckTCP, Host: "db.example.com", Port: 5432, Interval: c.Interval, Timeout: c.Timeout}
		},
		"host and port edited": func(c *config.Check) {
			*c = config.Check{Name: c.Name, Kind: config.CheckTCP, Host: "other.example.com", Port: 22, Interval: c.Interval, Timeout: c.Timeout}
		},
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			s := openStore(t)
			bg := context.Background()
			original := definition("api")
			original.Method = "POST"
			original.Headers = []config.Header{{Name: "X-Token", Value: sealedValue(1), Edit: config.EditReplace}}
			original.Body, original.BodyEdit = sealedValue(2), config.EditReplace
			original.ExpectText = "ok"
			if err := s.AddCheck(bg, original); err != nil {
				t.Fatal(err)
			}
			stored, _ := s.Checks(bg)
			r := &holdingRunner{original: stored[0], release: make(chan struct{}), loops: map[context.Context]bool{}}
			var release sync.Once
			unblock := func() { release.Do(func() { close(r.release) }) }
			t.Cleanup(unblock)
			var logs safeLog
			runChanging(t, s, r, &logs)
			waitFor(t, "in-flight measurement", func() bool { ran, _ := r.snapshot(); return len(ran) == 1 })

			next := stored[0]
			next.Headers = append([]config.Header(nil), next.Headers...)
			next.Headers[0].Edit = config.EditKeep
			edit(&next)
			if err := s.UpdateCheck(bg, next); err != nil {
				t.Fatal(err)
			}
			// Discovery cancels the old loop and joins it before starting the
			// new one, so the held runner has to return first.
			waitFor(t, "old loop canceled", func() bool {
				r.mu.Lock()
				defer r.mu.Unlock()
				for ctx := range r.loops {
					if ctx.Err() != nil {
						return true
					}
				}
				return false
			})
			unblock()
			waitFor(t, "restart with the new definition", func() bool { _, loops := r.snapshot(); return loops == 2 })
			waitFor(t, "new definition result", func() bool { rs, _ := s.Recent(bg, "api", 10); return len(rs) == 1 })
			time.Sleep(50 * time.Millisecond)
			if rs, err := s.Recent(bg, "api", 10); err != nil || len(rs) != 1 {
				t.Fatalf("results = %d, %v; the stale in-flight result must be dropped", len(rs), err)
			}
			ran, _ := r.snapshot()
			current, _ := s.Checks(bg)
			for _, c := range ran[1:] {
				if !c.SameDefinition(current[0]) {
					t.Fatalf("old definition still running: %+v", c)
				}
			}
		})
	}
}

// Shutdown that arrives before the first read of the definitions is a clean
// stop, not a failure (the serve command used to exit 1 in that window).
func TestRunStopsCleanlyWhenCanceledBeforeTheFirstRead(t *testing.T) {
	s := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var logs safeLog
	if err := Run(ctx, Options{Store: s, Runner: &holdingRunner{}, HeartbeatInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(&logs, nil))}); err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}
}
