package remote

import (
	"context"
	"net/http"
	"time"

	"github.com/heohlee/up/internal/config"
)

// ChannelTestStore is what the channel test routes need.
type ChannelTestStore interface {
	EnqueueChannelTest(ctx context.Context, channel string, at time.Time) (config.ChannelTest, error)
	LatestChannelTest(ctx context.Context, channel string) (config.ChannelTest, bool, error)
}

// ChannelTestRoutes serves channel test requests. Mount it on the pattern
// "/api/v1/channels/{name}/test" (it reads the wildcard):
//
//	POST  202 ChannelTestDTO   enqueue a pending test; 404 unknown channel,
//	                           409 when one is already pending or sending
//	GET   200 ChannelTestDTO   the channel's newest test; 404 when it never had one
//
// The routes only record and read requests; the daemon sends them. Results
// also arrive in the snapshot's channel_tests.
func ChannelTestRoutes(s ChannelTestStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if name == "" || name == "." || name == ".." {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		switch r.Method {
		case http.MethodPost:
			t, err := s.EnqueueChannelTest(r.Context(), name, time.Now())
			if err != nil {
				writeStoreError(w, err)
				return
			}
			writeJSON(w, http.StatusAccepted, channelTestDTO(t))
		case http.MethodGet:
			t, ok, err := s.LatestChannelTest(r.Context(), name)
			if err != nil {
				writeStoreError(w, err)
				return
			}
			if !ok {
				writeError(w, http.StatusNotFound, "no test for this channel")
				return
			}
			writeJSON(w, http.StatusOK, channelTestDTO(t))
		default:
			w.Header().Set("Allow", "GET, POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}
