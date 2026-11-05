package remote

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
	"github.com/giulianotesta7/up/internal/tui"
)

type WriteStore interface {
	AddCheck(context.Context, config.Check) error
	UpdateCheck(context.Context, config.Check) error
	DeleteCheck(context.Context, string) error
	SetMute(context.Context, string, time.Time, bool) error
	Unmute(context.Context, string, time.Time) error
}

func writeStoreError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrValidation):
		status = http.StatusBadRequest
	case errors.Is(err, store.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, store.ErrExists):
		status = http.StatusConflict
	default:
		var inUse *store.InUseError
		if errors.As(err, &inUse) {
			status = http.StatusConflict
		}
	}
	if status == http.StatusInternalServerError {
		writeError(w, status, "internal error")
	} else {
		writeError(w, status, err.Error())
	}
}

// maxDurationMS keeps the millisecond to time.Duration conversion from
// overflowing in either direction: a huge negative value would otherwise wrap
// around to a valid positive duration.
const maxDurationMS = math.MaxInt64 / int64(time.Millisecond)

// checkOrError converts the wire form, refusing values that would overflow a
// duration and names the router cannot address. The returned message is a
// client error (400); err is a server error (500). Protected values are
// checked against the server key when s can supply it (every real store can).
func (d CheckDTO) checkOrError(ctx context.Context, s any) (c config.Check, msg string, err error) {
	switch {
	case d.IntervalMS > maxDurationMS || d.IntervalMS < -maxDurationMS:
		return config.Check{}, "interval_ms is out of range", nil
	case d.TimeoutMS > maxDurationMS || d.TimeoutMS < -maxDurationMS:
		return config.Check{}, "timeout_ms is out of range", nil
	case d.Name == "." || d.Name == "..":
		return config.Check{}, `name cannot be "." or ".." over the API`, nil
	}
	keys, _ := s.(keyReader)
	c = d.check()
	if c.Body, c.BodyEdit, msg, err = protectedEdit(ctx, keys, d.Body, "body", config.MaxSealedBody); msg != "" || err != nil {
		return config.Check{}, msg, err
	}
	c.Headers = make([]config.Header, len(d.Headers))
	for i, h := range d.Headers {
		out := config.Header{Name: h.Name}
		if out.Value, out.Edit, msg, err = protectedEdit(ctx, keys, h.Value, fmt.Sprintf("headers[%d].value", i), config.MaxSealedHeaderValue); msg != "" || err != nil {
			return config.Check{}, msg, err
		}
		c.Headers[i] = out
	}
	return c, "", nil
}

// check converts the plain fields; protected values are filled by checkOrError.
func (d CheckDTO) check() config.Check {
	return config.Check{Name: d.Name, URL: d.URL, Interval: time.Duration(d.IntervalMS) * time.Millisecond, Timeout: time.Duration(d.TimeoutMS) * time.Millisecond, ExpectedStatus: d.ExpectedStatus, Channel: d.Channel, AlertAfter: d.AlertAfter, RecoverAfter: d.RecoverAfter, TLSOff: d.TLSOff,
		Kind: d.Kind, Method: d.Method, ExpectText: d.ExpectText, Host: d.Host, Port: d.Port}
}

// WriteRoutes handles check and mute mutations after authentication.
func WriteRoutes(s WriteStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		if path == "/api/v1/checks" {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", "POST")
				w.WriteHeader(405)
				return
			}
			var d CheckDTO
			if !DecodeJSON(w, r, &d) {
				return
			}
			c, msg, err := d.checkOrError(r.Context(), s)
			if err != nil {
				writeStoreError(w, err)
				return
			}
			if msg != "" {
				writeError(w, http.StatusBadRequest, msg)
				return
			}
			if err := s.AddCheck(r.Context(), c); err != nil {
				writeStoreError(w, err)
				return
			}
			writeJSON(w, 201, checkDTO(c.Canonical()))
			return
		}
		rest, ok := strings.CutPrefix(path, "/api/v1/checks/")
		if !ok {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		parts := strings.Split(rest, "/")
		if len(parts) < 1 || len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] != "mute") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		name, err := url.PathUnescape(parts[0])
		if err != nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if len(parts) == 2 {
			switch r.Method {
			case http.MethodPut:
				var d struct {
					DurationMS  int64 `json:"duration_ms"`
					WasAlerting bool  `json:"was_alerting"`
				}
				if !DecodeJSON(w, r, &d) {
					return
				}
				if d.DurationMS <= 0 {
					writeError(w, 400, "duration_ms is required")
					return
				}
				if d.DurationMS < tui.MinMute.Milliseconds() || d.DurationMS > tui.MaxMute.Milliseconds() {
					writeError(w, 400, "duration must be between 1m and 7d")
					return
				}
				err = s.SetMute(r.Context(), name, time.Now().Add(time.Duration(d.DurationMS)*time.Millisecond), d.WasAlerting)
			case http.MethodDelete:
				err = s.Unmute(r.Context(), name, time.Now())
			default:
				w.Header().Set("Allow", "PUT, DELETE")
				w.WriteHeader(405)
				return
			}
		} else {
			switch r.Method {
			case http.MethodPut:
				var raw struct {
					CheckDTO
					Name *string `json:"name"`
				}
				if !DecodeJSON(w, r, &raw) {
					return
				}
				if raw.Name != nil && *raw.Name != name {
					writeError(w, 400, "name must match path")
					return
				}
				raw.CheckDTO.Name = name
				c, msg, e := raw.CheckDTO.checkOrError(r.Context(), s)
				if e != nil {
					writeStoreError(w, e)
					return
				}
				if msg != "" {
					writeError(w, http.StatusBadRequest, msg)
					return
				}
				err = s.UpdateCheck(r.Context(), c)
			case http.MethodDelete:
				err = s.DeleteCheck(r.Context(), name)
			default:
				w.Header().Set("Allow", "PUT, DELETE")
				w.WriteHeader(405)
				return
			}
		}
		if err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
