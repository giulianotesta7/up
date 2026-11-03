package remote

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
	"github.com/giulianotesta7/up/internal/tui"
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
}
type CheckDTO struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	IntervalMS     int64  `json:"interval_ms"`
	TimeoutMS      int64  `json:"timeout_ms"`
	ExpectedStatus int    `json:"expected_status"`
	Channel        string `json:"channel"`
	AlertAfter     int    `json:"alert_after"`
	RecoverAfter   int    `json:"recover_after"`
	TLSOff         bool   `json:"tls_off"`
}
type ResultDTO struct {
	At         time.Time    `json:"at"`
	Status     check.Status `json:"status"`
	LatencyMS  int64        `json:"latency_ms"`
	StatusCode int          `json:"status_code"`
	Error      string       `json:"error"`
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
type ChannelDTO struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	SMTPServer string   `json:"smtp_server"`
	Recipients []string `json:"recipients"`
}

func resultDTO(r check.Result) ResultDTO {
	return ResultDTO{r.At.UTC(), r.Status, r.Latency.Milliseconds(), r.StatusCode, r.Error}
}
func (r ResultDTO) result(name string) check.Result {
	return check.Result{CheckName: name, At: r.At, Status: r.Status, Latency: time.Duration(r.LatencyMS) * time.Millisecond, StatusCode: r.StatusCode, Error: r.Error}
}

// NewSnapshotDTO converts field by field without exposing sealed credentials.
func NewSnapshotDTO(s tui.Snapshot, now time.Time) SnapshotDTO {
	d := SnapshotDTO{ServerTime: now.UTC(), PublicKey: s.PublicKey, HasKey: s.HasKey, LastBeat: s.LastBeat.UTC(), HasBeat: s.HasBeat, Uptime: s.Uptime, HasUptime: s.HasUptime, Incidents: s.Incidents}
	for _, v := range s.Services {
		c := v.Check
		x := ServiceDTO{Check: CheckDTO{c.Name, c.URL, c.Interval.Milliseconds(), c.Timeout.Milliseconds(), c.ExpectedStatus, c.Channel, c.AlertAfter, c.RecoverAfter, c.TLSOff}, Latest: resultDTO(v.Latest), HasResult: v.HasResult, DownSince: v.DownSince.UTC(), Streak: StreakDTO{v.Streak.Alerting, v.Streak.Failures, v.Streak.Successes}, Mute: MuteDTO{v.Mute.Until.UTC(), v.Mute.WasAlerting}, HasMute: v.HasMute, Cert: CertDTO{v.Cert.Host, v.Cert.Issuer, v.Cert.Serial, v.Cert.NotAfter.UTC(), v.Cert.Observed.UTC(), v.Cert.Notified}, HasCert: v.HasCert}
		for _, r := range v.Recent {
			x.Recent = append(x.Recent, resultDTO(r))
		}
		for _, o := range v.Outages {
			x.Outages = append(x.Outages, OutageDTO{o.Start.UTC(), o.End.UTC(), o.Ongoing, o.Error})
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
		d.Channels = append(d.Channels, ChannelDTO{v.Name, v.Kind, v.SMTPServer, v.Recipients})
	}
	return d
}

// Snapshot rebuilds display-only password placeholders, never ciphertext.
func (d SnapshotDTO) Snapshot() tui.Snapshot {
	s := tui.Snapshot{PublicKey: d.PublicKey, HasKey: d.HasKey, LastBeat: d.LastBeat, HasBeat: d.HasBeat, Uptime: d.Uptime, HasUptime: d.HasUptime, Incidents: d.Incidents}
	for _, v := range d.Services {
		c := v.Check
		x := tui.Service{Check: config.Check{Name: c.Name, URL: c.URL, Interval: time.Duration(c.IntervalMS) * time.Millisecond, Timeout: time.Duration(c.TimeoutMS) * time.Millisecond, ExpectedStatus: c.ExpectedStatus, Channel: c.Channel, AlertAfter: c.AlertAfter, RecoverAfter: c.RecoverAfter, TLSOff: c.TLSOff}, Latest: v.Latest.result(c.Name), HasResult: v.HasResult, DownSince: v.DownSince, Streak: check.Streak{Alerting: v.Streak.Alerting, Failures: v.Streak.Failures, Successes: v.Streak.Successes}, Mute: config.Mute{Until: v.Mute.Until, WasAlerting: v.Mute.WasAlerting}, HasMute: v.HasMute, Cert: config.Cert{Host: v.Cert.Host, Issuer: v.Cert.Issuer, Serial: v.Cert.Serial, NotAfter: v.Cert.NotAfter, Observed: v.Cert.Observed, Notified: v.Cert.Notified}, HasCert: v.HasCert}
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
		s.Channels = append(s.Channels, config.Channel{Name: v.Name, Kind: v.Kind, SMTPServer: v.SMTPServer, Recipients: v.Recipients})
	}
	return s
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
