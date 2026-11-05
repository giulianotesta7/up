package remote

import (
	"context"
	"net/http"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

// DefaultsDTO is the defaults on the wire, in both directions. Channel is
// empty for none. A write sends every field.
type DefaultsDTO struct {
	Zone           string `json:"zone"`
	Channel        string `json:"channel"`
	IntervalMS     int64  `json:"interval_ms"`
	TimeoutMS      int64  `json:"timeout_ms"`
	ExpectedStatus int    `json:"expected_status"`
	AlertAfter     int    `json:"alert_after"`
	RecoverAfter   int    `json:"recover_after"`
}

func defaultsDTO(d config.Defaults) DefaultsDTO {
	return DefaultsDTO{Zone: d.Zone, Channel: d.Channel, IntervalMS: d.Interval.Milliseconds(), TimeoutMS: d.Timeout.Milliseconds(),
		ExpectedStatus: d.ExpectedStatus, AlertAfter: d.AlertAfter, RecoverAfter: d.RecoverAfter}
}

func (d DefaultsDTO) defaults() config.Defaults {
	return config.Defaults{Zone: d.Zone, Channel: d.Channel, Interval: time.Duration(d.IntervalMS) * time.Millisecond, Timeout: time.Duration(d.TimeoutMS) * time.Millisecond,
		ExpectedStatus: d.ExpectedStatus, AlertAfter: d.AlertAfter, RecoverAfter: d.RecoverAfter}
}

// Defaults converts the wire form back.
func (d DefaultsDTO) Defaults() config.Defaults { return d.defaults() }

// DefaultsStore is what the defaults route needs.
type DefaultsStore interface {
	Defaults(context.Context) (config.Defaults, error)
	SetDefaults(context.Context, config.Defaults) error
}

// DefaultsRoutes serves "/api/v1/defaults":
//
//	GET  200 DefaultsDTO   the stored defaults, or the built-in ones
//	PUT  200 DefaultsDTO   body DefaultsDTO; replaces them
//
// Invalid input (including a channel that does not exist) is 400 and nothing
// changes. Saving never modifies existing monitors, channels or windows.
func DefaultsRoutes(s DefaultsStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/defaults" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		switch r.Method {
		case http.MethodGet:
		case http.MethodPut:
			var in DefaultsDTO
			if !DecodeJSON(w, r, &in) {
				return
			}
			switch {
			case in.IntervalMS > maxDurationMS || in.IntervalMS < -maxDurationMS:
				writeError(w, http.StatusBadRequest, "interval_ms is out of range")
				return
			case in.TimeoutMS > maxDurationMS || in.TimeoutMS < -maxDurationMS:
				writeError(w, http.StatusBadRequest, "timeout_ms is out of range")
				return
			}
			if err := s.SetDefaults(r.Context(), in.defaults()); err != nil {
				writeStoreError(w, err)
				return
			}
		default:
			w.Header().Set("Allow", "GET, PUT")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		d, err := s.Defaults(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, defaultsDTO(d))
	})
}
