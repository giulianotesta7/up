package remote

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/giulianotesta7/up/internal/store"
)

// DeliveryReader reads alert delivery attempts. *store.Store implements it.
type DeliveryReader interface {
	Attempts(context.Context, store.AttemptFilter) ([]store.Attempt, error)
}

// DeliveryResponse is the body of GET /api/v1/deliveries. ServerTime lets the
// client correct for clock skew, as the snapshot does.
type DeliveryResponse struct {
	ServerTime time.Time    `json:"server_time"`
	Attempts   []AttemptDTO `json:"attempts"`
}

// DeliveryRoutes serves "/api/v1/deliveries":
//
//	GET  200 DeliveryResponse, newest first
//
// The optional query parameters monitor and channel filter, and limit bounds
// the list (a positive integer; the store caps it at store.MaxAttemptLimit
// and uses store.DefaultAttemptLimit without one). A bad limit is 400.
func DeliveryRoutes(s DeliveryReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		f := store.AttemptFilter{Monitor: q.Get("monitor"), Channel: q.Get("channel")}
		if raw := q.Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				writeError(w, http.StatusBadRequest, "limit must be a positive number")
				return
			}
			f.Limit = n
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		now := time.Now()
		list, err := s.Attempts(ctx, f)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not read the database")
			return
		}
		writeJSON(w, http.StatusOK, DeliveryResponse{ServerTime: now.UTC(), Attempts: attemptDTOs(list)})
	})
}
