package monitor

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
)

// apiOnlyRunner feeds the rig's results to "api" and keeps every other
// monitor waiting, so a second tagged monitor never takes api's results.
type apiOnlyRunner struct{ *clockRunner }

func (r apiOnlyRunner) Run(ctx context.Context, c config.Check) check.Result {
	if c.Name != "api" {
		<-ctx.Done()
		return check.Result{Status: check.StatusCanceled}
	}
	return r.clockRunner.Run(ctx, c)
}

// startTagged starts the rig's daemon with apiOnlyRunner.
func (r *maintenanceRig) startTagged() {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan error, 1)
	before := r.runner.entered.Load()
	go func() {
		r.done <- Run(ctx, Options{Store: r.s, Runner: apiOnlyRunner{r.runner}, Alerter: r.alert, Now: r.clock.Now, HeartbeatInterval: time.Hour, DiscoveryInterval: 20 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	}()
	r.t.Cleanup(r.stop)
	waitFor(r.t, "the loop to start", func() bool { return r.runner.entered.Load() > before })
}

// tagAPI gives the rig's api monitor tags without restarting its loop.
func (r *maintenanceRig) tagAPI(tags ...string) {
	r.t.Helper()
	c, err := r.s.Checks(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	for _, x := range c {
		if x.Name == "api" {
			x.Tags = tags
			if err := r.s.UpdateCheck(context.Background(), x); err != nil {
				r.t.Fatal(err)
			}
		}
	}
}

func at(h, m int) time.Time { return time.Date(2030, 1, 7, h, m, 0, 0, time.UTC) }

// A tag's window holds the alerts of a monitor tagged after the window was
// created, exactly like the monitor's own window.
func TestTagWindowHoldsAMonitorTaggedLater(t *testing.T) {
	r := newRig(t)
	bg := context.Background()
	if err := r.s.AddCheck(bg, config.Check{Name: "box", URL: "http://example.com", Interval: time.Hour, Timeout: time.Second, ExpectedStatus: 200, Tags: []string{"nas"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.AddMaintenanceWindow(bg, config.MaintenanceWindow{Tag: "nas", Weekdays: []time.Weekday{0, 1, 2, 3, 4, 5, 6}, StartMinute: 10 * 60, Duration: 2 * time.Hour, Zone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	r.startTagged()
	r.feed(9, 30, up)
	r.tagAPI("nas")
	r.feed(10, 30, down)
	r.feed(11, 0, down)
	r.want()
	r.feed(12, 30, down)
	r.want(down)
}

// When a monitor and its tag are both muted, alerts are held until the later
// end, whichever of the two it is.
func TestOwnAndTagMuteHoldUntilTheLaterEnd(t *testing.T) {
	for _, tt := range []struct {
		name          string
		own, tagUntil time.Time
	}{
		{"tag ends later", at(11, 0), at(13, 0)},
		{"own ends later", at(13, 0), at(11, 0)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.tagAPI("nas")
			bg := context.Background()
			if err := r.s.SetMute(bg, "api", tt.own, false); err != nil {
				t.Fatal(err)
			}
			if err := r.s.SetTagMute(bg, "nas", tt.tagUntil); err != nil {
				t.Fatal(err)
			}
			r.startTagged()
			r.feed(10, 30, down)
			r.feed(11, 30, down) // one mute over, the other still on
			r.want()
			r.feed(13, 30, down)
			r.want(down)
			r.feed(13, 40, down)
			r.want(down)
			if m, ok, _ := r.s.Mute(bg, "api"); ok {
				t.Fatalf("mute not cleared after reconciling: %+v", m)
			}
			if mutes, _ := r.s.TagMutes(bg); len(mutes) != 0 {
				t.Fatalf("ended tag mute not cleared: %+v", mutes)
			}
		})
	}
}

// Unmuting a tag ends the hold of a monitor muted only through it.
func TestUnmutingATagSendsWhatIsOwed(t *testing.T) {
	r := newRig(t)
	r.tagAPI("nas")
	r.slowChecks()
	bg := context.Background()
	if err := r.s.SetTagMute(bg, "nas", at(13, 0)); err != nil {
		t.Fatal(err)
	}
	r.startTagged()
	r.feed(10, 30, down)
	r.want()
	if err := r.s.UnmuteTag(bg, "nas"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the alert after unmuting the tag", func() bool { return len(r.alerts()) == 1 })
}
