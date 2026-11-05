package remote

import (
	"context"
	"net/http"
	"strconv"

	"github.com/giulianotesta7/up/internal/config"
)

// MaintenanceStore is what the maintenance window routes need.
type MaintenanceStore interface {
	MaintenanceWindows(ctx context.Context, check string) ([]config.MaintenanceWindow, error)
	AddMaintenanceWindow(context.Context, config.MaintenanceWindow) (config.MaintenanceWindow, error)
	UpdateMaintenanceWindow(context.Context, config.MaintenanceWindow) error
	DeleteMaintenanceWindow(ctx context.Context, check string, id int64) error
}

// MaintenanceRoutes serves a check's maintenance windows. Mount it on the
// patterns "/api/v1/checks/{name}/maintenance" and
// "/api/v1/checks/{name}/maintenance/{id}" (it reads both wildcards):
//
//	GET    .../maintenance        200 [MaintenanceDTO]   stored windows in creation order
//	POST   .../maintenance        201 MaintenanceDTO     body MaintenanceWriteDTO
//	PUT    .../maintenance/{id}   204                    body MaintenanceWriteDTO
//	DELETE .../maintenance/{id}   204
//
// Invalid input is 400, an unknown check or window 404, and nothing changes
// on any error. The routes only store and read; nothing is suppressed yet.
func MaintenanceRoutes(s MaintenanceStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		check := r.PathValue("name")
		if check == "" || check == "." || check == ".." {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		rawID := r.PathValue("id")
		if rawID == "" {
			maintenanceCollection(w, r, s, check)
			return
		}
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		switch r.Method {
		case http.MethodPut:
			win, ok := decodeWindow(w, r, check)
			if !ok {
				return
			}
			win.ID = id
			err = s.UpdateMaintenanceWindow(r.Context(), win)
		case http.MethodDelete:
			err = s.DeleteMaintenanceWindow(r.Context(), check, id)
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

func maintenanceCollection(w http.ResponseWriter, r *http.Request, s MaintenanceStore, check string) {
	switch r.Method {
	case http.MethodGet:
		windows, err := s.MaintenanceWindows(r.Context(), check)
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
		win, ok := decodeWindow(w, r, check)
		if !ok {
			return
		}
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

// decodeWindow reads a window body, refusing values that overflow a duration.
func decodeWindow(w http.ResponseWriter, r *http.Request, check string) (config.MaintenanceWindow, bool) {
	var in MaintenanceWriteDTO
	if !DecodeJSON(w, r, &in) {
		return config.MaintenanceWindow{}, false
	}
	if in.DurationMS > maxDurationMS || in.DurationMS < -maxDurationMS {
		writeError(w, http.StatusBadRequest, "duration_ms is out of range")
		return config.MaintenanceWindow{}, false
	}
	return MaintenanceDTO{Weekdays: in.Weekdays, StartMinute: in.StartMinute, DurationMS: in.DurationMS, Zone: in.Zone}.window(check), true
}
