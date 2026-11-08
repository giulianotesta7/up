package store

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/giulianotesta7/up/internal/config"
)

// v9Database writes a database as the previous release left it (migrations 0
// to 8) with rows in every table that has user data, an email and a webhook
// channel among them.
func v9Database(t *testing.T) string {
	t.Helper()
	path := v8Database(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(migrations[8]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 9;
INSERT INTO channels (name, kind, smtp_server, recipients, webhook_origin, webhook_url, webhook_url_key, webhook_auth_name, webhook_auth, webhook_auth_key) VALUES ('hook', 'webhook', '', '', 'https://hooks.example.com', x'01', x'07', 'Authorization', x'02', x'07');
INSERT INTO daemon_api (id, listen, tls) VALUES (1, ':8080', 0);`); err != nil {
		t.Fatal(err)
	}
	return path
}

// v9Tables are the tables whose rows and columns v10 and v11 leave alone;
// checks gains a column in v11 and v9Checks compares its earlier columns.
var v9Tables = []string{"smtp_servers", "results", "mutes", "alert_state", "defaults", "daemon", "daemon_api", "certs", "maintenance_windows", "channel_tests", "secret_key", "delivery_attempts"}

// v9Channels dumps the channels columns that schema v9 has.
func v9Channels(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT quote(name) || '|' || quote(kind) || '|' || quote(smtp_server) || '|' || quote(recipients) || '|' || quote(webhook_origin) || '|' || quote(webhook_url) || '|' || quote(webhook_url_key) || '|' || quote(webhook_auth_name) || '|' || quote(webhook_auth) || '|' || quote(webhook_auth_key) FROM channels ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	return out
}

func TestOpenMigratesV9AddingOnlyPushFields(t *testing.T) {
	path := v9Database(t)
	before, beforeChecks, beforeChannels := dumpTables(t, path, v9Tables...), v9Checks(t, path), v9Channels(t, path)
	if _, err := OpenReadOnly(path); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("database schema version 9 is older than this up (%d)", schemaVersion)) {
		t.Fatalf("OpenReadOnly(v9) error = %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(v9) error = %v", err)
	}
	defer s.Close()
	if got := version(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}
	if after := dumpTables(t, path, v9Tables...); !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed existing rows:\nbefore %v\nafter  %v", before, after)
	}
	if after := v9Checks(t, path); !reflect.DeepEqual(beforeChecks, after) {
		t.Fatalf("migration changed existing checks:\nbefore %v\nafter  %v", beforeChecks, after)
	}
	if after := v9Channels(t, path); !reflect.DeepEqual(beforeChannels, after) {
		t.Fatalf("migration changed existing channels:\nbefore %v\nafter  %v", beforeChannels, after)
	}
	ctx := context.Background()
	channels, err := s.Channels(ctx)
	if err != nil || len(channels) != 2 {
		t.Fatalf("Channels() after migration = %+v, %v", channels, err)
	}
	for _, c := range channels {
		if c.ServerURL != "" || c.Topic != "" || c.ChatID != "" || !c.Token.Empty() || c.Priority != "" {
			t.Fatalf("migrated channel has push fields: %+v", c)
		}
	}
	if channels[0].Kind != config.KindWebhook || channels[0].WebhookOrigin != "https://hooks.example.com" || channels[1].Kind != config.KindEmail || channels[1].SMTPServer != "smtp" {
		t.Fatalf("migrated channels = %+v", channels)
	}
	phone := config.Channel{Name: "phone", Kind: config.KindNtfy, ServerURL: config.DefaultNtfyServer, Topic: "up-alerts"}
	if err := s.AddChannel(ctx, phone); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly after upgrade: %v", err)
	}
	defer ro.Close()
	if got, ok, err := ro.Channel(ctx, "phone"); err != nil || !ok || got.Topic != "up-alerts" {
		t.Fatalf("Channel(phone) after reopen = %+v, %v, %v", got, ok, err)
	}
}

// A v10 migration that fails part way (its last column name is taken) rolls
// back as a whole: the database stays v9 with its rows and no new columns.
func TestOpenV9FailingMidMigrationChangesNothing(t *testing.T) {
	path := v9Database(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE channels ADD COLUMN priority TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, beforeChecks, beforeChannels := dumpTables(t, path, v9Tables...), v9Checks(t, path), v9Channels(t, path)
	if s, err := Open(path); err == nil || !strings.Contains(err.Error(), "migrate schema 9 to 10") {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open(v9 with a clashing column) error = %v, want the 9 to 10 migration to fail", err)
	}
	if got := version(t, path); got != 9 {
		t.Fatalf("user_version = %d, want 9", got)
	}
	if after := dumpTables(t, path, v9Tables...); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed migration changed rows:\nbefore %v\nafter  %v", before, after)
	}
	if after := v9Checks(t, path); !reflect.DeepEqual(beforeChecks, after) {
		t.Fatalf("failed migration changed checks:\nbefore %v\nafter  %v", beforeChecks, after)
	}
	if after := v9Channels(t, path); !reflect.DeepEqual(beforeChannels, after) {
		t.Fatalf("failed migration changed channels:\nbefore %v\nafter  %v", beforeChannels, after)
	}
	ro, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.Exec("SELECT server_url FROM channels"); err == nil {
		t.Fatal("channels.server_url exists after a failed migration")
	}
}
