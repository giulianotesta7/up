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

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/store"
	"github.com/heohlee/up/internal/tui"
)

type WriteStore interface {
	AddCheck(context.Context, config.Check) error
	UpdateCheck(context.Context, config.Check) error
	DeleteCheck(context.Context, string) error
	SetMute(context.Context, string, time.Time, bool) error
	Unmute(context.Context, string, time.Time) error
}

// checkCopier is the part of a store that adds a monitor taking the protected
// values it keeps from a stored one, without opening them.
type checkCopier interface {
	AddCheckFrom(ctx context.Context, source string, c config.Check) error
}

// createdCheck is the check as stored, so a copy's response reports which
// protected values are set. It falls back to c when the store cannot read.
func createdCheck(ctx context.Context, s any, c config.Check) config.Check {
	reader, ok := s.(interface {
		Checks(context.Context) ([]config.Check, error)
	})
	if !ok {
		return c
	}
	checks, err := reader.Checks(ctx)
	if err != nil {
		return c
	}
	for _, stored := range checks {
		if stored.Name == c.Name {
			return stored
		}
	}
	return c
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
	case d.PeriodMS > maxDurationMS || d.PeriodMS < -maxDurationMS:
		return config.Check{}, "period_ms is out of range", nil
	case d.GraceMS > maxDurationMS || d.GraceMS < -maxDurationMS:
		return config.Check{}, "grace_ms is out of range", nil
	case d.Name == "." || d.Name == "..":
		return config.Check{}, `name cannot be "." or ".." over the API`, nil
	}
	for i, cond := range d.Conditions {
		if cond.WithinMS > maxDurationMS || cond.WithinMS < -maxDurationMS {
			return config.Check{}, fmt.Sprintf("conditions[%d].within_ms is out of range", i), nil
		}
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
		Kind: d.Kind, Method: d.Method, ExpectText: d.ExpectText, Host: d.Host, Port: d.Port,
		Period: time.Duration(d.PeriodMS) * time.Millisecond, Grace: time.Duration(d.GraceMS) * time.Millisecond, Conditions: conditions(d.Conditions),
		Schedule: d.Schedule, Cron: d.Cron}
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
			// copy_from is optional: when set, the values this body keeps
			// (absent or "keep") come from that stored monitor.
			var raw struct {
				CheckDTO
				CopyFrom *string `json:"copy_from"`
			}
			if !DecodeJSON(w, r, &raw) {
				return
			}
			c, msg, err := raw.CheckDTO.checkOrError(r.Context(), s)
			if err != nil {
				writeStoreError(w, err)
				return
			}
			if msg != "" {
				writeError(w, http.StatusBadRequest, msg)
				return
			}
			switch copier, canCopy := s.(checkCopier); {
			case raw.CopyFrom == nil:
				if err := s.AddCheck(r.Context(), c); err != nil {
					writeStoreError(w, err)
					return
				}
				writeJSON(w, 201, checkDTO(c.Canonical()))
			case *raw.CopyFrom == "":
				writeError(w, http.StatusBadRequest, "copy_from must name a monitor")
			case !canCopy:
				writeError(w, http.StatusNotImplemented, "copying a monitor is not supported")
			default:
				if err := copier.AddCheckFrom(r.Context(), *raw.CopyFrom, c); err != nil {
					writeStoreError(w, err)
					return
				}
				writeJSON(w, 201, checkDTO(createdCheck(r.Context(), s, c).Canonical()))
			}
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
