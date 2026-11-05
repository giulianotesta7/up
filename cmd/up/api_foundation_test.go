package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/giulianotesta7/up/internal/remote"
	"github.com/giulianotesta7/up/internal/secret"
)

// The serve command mounts the foundation routes next to the existing ones.
func TestServeMountsMaintenanceAndChannelTestRoutes(t *testing.T) {
	call := startTestAPI(t)
	status, body := call("GET", "/api/v1/snapshot", "")
	var snap remote.SnapshotDTO
	if status != 200 || json.Unmarshal([]byte(body), &snap) != nil || !snap.HasKey {
		t.Fatalf("snapshot: %d %s", status, body)
	}
	sealed, err := secret.Seal(snap.PublicKey, "https://hooks.example.com/services/SECRET")
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString
	steps := []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/api/v1/checks", `{"name":"api","url":"https://example.com","interval_ms":3600000,"timeout_ms":1000,"expected_status":200,"method":"HEAD"}`, 201},
		{"POST", "/api/v1/channels", `{"name":"hook","kind":"webhook","webhook_origin":"https://hooks.example.com","webhook_url":{"action":"replace","sealed":{"key":"` + b64(sealed.Key) + `","data":"` + b64(sealed.Data) + `"}}}`, 201},
		{"POST", "/api/v1/checks/api/maintenance", `{"weekdays":[1],"start_minute":60,"duration_ms":3600000,"zone":"UTC"}`, 201},
		{"GET", "/api/v1/checks/api/maintenance", "", 200},
		{"POST", "/api/v1/channels/hook/test", "", 202},
		{"POST", "/api/v1/channels/hook/test", "", 409},
		{"GET", "/api/v1/channels/hook/test", "", 200},
		{"POST", "/api/v1/checks/ghost/maintenance", `{"weekdays":[1],"duration_ms":3600000,"zone":"UTC"}`, 404},
	}
	for _, step := range steps {
		if got, out := call(step.method, step.path, step.body); got != step.want {
			t.Fatalf("%s %s = %d %s, want %d", step.method, step.path, got, out, step.want)
		}
	}
	_, body = call("GET", "/api/v1/snapshot", "")
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Services[0].Maintenance) != 1 || len(snap.ChannelTests) != 1 || snap.ChannelTests[0].Status != "pending" || snap.Services[0].Check.Method != "HEAD" {
		t.Fatalf("snapshot after writes: %s", body)
	}
	if strings.Contains(body, "SECRET") || strings.Contains(body, b64(sealed.Data)) {
		t.Fatalf("snapshot leaks the webhook URL: %s", body)
	}
}
