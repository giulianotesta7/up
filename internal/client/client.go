// Package client implements the remote TUI store.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/connection"
	"github.com/giulianotesta7/up/internal/remote"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
	"github.com/giulianotesta7/up/internal/tui"
)

const maxResponse = 16 << 20

var errReads = errors.New("client: reads go through LoadSnapshot")
var errRedirect = errors.New("server redirected the request; use its final URL")
var errIncompatible = errors.New("the server is not a compatible up server")

type Client struct {
	base, token string
	http        *http.Client
}

func New(serverURL, token string) *Client {
	return &Client{base: strings.TrimRight(serverURL, "/"), token: token, http: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: proxyFor, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// proxyFor keeps a plain http request away from any proxy: its bearer token is
// already unencrypted and a proxy would only widen who can read it. https
// follows the usual environment rules, where a proxy only tunnels.
func proxyFor(req *http.Request) (*url.URL, error) {
	if req.URL.Scheme == "http" {
		return nil, nil
	}
	return http.ProxyFromEnvironment(req)
}

var _ tui.Store = (*Client)(nil)
var _ tui.SnapshotLoader = (*Client)(nil)

func transportError(err error) error {
	var ua x509.UnknownAuthorityError
	var ci x509.CertificateInvalidError
	var hn x509.HostnameError
	var cv *tls.CertificateVerificationError
	if errors.As(err, &ua) || errors.As(err, &ci) || errors.As(err, &hn) || errors.As(err, &cv) {
		return fmt.Errorf("%w: certificate verification failed", connection.ErrCertUntrusted)
	}
	reason := "connection failed"
	var ne net.Error
	if errors.Is(err, context.Canceled) {
		reason = "request canceled"
	} else if errors.Is(err, syscall.ECONNREFUSED) {
		reason = "connection refused"
	} else if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		reason = "connection timed out"
	}
	return fmt.Errorf("%w: %s", connection.ErrUnreachable, reason)
}
func (c *Client) request(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, errors.New("client: cannot encode request")
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return 0, nil, errors.New("client: invalid request URL")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "up")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, transportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return resp.StatusCode, nil, errRedirect
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return 0, nil, transportError(err)
	}
	if len(b) > maxResponse {
		return resp.StatusCode, nil, errors.New("server response too large")
	}
	return resp.StatusCode, b, nil
}
func (c *Client) responseError(status int, b []byte) error {
	if status == 401 {
		return connection.ErrTokenRejected
	}
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		if c.token != "" && strings.Contains(e.Error, c.token) {
			return errors.New("server returned an error")
		}
		return errors.New(clean(e.Error))
	}
	return fmt.Errorf("server returned %d", status)
}
func (c *Client) write(ctx context.Context, method, path string, body any) error {
	status, b, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	if status >= 200 && status < 300 {
		return nil
	}
	return c.responseError(status, b)
}
func Validate(ctx context.Context, serverURL, token string) error {
	c := New(serverURL, token)
	status, b, err := c.request(ctx, http.MethodGet, "/api/v1/ping", nil)
	if err != nil {
		return err
	}
	if status == 401 {
		return connection.ErrTokenRejected
	}
	var p struct {
		Name string `json:"name"`
	}
	if status != 200 || json.Unmarshal(b, &p) != nil || p.Name != "up" {
		return errIncompatible
	}
	return nil
}
func (c *Client) LoadSnapshot(ctx context.Context, now time.Time) (tui.Snapshot, error) {
	status, b, err := c.request(ctx, http.MethodGet, "/api/v1/snapshot", nil)
	if err != nil {
		return tui.Snapshot{}, err
	}
	if status < 200 || status >= 300 {
		return tui.Snapshot{}, c.responseError(status, b)
	}
	var d remote.SnapshotDTO
	if json.Unmarshal(b, &d) != nil {
		return tui.Snapshot{}, errors.New("client: invalid snapshot")
	}
	s := d.Snapshot()
	sanitize(reflect.ValueOf(&s).Elem())
	skew := now.Sub(d.ServerTime)
	shift := func(t time.Time) time.Time {
		if t.IsZero() {
			return t
		}
		return t.Add(skew)
	}
	// now is read before the request, so the skew can be a few milliseconds
	// negative; a boundary that sat on a whole minute (a window end, the end
	// of a mute, a certificate expiry) would then show as the previous minute.
	// Such a boundary stays on its minute.
	boundary := func(t time.Time) time.Time {
		shifted := shift(t)
		if !t.IsZero() && t.Equal(t.Truncate(time.Minute)) {
			return shifted.Round(time.Minute)
		}
		return shifted
	}
	s.LastBeat = shift(s.LastBeat)
	for i := range s.ChannelTests {
		t := &s.ChannelTests[i]
		t.Requested, t.Started, t.Finished = shift(t.Requested), shift(t.Started), shift(t.Finished)
	}
	for i := range s.Services {
		v := &s.Services[i]
		v.InMaintenance.End = boundary(v.InMaintenance.End)
		v.Latest.At = shift(v.Latest.At)
		v.DownSince = shift(v.DownSince)
		v.Mute.Until = boundary(v.Mute.Until)
		v.Cert.NotAfter = boundary(v.Cert.NotAfter)
		v.Cert.Observed = shift(v.Cert.Observed)
		for j := range v.Recent {
			v.Recent[j].At = shift(v.Recent[j].At)
		}
		for j := range v.Outages {
			v.Outages[j].Start = shift(v.Outages[j].Start)
			v.Outages[j].End = shift(v.Outages[j].End)
		}
	}
	return s, nil
}
func (c *Client) Checks(context.Context) ([]config.Check, error)               { return nil, errReads }
func (c *Client) Recent(context.Context, string, int) ([]check.Result, error)  { return nil, errReads }
func (c *Client) Outages(context.Context, string, int) ([]store.Outage, error) { return nil, errReads }
func (c *Client) LastBeat(context.Context) (time.Time, bool, error) {
	return time.Time{}, false, errReads
}
func (c *Client) UptimeWindow(context.Context, string, time.Time, time.Time) (time.Duration, time.Duration, int, error) {
	return 0, 0, 0, errReads
}
func (c *Client) Mutes(context.Context) ([]config.Mute, error)             { return nil, errReads }
func (c *Client) Certs(context.Context) ([]config.Cert, error)             { return nil, errReads }
func (c *Client) SMTPServers(context.Context) ([]config.SMTPServer, error) { return nil, errReads }
func (c *Client) Channels(context.Context) ([]config.Channel, error)       { return nil, errReads }
func (c *Client) PublicKey(context.Context) ([]byte, bool, error)          { return nil, false, errReads }

// checkWrite is the body of a check create or update. Fields added after the
// first release are omitted when empty, so a plain check keeps its old wire
// form.
type checkWrite struct {
	Name           string          `json:"name"`
	URL            string          `json:"url"`
	IntervalMS     int64           `json:"interval_ms"`
	TimeoutMS      int64           `json:"timeout_ms"`
	ExpectedStatus int             `json:"expected_status"`
	Channel        string          `json:"channel"`
	AlertAfter     int             `json:"alert_after"`
	RecoverAfter   int             `json:"recover_after"`
	TLSOff         bool            `json:"tls_off"`
	Kind           string          `json:"kind,omitempty"`
	Method         string          `json:"method,omitempty"`
	Headers        []headerWrite   `json:"headers,omitempty"`
	Body           *protectedWrite `json:"body,omitempty"`
	ExpectText     string          `json:"expect_text,omitempty"`
	Host           string          `json:"host,omitempty"`
	Port           int             `json:"port,omitempty"`
	// CopyFrom (create only) names the stored monitor whose protected values
	// the server copies for every value this check keeps.
	CopyFrom string `json:"copy_from,omitempty"`
}

type headerWrite struct {
	Name  string          `json:"name"`
	Value *protectedWrite `json:"value,omitempty"`
}

func checkDTO(v config.Check) checkWrite {
	d := checkWrite{Name: v.Name, URL: v.URL, IntervalMS: v.Interval.Milliseconds(), TimeoutMS: v.Timeout.Milliseconds(), ExpectedStatus: v.ExpectedStatus, Channel: v.Channel,
		AlertAfter: v.AlertAfter, RecoverAfter: v.RecoverAfter, TLSOff: v.TLSOff, Kind: v.Kind, Method: v.Method,
		Body: protectedFor(v.BodyEdit, v.Body), ExpectText: v.ExpectText, Host: v.Host, Port: v.Port}
	for _, h := range v.Headers {
		d.Headers = append(d.Headers, headerWrite{Name: h.Name, Value: protectedFor(h.Edit, h.Value)})
	}
	return d
}

func item(resource, name string) string { return "/api/v1/" + resource + "/" + url.PathEscape(name) }

// addressable refuses names the server's router would normalize away, so no
// request is sent for them.
func addressable(name string) error {
	if name == "." || name == ".." {
		return errors.New(`name cannot be "." or ".." over the API`)
	}
	return nil
}
func (c *Client) AddCheck(ctx context.Context, v config.Check) error {
	if err := addressable(v.Name); err != nil {
		return err
	}
	return c.write(ctx, "POST", "/api/v1/checks", checkDTO(v))
}

// AddCheckFrom creates v taking the header values and body it keeps from the
// server's monitor named source. The sealed values stay on the server.
func (c *Client) AddCheckFrom(ctx context.Context, source string, v config.Check) error {
	if err := addressable(v.Name); err != nil {
		return err
	}
	d := checkDTO(v)
	d.CopyFrom = source
	return c.write(ctx, "POST", "/api/v1/checks", d)
}
func (c *Client) UpdateCheck(ctx context.Context, v config.Check) error {
	if err := addressable(v.Name); err != nil {
		return err
	}
	return c.write(ctx, "PUT", item("checks", v.Name), checkDTO(v))
}
func (c *Client) DeleteCheck(ctx context.Context, n string) error {
	if err := addressable(n); err != nil {
		return err
	}
	return c.write(ctx, "DELETE", item("checks", n), nil)
}

// SetMute sends the remaining time rounded to the second: the TUI computes
// `until` a moment before the request, and without rounding its one-minute
// choice would arrive just under the server's one-minute minimum.
func (c *Client) SetMute(ctx context.Context, n string, until time.Time, alert bool) error {
	if err := addressable(n); err != nil {
		return err
	}
	return c.write(ctx, "PUT", item("checks", n)+"/mute", struct {
		DurationMS  int64 `json:"duration_ms"`
		WasAlerting bool  `json:"was_alerting"`
	}{time.Until(until).Round(time.Second).Milliseconds(), alert})
}
func (c *Client) Unmute(ctx context.Context, n string, _ time.Time) error {
	if err := addressable(n); err != nil {
		return err
	}
	return c.write(ctx, "DELETE", item("checks", n)+"/mute", nil)
}

type sealedWire struct {
	Key  []byte `json:"key"`
	Data []byte `json:"data"`
}
type smtpWrite struct {
	Name     string         `json:"name"`
	Host     string         `json:"host"`
	Port     int            `json:"port"`
	TLS      config.TLSMode `json:"tls"`
	Username string         `json:"username"`
	Sender   string         `json:"sender"`
	Password *sealedWire    `json:"password_sealed,omitempty"`
}

func smtpDTO(v config.SMTPServer) smtpWrite {
	d := smtpWrite{Name: v.Name, Host: v.Host, Port: v.Port, TLS: v.TLS, Username: v.Username, Sender: v.Sender}
	if !v.Password.Empty() {
		d.Password = &sealedWire{v.Password.Key, v.Password.Data}
	}
	return d
}
func (c *Client) AddSMTPServer(ctx context.Context, v config.SMTPServer) error {
	if err := addressable(v.Name); err != nil {
		return err
	}
	return c.write(ctx, "POST", "/api/v1/smtp-servers", smtpDTO(v))
}
func (c *Client) UpdateSMTPServer(ctx context.Context, v config.SMTPServer) error {
	if err := addressable(v.Name); err != nil {
		return err
	}
	return c.write(ctx, "PUT", item("smtp-servers", v.Name), smtpDTO(v))
}
func (c *Client) DeleteSMTPServer(ctx context.Context, n string) error {
	if err := addressable(n); err != nil {
		return err
	}
	return c.write(ctx, "DELETE", item("smtp-servers", n), nil)
}

// protectedWrite is the write form of a protected value; nil keeps it.
type protectedWrite struct {
	Action string      `json:"action"`
	Sealed *sealedWire `json:"sealed,omitempty"`
}

func protectedFor(edit config.Edit, v secret.Sealed) *protectedWrite {
	switch edit {
	case config.EditReplace:
		return &protectedWrite{Action: "replace", Sealed: &sealedWire{v.Key, v.Data}}
	case config.EditClear:
		return &protectedWrite{Action: "clear"}
	}
	return nil
}

// channelWrite is the body of a channel create or update.
type channelWrite struct {
	Name            string          `json:"name"`
	Kind            string          `json:"kind"`
	SMTPServer      string          `json:"smtp_server"`
	Recipients      []string        `json:"recipients"`
	WebhookOrigin   string          `json:"webhook_origin,omitempty"`
	WebhookURL      *protectedWrite `json:"webhook_url,omitempty"`
	WebhookAuthName string          `json:"webhook_auth_name,omitempty"`
	WebhookAuth     *protectedWrite `json:"webhook_auth,omitempty"`
}

func channelDTO(v config.Channel) channelWrite {
	return channelWrite{Name: v.Name, Kind: v.Kind, SMTPServer: v.SMTPServer, Recipients: v.Recipients, WebhookOrigin: v.WebhookOrigin,
		WebhookURL: protectedFor(v.WebhookURLEdit, v.WebhookURL), WebhookAuthName: v.WebhookAuthName, WebhookAuth: protectedFor(v.WebhookAuthEdit, v.WebhookAuth)}
}
func (c *Client) AddChannel(ctx context.Context, v config.Channel) error {
	if err := addressable(v.Name); err != nil {
		return err
	}
	return c.write(ctx, "POST", "/api/v1/channels", channelDTO(v))
}
func (c *Client) UpdateChannel(ctx context.Context, v config.Channel) error {
	if err := addressable(v.Name); err != nil {
		return err
	}
	return c.write(ctx, "PUT", item("channels", v.Name), channelDTO(v))
}
func (c *Client) DeleteChannel(ctx context.Context, n string) error {
	if err := addressable(n); err != nil {
		return err
	}
	return c.write(ctx, "DELETE", item("channels", n), nil)
}

var _ tui.MaintenanceStore = (*Client)(nil)
var _ tui.CheckCopier = (*Client)(nil)
var _ tui.ChannelTester = (*Client)(nil)
var _ tui.DefaultsStore = (*Client)(nil)

// SetDefaults replaces the server's defaults; they come back in the next
// snapshot.
func (c *Client) SetDefaults(ctx context.Context, d config.Defaults) error {
	dto := remote.DefaultsDTO{Zone: d.Zone, Channel: d.Channel, IntervalMS: d.Interval.Milliseconds(), TimeoutMS: d.Timeout.Milliseconds(),
		ExpectedStatus: d.ExpectedStatus, AlertAfter: d.AlertAfter, RecoverAfter: d.RecoverAfter}
	return c.write(ctx, "PUT", "/api/v1/defaults", dto)
}

// AddMaintenanceWindow creates a window and returns it with its ID.
func (c *Client) AddMaintenanceWindow(ctx context.Context, w config.MaintenanceWindow) (config.MaintenanceWindow, error) {
	if err := addressable(w.Check); err != nil {
		return config.MaintenanceWindow{}, err
	}
	status, b, err := c.request(ctx, "POST", item("checks", w.Check)+"/maintenance", maintenanceBody(w))
	if err != nil {
		return config.MaintenanceWindow{}, err
	}
	if status < 200 || status >= 300 {
		return config.MaintenanceWindow{}, c.responseError(status, b)
	}
	var d remote.MaintenanceDTO
	if json.Unmarshal(b, &d) != nil {
		return config.MaintenanceWindow{}, errors.New("client: invalid response")
	}
	win := d.Window(w.Check)
	win.Zone = clean(win.Zone)
	return win, nil
}

// UpdateMaintenanceWindow replaces the window with w.ID.
func (c *Client) UpdateMaintenanceWindow(ctx context.Context, w config.MaintenanceWindow) error {
	if err := addressable(w.Check); err != nil {
		return err
	}
	return c.write(ctx, "PUT", fmt.Sprintf("%s/maintenance/%d", item("checks", w.Check), w.ID), maintenanceBody(w))
}

// DeleteMaintenanceWindow removes window id of a check.
func (c *Client) DeleteMaintenanceWindow(ctx context.Context, check string, id int64) error {
	if err := addressable(check); err != nil {
		return err
	}
	return c.write(ctx, "DELETE", fmt.Sprintf("%s/maintenance/%d", item("checks", check), id), nil)
}

func maintenanceBody(w config.MaintenanceWindow) remote.MaintenanceWriteDTO {
	d := remote.MaintenanceWriteDTO{Weekdays: []int{}, StartMinute: w.StartMinute, DurationMS: w.Duration.Milliseconds(), Zone: w.Zone}
	for _, day := range w.Weekdays {
		d.Weekdays = append(d.Weekdays, int(day))
	}
	return d
}

// EnqueueChannelTest asks the server to test a channel; the time is ignored
// (the server stamps the request). The result arrives in later snapshots.
func (c *Client) EnqueueChannelTest(ctx context.Context, channel string, _ time.Time) (config.ChannelTest, error) {
	if err := addressable(channel); err != nil {
		return config.ChannelTest{}, err
	}
	status, b, err := c.request(ctx, "POST", item("channels", channel)+"/test", nil)
	if err != nil {
		return config.ChannelTest{}, err
	}
	if status < 200 || status >= 300 {
		return config.ChannelTest{}, c.responseError(status, b)
	}
	var d remote.ChannelTestDTO
	if json.Unmarshal(b, &d) != nil {
		return config.ChannelTest{}, errors.New("client: invalid response")
	}
	t := d.ChannelTest()
	t.Channel, t.Error = clean(t.Channel), clean(t.Error)
	return t, nil
}

// clean removes terminal escape sequences and control characters from text a
// server sent, so a compromised server cannot repaint the screen, move the
// cursor or reach the clipboard through a monitor name or an error message.
// Ordinary text, including any language, is left as it is.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, ansi.Strip(s))
}

// sanitize cleans every string reachable from v. It walks by reflection so a
// field added to the snapshot later is covered without remembering to list it.
func sanitize(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString(clean(v.String()))
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			sanitize(v.Field(i))
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			sanitize(v.Index(i))
		}
	case reflect.Pointer:
		if !v.IsNil() {
			sanitize(v.Elem())
		}
	}
}
