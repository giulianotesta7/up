package main

import (
	"encoding/json"
	"testing"

	"github.com/heohlee/up/internal/remote"
)

// The serve command mounts the tag routes behind the access token.
func TestServeMountsTagRoutes(t *testing.T) {
	call := startTestAPI(t)
	steps := []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/api/v1/checks", `{"name":"api","url":"https://example.com","interval_ms":3600000,"timeout_ms":1000,"expected_status":200,"tags":["NAS"]}`, 201},
		{"PUT", "/api/v1/tags/nas/mute", `{"duration_ms":3600000}`, 204},
		{"POST", "/api/v1/tags/nas/maintenance", `{"weekdays":[1],"start_minute":60,"duration_ms":3600000,"zone":"UTC"}`, 201},
		{"GET", "/api/v1/tags/nas/maintenance", "", 200},
		{"PUT", "/api/v1/tags/bad%20tag/mute", `{"duration_ms":3600000}`, 400},
		{"PUT", "/api/v1/tags/ghost/mute", `{"duration_ms":3600000}`, 404},
	}
	for _, step := range steps {
		if got, out := call(step.method, step.path, step.body); got != step.want {
			t.Fatalf("%s %s = %d %s, want %d", step.method, step.path, got, out, step.want)
		}
	}
	var snap remote.SnapshotDTO
	_, body := call("GET", "/api/v1/snapshot", "")
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		t.Fatal(err)
	}
	svc := snap.Services[0]
	if len(svc.Check.Tags) != 1 || svc.Check.Tags[0] != "nas" || svc.Mute.Tag != "nas" || len(svc.Maintenance) != 1 || svc.Maintenance[0].Tag != "nas" {
		t.Fatalf("snapshot after tag writes: %s", body)
	}
}
