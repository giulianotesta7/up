package remote

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
	"github.com/heohlee/up/internal/store"
)

// HeartbeatPrefix is the path of heartbeat pings. They are served without the
// API access token: the ping token in the path is the only credential, and
// it grants nothing but recording a ping or a failure for one monitor.
const HeartbeatPrefix = "/api/v1/heartbeat/"

// MaxPingBody caps a ping's body, which is read and ignored, so a job can
// send `curl --data` output without being refused.
const MaxPingBody = 1 << 10

// PingStore looks up and records heartbeat pings. *store.Store implements
// it.
type PingStore interface {
	HeartbeatTokenKnown(ctx context.Context, token string) (bool, error)
	PingHeartbeat(ctx context.Context, token string, failed bool, at time.Time) (bool, error)
}

// PingLimits are token buckets (rate per second, burst) for the ping
// endpoint. The token is looked up first, so traffic without a valid token
// cannot block valid pings:
//
//   - Cap bounds every request that reaches the token lookup, protecting
//     the database.
//   - Token is per presented token, valid or not (a ping and its /fail
//     share one), so many jobs behind one proxy that fire at the same
//     minute are not throttled together.
//   - IP (per client address) and Unknown (overall) apply only to tokens
//     that match no heartbeat, bounding guessing; a valid token never
//     spends or meets them.
//
// The client address is the connection's: forwarded headers are not
// trusted.
type PingLimits struct {
	TokenRate, IPRate, UnknownRate, CapRate     float64
	TokenBurst, IPBurst, UnknownBurst, CapBurst int
}

// DefaultPingLimits: 1/s burst 5 per token; for unknown tokens 20/s burst
// 100 per client address and 50/s burst 200 overall; 500/s burst 1000 for
// all lookups.
var DefaultPingLimits = PingLimits{TokenRate: 1, TokenBurst: 5, IPRate: 20, IPBurst: 100, UnknownRate: 50, UnknownBurst: 200, CapRate: 500, CapBurst: 1000}

// maxLimiterKeys bounds the per-token and per-address bucket maps.
const maxLimiterKeys = 10000

// PingOptions configures HeartbeatRoutes.
type PingOptions struct {
	Limits PingLimits
	// Now stamps pings and refills the buckets; nil is time.Now.
	Now func() time.Time
}

// HeartbeatRoutes serves POST /api/v1/heartbeat/{token} (the job finished)
// and POST /api/v1/heartbeat/{token}/fail (it failed). Answers:
//
//   - 200 {"ok":true}: recorded.
//   - 404 {"error":"not found"}: any token that matches no heartbeat
//     (unknown, malformed, former, missing) and any other path below the
//     prefix, all byte-identical, so a caller cannot tell them apart.
//   - 405 (Allow: POST) for other methods and 413 for a body over
//     MaxPingBody, decided before the token is looked at.
//   - 429 {"error":"too many requests"} (Retry-After: 1) over a limit (see
//     PingLimits), identical for every limit.
//   - 500 {"error":"internal error"} when the database fails.
//
// Unknown, malformed and former tokens take the same path, so the limits
// they meet do not tell them apart either; only a valid token, which already
// answers 200, skips the unknown-token buckets. The token never appears in a
// response or a log.
func HeartbeatRoutes(s PingStore, o PingOptions) http.Handler {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	l := o.Limits
	limitCap := newLimiter(l.CapRate, l.CapBurst, 1)
	unknown := newLimiter(l.UnknownRate, l.UnknownBurst, 1)
	perIP := newLimiter(l.IPRate, l.IPBurst, maxLimiterKeys)
	perToken := newLimiter(l.TokenRate, l.TokenBurst, maxLimiterKeys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodPost {
			h.Set("Allow", "POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.ContentLength > MaxPingBody {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		if _, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, MaxPingBody)); err != nil {
			writeBodyError(w, err)
			return
		}
		// Stamped once the body is in, when the job is done sending.
		at := now()
		if !limitCap.allow("", at) {
			tooMany(w)
			return
		}
		token, failed, ok := pingPath(r.URL.EscapedPath())
		known := false
		if ok {
			var err error
			if known, err = s.HeartbeatTokenKnown(r.Context(), token); err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
		}
		// The bucket is keyed by what was presented, so an unknown token
		// is limited exactly like a real one.
		presented := token
		if !ok {
			presented = r.URL.EscapedPath()
		}
		sum := sha256.Sum256([]byte(presented))
		if !perToken.allow(string(sum[:]), at) {
			tooMany(w)
			return
		}
		if !known {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
			if !perIP.allow(ip, at) || !unknown.allow("", at) {
				tooMany(w)
				return
			}
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		found, err := s.PingHeartbeat(r.Context(), token, failed, at)
		switch {
		case err != nil:
			writeError(w, http.StatusInternalServerError, "internal error")
		case !found:
			writeError(w, http.StatusNotFound, "not found")
		default:
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		}
	})
}

// pingPath splits an escaped ping path into its token and whether it reports
// a failure. Anything but {token} or {token}/fail is not ok.
func pingPath(path string) (token string, failed, ok bool) {
	rest, found := strings.CutPrefix(path, HeartbeatPrefix)
	if !found {
		return "", false, false
	}
	token, action, hasAction := strings.Cut(rest, "/")
	if token == "" || (hasAction && action != strings.TrimPrefix(config.HeartbeatFailSuffix, "/")) {
		return "", false, false
	}
	return token, hasAction, true
}

func tooMany(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	writeError(w, http.StatusTooManyRequests, "too many requests")
}

// limiter is a set of token buckets by key.
type limiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	max     int
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(rate float64, burst, max int) *limiter {
	return &limiter{rate: rate, burst: float64(burst), max: max, buckets: map[string]*bucket{}}
}

// allow takes one token from key's bucket at now. When the map is full it
// first forgets buckets that refilled completely (idle keys); if none did,
// new keys are refused until some do.
func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= l.max {
			for k, old := range l.buckets {
				if l.refill(old, now) >= l.burst {
					delete(l.buckets, k)
				}
			}
			if len(l.buckets) >= l.max {
				return false
			}
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens, b.last = l.refill(b, now), now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *limiter) refill(b *bucket, now time.Time) float64 {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	return min(l.burst, b.tokens+elapsed*l.rate)
}

// HeartbeatTokenStore reads and replaces ping tokens. *store.Store
// implements it.
type HeartbeatTokenStore interface {
	HeartbeatToken(ctx context.Context, name string) (secret.Sealed, error)
	RegenerateHeartbeatToken(ctx context.Context, name string) (string, error)
}

// HeartbeatTokenDTO is a heartbeat's ping token and the path pings use. Only
// the authenticated token route returns it.
type HeartbeatTokenDTO struct {
	Token string `json:"token"`
	Path  string `json:"path"`
}

// HeartbeatTokenRoutes serves /api/v1/checks/{name}/heartbeat-token after
// authentication: GET opens the stored token with the daemon key, POST
// replaces it (the old one stops working at once). Both answer 200 with a
// HeartbeatTokenDTO, 404 for a monitor that is missing or not a heartbeat
// and 409 when the token was sealed to another key.
func HeartbeatTokenRoutes(s HeartbeatTokenStore, key *secret.Key) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		var token string
		var err error
		switch r.Method {
		case http.MethodGet:
			var sealed secret.Sealed
			if sealed, err = s.HeartbeatToken(r.Context(), name); err == nil {
				token, err = openToken(key, sealed)
			}
		case http.MethodPost:
			token, err = s.RegenerateHeartbeatToken(r.Context(), name)
		default:
			w.Header().Set("Allow", "GET, POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if errors.Is(err, errOtherKey) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, HeartbeatTokenDTO{Token: token, Path: config.HeartbeatPath(token)})
	})
}

var errOtherKey = errors.New("the token was sealed with another encryption key; replace the token")

func openToken(key *secret.Key, sealed secret.Sealed) (string, error) {
	if key == nil {
		return "", errOtherKey
	}
	token, err := key.Open(sealed)
	if err != nil {
		return "", errOtherKey
	}
	return token, nil
}

var _ PingStore = (*store.Store)(nil)
var _ HeartbeatTokenStore = (*store.Store)(nil)
