package remote

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
	"github.com/heohlee/up/internal/store"
	"github.com/heohlee/up/internal/tui"
)

// SnapshotDTO is the secret-free wire representation of a TUI snapshot.
type SnapshotDTO struct {
	ServerTime  time.Time       `json:"server_time"`
	Services    []ServiceDTO    `json:"services"`
	SMTPServers []SMTPServerDTO `json:"smtp_servers"`
	Channels    []ChannelDTO    `json:"channels"`
	PublicKey   []byte          `json:"public_key"`
	HasKey      bool            `json:"has_key"`
	LastBeat    time.Time       `json:"last_beat"`
	HasBeat     bool            `json:"has_beat"`
	Uptime      float64         `json:"uptime"`
	HasUptime   bool            `json:"has_uptime"`
	Incidents   int             `json:"incidents"`
	// ChannelTests is each channel's newest test request.
	ChannelTests []ChannelTestDTO `json:"channel_tests"`
	// Attempts are the latest delivery attempts of all monitors, newest first;
	// LastAttempts is the newest of each channel. Both are bounded.
	Attempts     []AttemptDTO `json:"attempts"`
	LastAttempts []AttemptDTO `json:"last_attempts"`
	// Defaults are what new monitors and maintenance windows start from.
	Defaults DefaultsDTO `json:"defaults"`
	// API is the daemon's API listener, which heartbeat pings reach.
	API APIDTO `json:"api"`
}

// APIDTO is the address the daemon's API listens on, as bound, and whether
// it serves HTTPS. An empty listen means no API.
type APIDTO struct {
	Listen string `json:"listen"`
	TLS    bool   `json:"tls"`
}

// HeartbeatDTO is a heartbeat monitor's ping state (see tui.HeartbeatState).
// The ping token is never part of it.
type HeartbeatDTO struct {
	Waiting  bool      `json:"waiting"`
	LastPing time.Time `json:"last_ping"`
	Deadline time.Time `json:"deadline"`
}

// CheckDTO is a check on the wire, in both directions.
//
// In a snapshot (and in write responses) protected values show only whether
// they are set: BodySet and HeaderDTO.ValueSet. In a write, Body and
// HeaderDTO.Value carry the edit (ProtectedDTO; absent keeps) and the *Set
// fields are ignored. Kind is "http" or "tcp" (empty on write means http) and
// Method is the HTTP method (empty on write means GET). Host has no brackets
// for IPv6. Conditions are plain and travel the same way in both directions.
// A heartbeat has kind "heartbeat", GraceMS, a Schedule ("period", read
// back for every period heartbeat, or "cron"; empty on write means period)
// with PeriodMS or Cron, and none of the HTTP or TCP fields; its ping token
// is never in a CheckDTO.
type CheckDTO struct {
	Name           string         `json:"name"`
	URL            string         `json:"url"`
	IntervalMS     int64          `json:"interval_ms"`
	TimeoutMS      int64          `json:"timeout_ms"`
	ExpectedStatus int            `json:"expected_status"`
	Channel        string         `json:"channel"`
	AlertAfter     int            `json:"alert_after"`
	RecoverAfter   int            `json:"recover_after"`
	TLSOff         bool           `json:"tls_off"`
	Kind           string         `json:"kind"`
	Method         string         `json:"method"`
	Headers        []HeaderDTO    `json:"headers"`
	Body           *ProtectedDTO  `json:"body,omitempty"`
	BodySet        bool           `json:"body_set"`
	ExpectText     string         `json:"expect_text"`
	Conditions     []ConditionDTO `json:"conditions"`
	Host           string         `json:"host"`
	Port           int            `json:"port"`
	PeriodMS       int64          `json:"period_ms"`
	GraceMS        int64          `json:"grace_ms"`
	Schedule       string         `json:"schedule"`
	Cron           string         `json:"cron"`
}

// HeaderDTO is one request header: a plain name and a protected value.
type HeaderDTO struct {
	Name     string        `json:"name"`
	ValueSet bool          `json:"value_set"`
	Value    *ProtectedDTO `json:"value,omitempty"`
}

// ConditionDTO is one HTTP response condition (see config.Condition). Kind is
// json, regex, response_time or header; only that kind's fields are set.
type ConditionDTO struct {
	Kind     string `json:"kind"`
	Path     string `json:"path,omitempty"`
	Name     string `json:"name,omitempty"`
	Value    string `json:"value,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
	WithinMS int64  `json:"within_ms,omitempty"`
}

func conditionDTOs(conds []config.Condition) []ConditionDTO {
	var out []ConditionDTO
	for _, c := range conds {
		out = append(out, ConditionDTO{Kind: c.Kind, Path: c.Path, Name: c.Name, Value: c.Value, Pattern: c.Pattern, WithinMS: c.Within.Milliseconds()})
	}
	return out
}

// conditions converts the wire form; WithinMS must already be in range.
func conditions(dtos []ConditionDTO) []config.Condition {
	var out []config.Condition
	for _, d := range dtos {
		out = append(out, config.Condition{Kind: d.Kind, Path: d.Path, Name: d.Name, Value: d.Value, Pattern: d.Pattern, Within: time.Duration(d.WithinMS) * time.Millisecond})
	}
	return out
}

// MaintenanceDTO is a stored maintenance window. Weekdays are 0 (Sunday) to 6.
type MaintenanceDTO struct {
	ID          int64  `json:"id"`
	Weekdays    []int  `json:"weekdays"`
	StartMinute int    `json:"start_minute"`
	DurationMS  int64  `json:"duration_ms"`
	Zone        string `json:"zone"`
}

// MaintenanceWriteDTO is the body that creates or replaces a window; the
// check and ID come from the path.
type MaintenanceWriteDTO struct {
	Weekdays    []int  `json:"weekdays"`
	StartMinute int    `json:"start_minute"`
	DurationMS  int64  `json:"duration_ms"`
	Zone        string `json:"zone"`
}

// ChannelTestDTO is a channel test request. Status is pending, sending, ok or
// error; Error is short and secret-free. Unset times are the zero time.
type ChannelTestDTO struct {
	ID        int64     `json:"id"`
	Channel   string    `json:"channel"`
	Status    string    `json:"status"`
	Error     string    `json:"error"`
	Requested time.Time `json:"requested"`
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished"`
}
type ResultDTO struct {
	At         time.Time    `json:"at"`
	Status     check.Status `json:"status"`
	LatencyMS  int64        `json:"latency_ms"`
	StatusCode int          `json:"status_code"`
	Error      string       `json:"error"`
	// Run is a missed cron run's (check.Result.Run); absent otherwise.
	Run time.Time `json:"run,omitzero"`
}
type OutageDTO struct {
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Ongoing bool      `json:"ongoing"`
	Error   string    `json:"error"`
}
type StreakDTO struct {
	Alerting  bool `json:"alerting"`
	Failures  int  `json:"failures"`
	Successes int  `json:"successes"`
}
type MuteDTO struct {
	Until       time.Time `json:"until"`
	WasAlerting bool      `json:"was_alerting"`
}
type CertDTO struct {
	Host     string    `json:"host"`
	Issuer   string    `json:"issuer"`
	Serial   string    `json:"serial"`
	NotAfter time.Time `json:"not_after"`
	Observed time.Time `json:"observed"`
	Notified int       `json:"notified"`
}
type ServiceDTO struct {
	Check     CheckDTO    `json:"check"`
	Latest    ResultDTO   `json:"latest"`
	HasResult bool        `json:"has_result"`
	DownSince time.Time   `json:"down_since"`
	Recent    []ResultDTO `json:"recent"`
	Outages   []OutageDTO `json:"outages"`
	Streak    StreakDTO   `json:"streak"`
	Mute      MuteDTO     `json:"mute"`
	HasMute   bool        `json:"has_mute"`
	Cert      CertDTO     `json:"cert"`
	HasCert   bool        `json:"has_cert"`
	// Maintenance are the monitor's windows; InMaintenance and
	// MaintenanceEnd are their evaluation at ServerTime.
	Maintenance    []MaintenanceDTO `json:"maintenance"`
	InMaintenance  bool             `json:"in_maintenance"`
	MaintenanceEnd time.Time        `json:"maintenance_end"`
	// KnownAlerting is whether the recipients were last told it is down.
	KnownAlerting bool `json:"known_alerting"`
	// Attempts are the monitor's latest delivery attempts, newest first.
	Attempts []AttemptDTO `json:"attempts"`
	// Incidents are the outages of the last 30 days, newest first and
	// bounded, with the outcome of their alerts. PeriodUpMS,
	// PeriodObservedMS and PeriodIncidents measure the same 30 days.
	Incidents        []IncidentDTO `json:"incidents"`
	PeriodUpMS       int64         `json:"period_up_ms"`
	PeriodObservedMS int64         `json:"period_observed_ms"`
	PeriodIncidents  int           `json:"period_incidents"`
	// Heartbeat is set for heartbeat monitors only.
	Heartbeat *HeartbeatDTO `json:"heartbeat,omitempty"`
}

// IncidentDTO is an outage with the worst outcome of its alerts (sent,
// failed, held; empty when none was made), the channel with that outcome
// when channels disagreed and its secret-free reason.
type IncidentDTO struct {
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Ongoing bool      `json:"ongoing"`
	Error   string    `json:"error"`
	Alert   string    `json:"alert"`
	Channel string    `json:"channel"`
	Reason  string    `json:"reason"`
}

// AttemptDTO is one alert delivery attempt. Event is monitor.down,
// monitor.recovered or cert.expiring; Outcome is sent, failed or held; Reason
// is short and secret-free.
type AttemptDTO struct {
	At      time.Time `json:"at"`
	Monitor string    `json:"monitor"`
	Channel string    `json:"channel"`
	Event   string    `json:"event"`
	Outcome string    `json:"outcome"`
	Reason  string    `json:"reason"`
}

func attemptDTOs(list []store.Attempt) []AttemptDTO {
	out := []AttemptDTO{}
	for _, a := range list {
		out = append(out, AttemptDTO{a.At.UTC(), a.Monitor, a.Channel, string(a.Event), string(a.Outcome), a.Reason})
	}
	return out
}

// Attempts converts the wire form back; an empty list is nil.
func Attempts(list []AttemptDTO) []store.Attempt {
	var out []store.Attempt
	for _, a := range list {
		out = append(out, store.Attempt{At: a.At, Monitor: a.Monitor, Channel: a.Channel, Event: store.Event(a.Event), Outcome: store.Outcome(a.Outcome), Reason: a.Reason})
	}
	return out
}

type SMTPServerDTO struct {
	Name          string         `json:"name"`
	Host          string         `json:"host"`
	Port          int            `json:"port"`
	TLS           config.TLSMode `json:"tls"`
	Username      string         `json:"username"`
	Sender        string         `json:"sender"`
	PasswordState string         `json:"password_state"`
}

// ChannelDTO is a channel as the snapshot and write responses show it. A
// webhook, discord or slack channel shows only its origin (scheme and host)
// and whether its URL and auth value are set; ntfy, telegram and gotify
// show their plain fields and whether a token is set. The protected values
// never leave the server.
type ChannelDTO struct {
	Name            string   `json:"name"`
	Kind            string   `json:"kind"`
	SMTPServer      string   `json:"smtp_server"`
	Recipients      []string `json:"recipients"`
	WebhookOrigin   string   `json:"webhook_origin"`
	WebhookURLSet   bool     `json:"webhook_url_set"`
	WebhookAuthName string   `json:"webhook_auth_name"`
	WebhookAuthSet  bool     `json:"webhook_auth_set"`
	ServerURL       string   `json:"server_url"`
	Topic           string   `json:"topic"`
	ChatID          string   `json:"chat_id"`
	TokenSet        bool     `json:"token_set"`
	Priority        string   `json:"priority"`
}

func channelDTO(c config.Channel) ChannelDTO {
	return ChannelDTO{Name: c.Name, Kind: c.Kind, SMTPServer: c.SMTPServer, Recipients: c.Recipients, WebhookOrigin: c.WebhookOrigin,
		WebhookURLSet: !c.WebhookURL.Empty(), WebhookAuthName: c.WebhookAuthName, WebhookAuthSet: !c.WebhookAuth.Empty(),
		ServerURL: c.ServerURL, Topic: c.Topic, ChatID: c.ChatID, TokenSet: !c.Token.Empty(), Priority: c.Priority}
}

// checkDTO is the read form of a check: plain fields and "set" flags.
func checkDTO(c config.Check) CheckDTO {
	d := CheckDTO{Name: c.Name, URL: c.URL, IntervalMS: c.Interval.Milliseconds(), TimeoutMS: c.Timeout.Milliseconds(), ExpectedStatus: c.ExpectedStatus,
		Channel: c.Channel, AlertAfter: c.AlertAfter, RecoverAfter: c.RecoverAfter, TLSOff: c.TLSOff,
		Kind: c.Kind, Method: c.Method, BodySet: !c.Body.Empty(), ExpectText: c.ExpectText, Host: c.Host, Port: c.Port,
		PeriodMS: c.Period.Milliseconds(), GraceMS: c.Grace.Milliseconds(), Conditions: conditionDTOs(c.Conditions),
		Schedule: c.Schedule, Cron: c.Cron}
	if d.Kind == "" {
		d.Kind = config.CheckHTTP
	}
	if d.Kind == config.CheckHeartbeat && d.Schedule == "" {
		d.Schedule = config.SchedulePeriod
	}
	if d.Kind == config.CheckHTTP && d.Method == "" {
		d.Method = "GET"
	}
	for _, h := range c.Headers {
		d.Headers = append(d.Headers, HeaderDTO{Name: h.Name, ValueSet: !h.Value.Empty()})
	}
	return d
}

func maintenanceDTO(w config.MaintenanceWindow) MaintenanceDTO {
	d := MaintenanceDTO{ID: w.ID, StartMinute: w.StartMinute, DurationMS: w.Duration.Milliseconds(), Zone: w.Zone, Weekdays: []int{}}
	for _, day := range w.Weekdays {
		d.Weekdays = append(d.Weekdays, int(day))
	}
	return d
}

func (d MaintenanceDTO) window(check string) config.MaintenanceWindow {
	w := config.MaintenanceWindow{ID: d.ID, Check: check, StartMinute: d.StartMinute, Duration: time.Duration(d.DurationMS) * time.Millisecond, Zone: d.Zone}
	for _, day := range d.Weekdays {
		w.Weekdays = append(w.Weekdays, time.Weekday(day))
	}
	return w
}

func channelTestDTO(t config.ChannelTest) ChannelTestDTO {
	return ChannelTestDTO{ID: t.ID, Channel: t.Channel, Status: string(t.Status), Error: t.Error, Requested: t.Requested.UTC(), Started: t.Started.UTC(), Finished: t.Finished.UTC()}
}

// ChannelTest converts the wire form back.
func (d ChannelTestDTO) ChannelTest() config.ChannelTest {
	return config.ChannelTest{ID: d.ID, Channel: d.Channel, Status: config.ChannelTestStatus(d.Status), Error: d.Error, Requested: d.Requested, Started: d.Started, Finished: d.Finished}
}

// Window converts the wire form back for a check.
func (d MaintenanceDTO) Window(check string) config.MaintenanceWindow { return d.window(check) }

func resultDTO(r check.Result) ResultDTO {
	return ResultDTO{r.At.UTC(), r.Status, r.Latency.Milliseconds(), r.StatusCode, r.Error, r.Run.UTC()}
}
func (r ResultDTO) result(name string) check.Result {
	return check.Result{CheckName: name, At: r.At, Status: r.Status, Latency: time.Duration(r.LatencyMS) * time.Millisecond, StatusCode: r.StatusCode, Error: r.Error, Run: r.Run}
}

// NewSnapshotDTO converts field by field without exposing sealed credentials.
func NewSnapshotDTO(s tui.Snapshot, now time.Time) SnapshotDTO {
	d := SnapshotDTO{ServerTime: now.UTC(), PublicKey: s.PublicKey, HasKey: s.HasKey, LastBeat: s.LastBeat.UTC(), HasBeat: s.HasBeat, Uptime: s.Uptime, HasUptime: s.HasUptime, Incidents: s.Incidents}
	for _, v := range s.Services {
		c := v.Check
		x := ServiceDTO{Check: checkDTO(c), Latest: resultDTO(v.Latest), HasResult: v.HasResult, DownSince: v.DownSince.UTC(), Streak: StreakDTO{v.Streak.Alerting, v.Streak.Failures, v.Streak.Successes}, Mute: MuteDTO{v.Mute.Until.UTC(), v.Mute.WasAlerting}, HasMute: v.HasMute, Cert: CertDTO{v.Cert.Host, v.Cert.Issuer, v.Cert.Serial, v.Cert.NotAfter.UTC(), v.Cert.Observed.UTC(), v.Cert.Notified}, HasCert: v.HasCert}
		for _, r := range v.Recent {
			x.Recent = append(x.Recent, resultDTO(r))
		}
		for _, o := range v.Outages {
			x.Outages = append(x.Outages, OutageDTO{o.Start.UTC(), o.End.UTC(), o.Ongoing, o.Error})
		}
		x.Maintenance = []MaintenanceDTO{}
		for _, w := range v.Maintenance {
			x.Maintenance = append(x.Maintenance, maintenanceDTO(w))
		}
		x.InMaintenance, x.MaintenanceEnd = v.InMaintenance.Active, v.InMaintenance.End.UTC()
		x.KnownAlerting = v.KnownAlerting
		x.Attempts = attemptDTOs(v.Attempts)
		x.Incidents = []IncidentDTO{}
		for _, i := range v.Incidents {
			x.Incidents = append(x.Incidents, IncidentDTO{i.Start.UTC(), i.End.UTC(), i.Ongoing, i.Error, string(i.Alert), i.Channel, i.Reason})
		}
		x.PeriodUpMS, x.PeriodObservedMS, x.PeriodIncidents = v.PeriodUp.Milliseconds(), v.PeriodObserved.Milliseconds(), v.PeriodIncidents
		if c.Kind == config.CheckHeartbeat {
			x.Heartbeat = &HeartbeatDTO{v.Heartbeat.Waiting, v.Heartbeat.LastPing.UTC(), v.Heartbeat.Deadline.UTC()}
		}
		d.Services = append(d.Services, x)
	}
	for _, v := range s.SMTPServers {
		state := "none"
		if !v.Password.Empty() {
			state = "needs_reentry"
			if s.HasKey && bytes.Equal(v.Password.Key, s.PublicKey) {
				state = "set"
			}
		}
		d.SMTPServers = append(d.SMTPServers, SMTPServerDTO{v.Name, v.Host, v.Port, v.TLS, v.Username, v.Sender, state})
	}
	for _, v := range s.Channels {
		d.Channels = append(d.Channels, channelDTO(v))
	}
	d.Attempts, d.LastAttempts = attemptDTOs(s.Attempts), attemptDTOs(s.LastAttempts)
	d.Defaults = defaultsDTO(s.Defaults)
	d.API = APIDTO{Listen: s.API.Address, TLS: s.API.TLS}
	d.ChannelTests = []ChannelTestDTO{}
	for _, t := range s.ChannelTests {
		d.ChannelTests = append(d.ChannelTests, channelTestDTO(t))
	}
	return d
}

// Snapshot rebuilds display-only password placeholders, never ciphertext.
func (d SnapshotDTO) Snapshot() tui.Snapshot {
	s := tui.Snapshot{PublicKey: d.PublicKey, HasKey: d.HasKey, LastBeat: d.LastBeat, HasBeat: d.HasBeat, Uptime: d.Uptime, HasUptime: d.HasUptime, Incidents: d.Incidents}
	for _, v := range d.Services {
		c := v.Check
		x := tui.Service{Check: c.snapshotCheck(d.PublicKey), Latest: v.Latest.result(c.Name), HasResult: v.HasResult, DownSince: v.DownSince, Streak: check.Streak{Alerting: v.Streak.Alerting, Failures: v.Streak.Failures, Successes: v.Streak.Successes}, Mute: config.Mute{Until: v.Mute.Until, WasAlerting: v.Mute.WasAlerting}, HasMute: v.HasMute, Cert: config.Cert{Host: v.Cert.Host, Issuer: v.Cert.Issuer, Serial: v.Cert.Serial, NotAfter: v.Cert.NotAfter, Observed: v.Cert.Observed, Notified: v.Cert.Notified}, HasCert: v.HasCert}
		if v.HasMute {
			x.Mute.Check = c.Name
		}
		if v.HasCert {
			x.Cert.Check = c.Name
		}
		if !v.HasResult {
			x.Latest.CheckName = ""
		}
		for _, r := range v.Recent {
			x.Recent = append(x.Recent, r.result(c.Name))
		}
		for _, o := range v.Outages {
			x.Outages = append(x.Outages, store.Outage{Start: o.Start, End: o.End, Ongoing: o.Ongoing, Error: o.Error})
		}
		for _, w := range v.Maintenance {
			x.Maintenance = append(x.Maintenance, w.window(c.Name))
		}
		x.InMaintenance = config.Maintenance{Active: v.InMaintenance, End: v.MaintenanceEnd}
		if !v.InMaintenance {
			x.InMaintenance.End = time.Time{}
		}
		x.KnownAlerting = v.KnownAlerting
		x.Attempts = Attempts(v.Attempts)
		for _, i := range v.Incidents {
			x.Incidents = append(x.Incidents, tui.Incident{Outage: store.Outage{Start: i.Start, End: i.End, Ongoing: i.Ongoing, Error: i.Error}, Alert: store.Outcome(i.Alert), Channel: i.Channel, Reason: i.Reason})
		}
		x.PeriodUp, x.PeriodObserved, x.PeriodIncidents = time.Duration(v.PeriodUpMS)*time.Millisecond, time.Duration(v.PeriodObservedMS)*time.Millisecond, v.PeriodIncidents
		if h := v.Heartbeat; h != nil {
			x.Heartbeat = tui.HeartbeatState{Waiting: h.Waiting, LastPing: zeroUTC(h.LastPing), Deadline: zeroUTC(h.Deadline)}
		}
		s.Services = append(s.Services, x)
	}
	for _, v := range d.SMTPServers {
		p := secret.Sealed{}
		switch v.PasswordState {
		case "set":
			p = secret.Sealed{Key: d.PublicKey, Data: []byte{1}}
		case "needs_reentry":
			p = secret.Sealed{Key: []byte{0}, Data: []byte{1}}
		}
		s.SMTPServers = append(s.SMTPServers, config.SMTPServer{Name: v.Name, Host: v.Host, Port: v.Port, TLS: v.TLS, Username: v.Username, Sender: v.Sender, Password: p})
	}
	for _, v := range d.Channels {
		c := config.Channel{Name: v.Name, Kind: v.Kind, SMTPServer: v.SMTPServer, Recipients: v.Recipients, WebhookOrigin: v.WebhookOrigin, WebhookAuthName: v.WebhookAuthName,
			ServerURL: v.ServerURL, Topic: v.Topic, ChatID: v.ChatID, Priority: v.Priority}
		if v.WebhookURLSet {
			c.WebhookURL = placeholder(d.PublicKey)
		}
		if v.WebhookAuthSet {
			c.WebhookAuth = placeholder(d.PublicKey)
		}
		if v.TokenSet {
			c.Token = placeholder(d.PublicKey)
		}
		s.Channels = append(s.Channels, c)
	}
	for _, t := range d.ChannelTests {
		s.ChannelTests = append(s.ChannelTests, t.ChannelTest())
	}
	s.Attempts, s.LastAttempts = Attempts(d.Attempts), Attempts(d.LastAttempts)
	s.Defaults = d.Defaults.defaults()
	s.API = config.APIListener{Address: d.API.Listen, TLS: d.API.TLS}
	return s
}

// zeroUTC keeps an unset time the zero value, which JSON turns into a
// non-zero location otherwise.
func zeroUTC(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t
}

// SnapshotRoutes serves the authenticated snapshot endpoint.
func SnapshotRoutes(load func(context.Context, time.Time) (tui.Snapshot, error)) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		now := time.Now()
		s, err := load(ctx, now)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not read the database")
			return
		}
		writeJSON(w, http.StatusOK, NewSnapshotDTO(s, now))
	})
	return mux
}

// placeholder stands in for a protected value that is set: it is not a real
// ciphertext and exists only so the TUI can tell "set" from "empty".
func placeholder(key []byte) secret.Sealed { return secret.Sealed{Key: key, Data: []byte{1}} }

// snapshotCheck rebuilds a check from a snapshot; protected values become
// placeholders and canonical forms are restored.
func (c CheckDTO) snapshotCheck(key []byte) config.Check {
	out := config.Check{Name: c.Name, URL: c.URL, Interval: time.Duration(c.IntervalMS) * time.Millisecond, Timeout: time.Duration(c.TimeoutMS) * time.Millisecond,
		ExpectedStatus: c.ExpectedStatus, Channel: c.Channel, AlertAfter: c.AlertAfter, RecoverAfter: c.RecoverAfter, TLSOff: c.TLSOff,
		Kind: c.Kind, Method: c.Method, ExpectText: c.ExpectText, Host: c.Host, Port: c.Port,
		Period: time.Duration(c.PeriodMS) * time.Millisecond, Grace: time.Duration(c.GraceMS) * time.Millisecond, Conditions: conditions(c.Conditions),
		Schedule: c.Schedule, Cron: c.Cron}
	if c.BodySet {
		out.Body = placeholder(key)
	}
	for _, h := range c.Headers {
		header := config.Header{Name: h.Name}
		if h.ValueSet {
			header.Value = placeholder(key)
		}
		out.Headers = append(out.Headers, header)
	}
	return out.Canonical()
}
