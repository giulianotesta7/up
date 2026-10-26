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
	recentLimit = 20
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
	Services []Service
	LastBeat time.Time
	HasBeat  bool
}

// Stale reports whether the daemon stopped writing heartbeats. Service
// statuses stay as last stored; staleness never turns a service down.
func (s Snapshot) Stale(now time.Time) bool {
	return !s.HasBeat || now.Sub(s.LastBeat) > StaleAfter
}

// Load reads a snapshot from the store.
func Load(ctx context.Context, s *store.Store) (Snapshot, error) {
	var snap Snapshot
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
		snap.Services = append(snap.Services, svc)
	}
	if snap.LastBeat, snap.HasBeat, err = s.LastBeat(ctx); err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}
