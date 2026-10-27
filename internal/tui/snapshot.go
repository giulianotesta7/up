// Package tui shows the results stored by `up serve` in the terminal.
package tui

import (
	"context"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/monitor"
	"github.com/giulianotesta7/up/internal/store"
)

// StaleAfter is how old the daemon heartbeat may get before the TUI warns
// that `up serve` stopped writing.
const StaleAfter = 3 * monitor.DefaultHeartbeatInterval

const (
	recentLimit = 28
	outageLimit = 5
)

// Service is one configured check with its stored history.
type Service struct {
	Check     config.Check
	Latest    check.Result
	HasResult bool
	// DownSince is the start of the ongoing outage; zero when not down.
	DownSince time.Time
	// Recent and Outages are newest first.
	Recent  []check.Result
	Outages []store.Outage
}

// Label is the status shown for the service.
func (s Service) Label() string {
	switch {
	case !s.HasResult:
		return "PENDING"
	case s.Latest.Status == check.StatusUp:
		return "UP"
	default:
		return "DOWN"
	}
}

// Snapshot is everything the TUI shows, read at one moment.
type Snapshot struct {
	Services    []Service
	SMTPServers []config.SMTPServer
	Channels    []config.Channel
	PublicKey   []byte
	HasKey      bool
	LastBeat    time.Time
	HasBeat     bool
	// Uptime is the mean percentage over monitors with observed time.
	Uptime    float64
	HasUptime bool
	Incidents int
}

// Stale reports whether the daemon stopped writing heartbeats. Service
// statuses stay as last stored; staleness never turns a service down.
func (s Snapshot) Stale(now time.Time) bool {
	return !s.HasBeat || now.Sub(s.LastBeat) > StaleAfter
}

// Store is the database boundary used by the TUI.
type Store interface {
	Checks(context.Context) ([]config.Check, error)
	Recent(context.Context, string, int) ([]check.Result, error)
	Outages(context.Context, string, int) ([]store.Outage, error)
	LastBeat(context.Context) (time.Time, bool, error)
	UptimeWindow(context.Context, string, time.Time, time.Time) (time.Duration, time.Duration, int, error)
	AddCheck(context.Context, config.Check) error
	UpdateCheck(context.Context, config.Check) error
	DeleteCheck(context.Context, string) error
	SMTPServers(context.Context) ([]config.SMTPServer, error)
	AddSMTPServer(context.Context, config.SMTPServer) error
	UpdateSMTPServer(context.Context, config.SMTPServer) error
	DeleteSMTPServer(context.Context, string) error
	Channels(context.Context) ([]config.Channel, error)
	AddChannel(context.Context, config.Channel) error
	UpdateChannel(context.Context, config.Channel) error
	DeleteChannel(context.Context, string) error
	PublicKey(context.Context) ([]byte, bool, error)
}

var _ Store = (*store.Store)(nil)

// Load reads a snapshot from the store.
func Load(ctx context.Context, s Store, clock ...time.Time) (Snapshot, error) {
	var snap Snapshot
	now := time.Now()
	if len(clock) > 0 {
		now = clock[0]
	}
	var err error
	if snap.SMTPServers, err = s.SMTPServers(ctx); err != nil {
		return Snapshot{}, err
	}
	if snap.Channels, err = s.Channels(ctx); err != nil {
		return Snapshot{}, err
	}
	if snap.PublicKey, snap.HasKey, err = s.PublicKey(ctx); err != nil {
		return Snapshot{}, err
	}
	if snap.LastBeat, snap.HasBeat, err = s.LastBeat(ctx); err != nil {
		return Snapshot{}, err
	}
	until := now
	if snap.HasBeat && snap.Stale(now) {
		until = snap.LastBeat
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var observedMonitors int
	checks, err := s.Checks(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	for _, c := range checks {
		svc := Service{Check: c}
		if svc.Recent, err = s.Recent(ctx, c.Name, recentLimit); err != nil {
			return Snapshot{}, err
		}
		if len(svc.Recent) > 0 {
			svc.Latest, svc.HasResult = svc.Recent[0], true
		}
		if svc.Outages, err = s.Outages(ctx, c.Name, outageLimit); err != nil {
			return Snapshot{}, err
		}
		if len(svc.Outages) > 0 && svc.Outages[0].Ongoing {
			svc.DownSince = svc.Outages[0].Start
		}
		up, observed, incidents, err := s.UptimeWindow(ctx, c.Name, start, until)
		if err != nil {
			return Snapshot{}, err
		}
		if snap.HasBeat && observed > 0 {
			snap.Uptime += float64(up) / float64(observed) * 100
			observedMonitors++
		}
		snap.Incidents += incidents
		snap.Services = append(snap.Services, svc)
	}
	if observedMonitors > 0 {
		snap.Uptime /= float64(observedMonitors)
		snap.HasUptime = true
	}
	return snap, nil
}
