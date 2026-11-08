package remote

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/tui"
)

// TagStore is what the tag routes need. *store.Store implements it.
type TagStore interface {
	SetTagMute(ctx context.Context, tag string, until time.Time) error
	UnmuteTag(ctx context.Context, tag string) error
	TagMaintenanceWindows(ctx context.Context, tag string) ([]config.MaintenanceWindow, error)
	AddMaintenanceWindow(context.Context, config.MaintenanceWindow) (config.MaintenanceWindow, error)
	UpdateMaintenanceWindow(context.Context, config.MaintenanceWindow) error
	DeleteTagMaintenanceWindow(ctx context.Context, tag string, id int64) error
}

// TagPatterns are the patterns to mount TagRoutes on.
var TagPatterns = []string{"/api/v1/tags/{tag}/mute", "/api/v1/tags/{tag}/maintenance", "/api/v1/tags/{tag}/maintenance/{id}"}

// TagRoutes serves the mute and maintenance windows of a tag, which hold
// every monitor carrying the tag when evaluated. Mount it on TagPatterns (it
// reads the wildcards):
//
//	PUT    /api/v1/tags/{tag}/mute               204  body {"duration_ms"} (1 minute to 7 days)
//	DELETE /api/v1/tags/{tag}/mute               204  unmutes the tag
//	GET    /api/v1/tags/{tag}/maintenance        200  [MaintenanceDTO] in creation order
//	POST   /api/v1/tags/{tag}/maintenance        201  MaintenanceDTO, body MaintenanceWriteDTO
//	PUT    /api/v1/tags/{tag}/maintenance/{id}   204  body MaintenanceWriteDTO
//	DELETE /api/v1/tags/{tag}/maintenance/{id}   204
//
// The tag is normalized ("NAS" is "nas"). An invalid tag or body is 400, a
// tag no monitor carries or an unknown window 404, and nothing changes on any
// error.
func TagRoutes(s TagStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tag := r.PathValue("tag")
		if r.Pattern == TagPatterns[0] {
			tagMute(w, r, s, tag)
			return
		}
		rawID := r.PathValue("id")
		if rawID == "" {
			tagMaintenanceCollection(w, r, s, tag)
			return
		}
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		switch r.Method {
		case http.MethodPut:
			win, ok := decodeWindow(w, r, "")
			if !ok {
				return
			}
			win.ID, win.Tag = id, tag
			err = s.UpdateMaintenanceWindow(r.Context(), win)
		case http.MethodDelete:
			err = s.DeleteTagMaintenanceWindow(r.Context(), tag, id)
		default:
			w.Header().Set("Allow", "PUT, DELETE")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func tagMute(w http.ResponseWriter, r *http.Request, s TagStore, tag string) {
	var err error
	switch r.Method {
	case http.MethodPut:
		var d struct {
			DurationMS int64 `json:"duration_ms"`
		}
		if !DecodeJSON(w, r, &d) {
			return
		}
		if d.DurationMS <= 0 {
			writeError(w, http.StatusBadRequest, "duration_ms is required")
			return
		}
		if d.DurationMS < tui.MinMute.Milliseconds() || d.DurationMS > tui.MaxMute.Milliseconds() {
			writeError(w, http.StatusBadRequest, "duration must be between 1m and 7d")
			return
		}
		err = s.SetTagMute(r.Context(), tag, time.Now().Add(time.Duration(d.DurationMS)*time.Millisecond))
	case http.MethodDelete:
		err = s.UnmuteTag(r.Context(), tag)
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func tagMaintenanceCollection(w http.ResponseWriter, r *http.Request, s TagStore, tag string) {
	switch r.Method {
	case http.MethodGet:
		windows, err := s.TagMaintenanceWindows(r.Context(), tag)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		out := []MaintenanceDTO{}
		for _, win := range windows {
			out = append(out, maintenanceDTO(win))
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		win, ok := decodeWindow(w, r, "")
		if !ok {
			return
		}
		win.Tag = tag
		added, err := s.AddMaintenanceWindow(r.Context(), win)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, maintenanceDTO(added))
	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
