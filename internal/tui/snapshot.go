// Package tui shows the results stored by `up serve` in the terminal.
package tui

import (
	"context"
	"fmt"
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
	recentLimit = 60
	outageLimit = 5
)

// The monitor detail lists the incidents of the last IncidentPeriod, at most
// IncidentLimit, newest first. It matches them to at most
// store.MaxAttemptLimit delivery attempts of the monitor.
const (
	IncidentPeriod = 30 * day
	IncidentLimit  = 50
)

// Delivery attempts in a snapshot are bounded, newest first, so a snapshot
// does not grow with retention: AttemptLimit across all monitors and
// ServiceAttemptLimit for each monitor.
const (
	AttemptLimit        = 200
	ServiceAttemptLimit = 5
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
	// Streak is the alert state rebuilt from Recent with the monitor's
	// thresholds.
	Streak check.Streak
	// Mute is the stored mute, if any; it may already be over but not yet
	// reconciled by the daemon. Use Muted.
	Mute    config.Mute
	HasMute bool
	// Cert is the last certificate observed for an HTTPS monitor.
	Cert    config.Cert
	HasCert bool
	// Maintenance are the monitor's recurring windows and InMaintenance
	// their evaluation at the snapshot's time (see config.EvaluateMaintenance).
	// Both stay empty with a store that cannot read windows.
	Maintenance   []config.MaintenanceWindow
	InMaintenance config.Maintenance
	// KnownAlerting is whether the recipients were last told the monitor is
	// down: what the daemon recorded, or the streak when it recorded nothing
	// yet. A mute taken now starts from it.
	KnownAlerting bool
	// Attempts are the monitor's latest alert delivery attempts, newest
	// first, at most ServiceAttemptLimit.
	Attempts []store.Attempt
	// Incidents are the outages of the last IncidentPeriod (ongoing or ended
	// after its start), newest first, at most IncidentLimit, with what became
	// of their alerts.
	Incidents []Incident
	// PeriodUp and PeriodObserved are the time spent up and observed in the
	// last IncidentPeriod (up to the last heartbeat when the daemon stopped),
	// and PeriodIncidents the outages intersecting it (see
	// store.UptimeWindow).
	PeriodUp, PeriodObserved time.Duration
	PeriodIncidents          int
}

// Incident is an outage with the outcome of its alerts: the down alert and
// the recovery alert, through every channel.
type Incident struct {
	store.Outage
	// Alert is the worst outcome of those attempts (failed, then held, then
	// sent); empty when none was made. When the channels had different
	// outcomes, Channel names the channel with the worst one. Reason is the
	// reason of that outcome.
	Alert   store.Outcome
	Channel string
	Reason  string
}

func outcomeRank(o store.Outcome) int {
	switch o {
	case store.OutcomeFailed:
		return 3
	case store.OutcomeHeld:
		return 2
	case store.OutcomeSent:
		return 1
	}
	return 0
}

// matchAlerts gives each outage (newest first) the monitor.down and
// monitor.recovered attempts made from its start until the start of the next
// outage: its down alert and, after it ended, its recovery alert.
func matchAlerts(outages []store.Outage, attempts []store.Attempt) []Incident {
	var incidents []Incident
	for i, o := range outages {
		incident := Incident{Outage: o}
		worst := map[string]store.Attempt{}
		for _, a := range attempts {
			if a.Event != store.EventDown && a.Event != store.EventRecovered {
				continue
			}
			if a.At.Before(o.Start) || (i > 0 && !a.At.Before(outages[i-1].Start)) {
				continue
			}
			if outcomeRank(a.Outcome) > outcomeRank(worst[a.Channel].Outcome) {
				worst[a.Channel] = a
			}
		}
		outcomes := map[store.Outcome]bool{}
		for channel, a := range worst {
			outcomes[a.Outcome] = true
			rank, best := outcomeRank(a.Outcome), outcomeRank(incident.Alert)
			if rank > best || (rank == best && channel < incident.Channel) {
				incident.Alert, incident.Channel, incident.Reason = a.Outcome, channel, a.Reason
			}
		}
		if len(outcomes) < 2 {
			incident.Channel = ""
		}
		incidents = append(incidents, incident)
	}
	return incidents
}

// Muted reports whether the service's alerts are being held at now.
func (s Service) Muted(now time.Time) bool { return s.HasMute && s.Mute.Until.After(now) }

// Pending describes the email held by an active mute: what the recipients
// do not know yet. It is empty when nothing would be sent.
func (s Service) Pending() string {
	switch {
	case s.Streak.Alerting == s.Mute.WasAlerting:
		return ""
	case s.Streak.Alerting:
		return "down"
	default:
		return "recovery"
	}
}

// thresholdsDefault reports the one-failure, one-success rule that alerts on
// every status change.
func (s Service) thresholdsDefault() bool {
	return s.Check.FailuresToAlert() == 1 && s.Check.SuccessesToRecover() == 1
}

// Label is the status shown for the service. With alert thresholds above 1 a
// failing monitor reads FAILING n/N until the down alert fires, and a
// recovering one RECOVERING n/M until the recovery alert fires.
func (s Service) Label() string {
	switch {
	case !s.HasResult:
		return "PENDING"
	case s.thresholdsDefault():
		if s.Latest.Status == check.StatusUp {
			return "UP"
		}
		return "DOWN"
	case s.Latest.Status == check.StatusUp:
		if s.Streak.Alerting {
			return fmt.Sprintf("RECOVERING %d/%d", s.Streak.Successes, s.Check.SuccessesToRecover())
		}
		return "UP"
	case s.Streak.Alerting:
		return "DOWN"
	default:
		return fmt.Sprintf("FAILING %d/%d", s.Streak.Failures, s.Check.FailuresToAlert())
	}
}

// Snapshot is everything the TUI shows, read at one moment.
type Snapshot struct {
	Services    []Service
	SMTPServers []config.SMTPServer
	Channels    []config.Channel
	// ChannelTests is each channel's newest test request, by channel name.
	ChannelTests []config.ChannelTest
	// Attempts are the latest alert delivery attempts of all monitors, newest
	// first, at most AttemptLimit. LastAttempts is the newest attempt of each
	// channel that has one, ordered by channel.
	Attempts     []store.Attempt
	LastAttempts []store.Attempt
	PublicKey    []byte
	HasKey       bool
	LastBeat     time.Time
	HasBeat      bool
	// Uptime is the mean percentage over monitors with observed time.
	Uptime    float64
	HasUptime bool
	Incidents int
	// Defaults are the values new monitors and maintenance windows start
	// from. The zero value (no Zone) means config.BuiltinDefaults; use
	// Snapshot.NewItemDefaults.
	Defaults config.Defaults
}

// NewItemDefaults returns the defaults, or the built-in ones for a snapshot
// that carries none (for example from an older server).
func (s Snapshot) NewItemDefaults() config.Defaults {
	if s.Defaults.Zone == "" {
		return config.BuiltinDefaults()
	}
	return s.Defaults
}

// Stale reports whether the daemon stopped writing heartbeats. Service
// statuses stay as last stored; staleness never turns a service down.
func (s Snapshot) Stale(now time.Time) bool {
	return !s.HasBeat || now.Sub(s.LastBeat) > StaleAfter
}

// existingChannel returns name when a channel of that name exists, else none
// (""): a default channel that was deleted must not be offered.
func (s Snapshot) existingChannel(name string) string {
	for _, c := range s.Channels {
		if c.Name == name {
			return name
		}
	}
	return ""
}

// Store is the database boundary used by the TUI.
type Store interface {
	Checks(context.Context) ([]config.Check, error)
	Recent(context.Context, string, int) ([]check.Result, error)
	Outages(context.Context, string, int) ([]store.Outage, error)
	LastBeat(context.Context) (time.Time, bool, error)
	UptimeWindow(context.Context, string, time.Time, time.Time) (time.Duration, time.Duration, int, error)
	Mutes(context.Context) ([]config.Mute, error)
	Certs(context.Context) ([]config.Cert, error)
	SetMute(context.Context, string, time.Time, bool) error
	Unmute(context.Context, string, time.Time) error
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

// The interfaces below are optional capabilities of a Store, found with a
// type assertion so features can use them without widening Store. Both the
// local *store.Store and the remote client implement the write ones; only
// *store.Store implements the readers, because remote reads arrive in the
// snapshot.

// CheckCopier adds a monitor taking the header values and request body it
// keeps (config.EditKeep) from the stored monitor named source, so a copy
// carries sealed values without the TUI ever holding them in the clear. Both
// the local *store.Store and the remote client implement it.
type CheckCopier interface {
	AddCheckFrom(ctx context.Context, source string, c config.Check) error
}

var _ CheckCopier = (*store.Store)(nil)

// MaintenanceStore edits a monitor's recurring maintenance windows. IDs are
// assigned by the store and returned in the added window.
type MaintenanceStore interface {
	AddMaintenanceWindow(context.Context, config.MaintenanceWindow) (config.MaintenanceWindow, error)
	UpdateMaintenanceWindow(context.Context, config.MaintenanceWindow) error
	DeleteMaintenanceWindow(ctx context.Context, check string, id int64) error
}

// ChannelTester asks the daemon to send a test message through a channel. It
// only records the request (pending); the daemon sends it, and the result
// arrives in later snapshots as Snapshot.ChannelTests. The remote client
// ignores the time and uses the server's clock. A channel with an active test
// returns store.ErrExists.
type ChannelTester interface {
	EnqueueChannelTest(ctx context.Context, channel string, at time.Time) (config.ChannelTest, error)
}

// DefaultsStore saves the defaults. Both the local *store.Store and the
// remote client implement it; the values arrive back in Snapshot.Defaults.
type DefaultsStore interface {
	SetDefaults(context.Context, config.Defaults) error
}

// DefaultsReader lets Load fill Snapshot.Defaults.
type DefaultsReader interface {
	Defaults(context.Context) (config.Defaults, error)
}

// MaintenanceReader lets Load fill Service.Maintenance.
type MaintenanceReader interface {
	AllMaintenanceWindows(context.Context) ([]config.MaintenanceWindow, error)
}

// AlertStateReader lets Load fill Service.KnownAlerting.
type AlertStateReader interface {
	AlertStates(context.Context) (map[string]bool, error)
}

// ChannelTestReader lets Load fill Snapshot.ChannelTests.
type ChannelTestReader interface {
	LatestChannelTests(context.Context) ([]config.ChannelTest, error)
}

// AttemptReader lets Load fill the delivery attempts of the snapshot.
type AttemptReader interface {
	Attempts(context.Context, store.AttemptFilter) ([]store.Attempt, error)
	LastAttempts(context.Context) ([]store.Attempt, error)
}

var _ AttemptReader = (*store.Store)(nil)

// SnapshotLoader is an optional capability of a Store: it returns a whole
// snapshot in one call. The TUI uses it instead of many per-resource reads,
// so a remote store costs one request per refresh and never falls back to
// partial reads.
type SnapshotLoader interface {
	LoadSnapshot(ctx context.Context, now time.Time) (Snapshot, error)
}

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
	since := now.Add(-IncidentPeriod)
	mutes, err := s.Mutes(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	certs, err := s.Certs(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	var windows []config.MaintenanceWindow
	if r, ok := s.(MaintenanceReader); ok {
		if windows, err = r.AllMaintenanceWindows(ctx); err != nil {
			return Snapshot{}, err
		}
	}
	var states map[string]bool
	if r, ok := s.(AlertStateReader); ok {
		if states, err = r.AlertStates(ctx); err != nil {
			return Snapshot{}, err
		}
	}
	if r, ok := s.(ChannelTestReader); ok {
		if snap.ChannelTests, err = r.LatestChannelTests(ctx); err != nil {
			return Snapshot{}, err
		}
	}
	attempts, hasAttempts := s.(AttemptReader)
	if hasAttempts {
		if snap.Attempts, err = attempts.Attempts(ctx, store.AttemptFilter{Limit: AttemptLimit}); err != nil {
			return Snapshot{}, err
		}
		if snap.LastAttempts, err = attempts.LastAttempts(ctx); err != nil {
			return Snapshot{}, err
		}
	}
	if r, ok := s.(DefaultsReader); ok {
		if snap.Defaults, err = r.Defaults(ctx); err != nil {
			return Snapshot{}, err
		}
	}
	var observedMonitors int
	checks, err := s.Checks(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	for _, c := range checks {
		svc := Service{Check: c}
		for _, cert := range certs {
			if cert.Check == c.Name && !c.TLSOff {
				svc.Cert, svc.HasCert = cert, true
			}
		}
		for _, mute := range mutes {
			if mute.Check == c.Name {
				svc.Mute, svc.HasMute = mute, true
			}
		}
		for _, w := range windows {
			if w.Check == c.Name {
				svc.Maintenance = append(svc.Maintenance, w)
			}
		}
		svc.InMaintenance = config.EvaluateMaintenance(svc.Maintenance, now)
		if svc.Recent, err = s.Recent(ctx, c.Name, recentLimit); err != nil {
			return Snapshot{}, err
		}
		if len(svc.Recent) > 0 {
			svc.Latest, svc.HasResult = svc.Recent[0], true
		}
		svc.Streak = check.Replay(svc.Recent, c.FailuresToAlert(), c.SuccessesToRecover())
		svc.KnownAlerting = svc.Streak.Alerting && c.Channel != ""
		if known, ok := states[c.Name]; ok {
			svc.KnownAlerting = known
		}
		if hasAttempts {
			if svc.Attempts, err = attempts.Attempts(ctx, store.AttemptFilter{Monitor: c.Name, Limit: ServiceAttemptLimit}); err != nil {
				return Snapshot{}, err
			}
		}
		outages, err := s.Outages(ctx, c.Name, IncidentLimit)
		if err != nil {
			return Snapshot{}, err
		}
		svc.Outages = outages[:min(len(outages), outageLimit)]
		var period []store.Outage
		for _, o := range outages {
			if o.Ongoing || o.End.After(since) {
				period = append(period, o)
			}
		}
		var matched []store.Attempt
		if hasAttempts && len(period) > 0 {
			if matched, err = attempts.Attempts(ctx, store.AttemptFilter{Monitor: c.Name, Since: period[len(period)-1].Start, Limit: store.MaxAttemptLimit}); err != nil {
				return Snapshot{}, err
			}
		}
		svc.Incidents = matchAlerts(period, matched)
		// Like today's uptime, the period's needs a heartbeat to end at.
		periodUp, periodObserved, periodIncidents, err := s.UptimeWindow(ctx, c.Name, since, until)
		if err != nil {
			return Snapshot{}, err
		}
		svc.PeriodIncidents = periodIncidents
		if snap.HasBeat {
			svc.PeriodUp, svc.PeriodObserved = periodUp, periodObserved
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
