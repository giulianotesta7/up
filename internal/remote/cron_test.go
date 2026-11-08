package remote

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/tui"
)

func TestCronHeartbeatThroughTheAPIAndSnapshot(t *testing.T) {
	h, s, key := tokenServer(t)
	ctx := context.Background()
	body := `{"name":"nightly","kind":"heartbeat","schedule":"cron","cron":"0 2 * * 1-5","grace_ms":1800000}`
	rec := send(h, "POST", "/api/v1/checks", body)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"schedule":"cron","cron":"0 2 * * 1-5"`) {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	want := config.Check{Name: "nightly", Kind: config.CheckHeartbeat, Schedule: config.ScheduleCron, Cron: "0 2 * * 1-5", Grace: 30 * time.Minute}
	if got := storedCheck(t, s, "nightly"); !reflect.DeepEqual(got, want) {
		t.Fatalf("stored %+v, want %+v", got, want)
	}

	// An invalid expression is a 400 with the reason, and nothing is stored.
	before, err := s.Checks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for bad, reason := range map[string]string{
		`{"name":"x","kind":"heartbeat","schedule":"cron","cron":"0 25 * * *"}`:                                                   `cron "0 25 * * *" is not valid: hour 25 is out of range 0-23`,
		`{"name":"x","kind":"heartbeat","schedule":"cron"}`:                                                                       "cron is required",
		`{"name":"x","kind":"heartbeat","schedule":"hourly","period_ms":3600000}`:                                                 `schedule "hourly" must be period or cron`,
		`{"name":"x","kind":"heartbeat","schedule":"cron","cron":"0 2 * * *","period_ms":3600000}`:                                "period is not used by cron heartbeats",
		`{"name":"x","url":"https://example.com","interval_ms":60000,"timeout_ms":1000,"cron":"* * * * *","expected_status":200}`: "schedule and cron are only for heartbeat checks",
	} {
		rec := send(h, "POST", "/api/v1/checks", bad)
		var e struct{ Error string }
		if err := json.Unmarshal(rec.Body.Bytes(), &e); rec.Code != 400 || err != nil || !strings.Contains(e.Error, reason) {
			t.Errorf("create %s = %d %s, want 400 with %q", bad, rec.Code, rec.Body, reason)
		}
	}
	if rec := send(h, "PUT", "/api/v1/checks/nightly", `{"kind":"heartbeat","schedule":"cron","cron":"0 2 31 2 *"}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), `it never runs`) {
		t.Fatalf("update with an invalid cron = %d %s", rec.Code, rec.Body)
	}
	if after, err := s.Checks(ctx); err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("checks after rejected writes = %+v, %v, want %+v", after, err, before)
	}

	// The period heartbeat "backup" switches to cron on edit and keeps its
	// token; a period one reads schedule "period".
	if rec := send(h, "POST", "/api/v1/checks", `{"name":"hourly","kind":"heartbeat","period_ms":3600000}`); rec.Code != 201 || !strings.Contains(rec.Body.String(), `"schedule":"period","cron":""`) {
		t.Fatalf("create a period heartbeat = %d %s", rec.Code, rec.Body)
	}
	token := beatToken(t, s, key, "backup")
	if rec := send(h, "PUT", "/api/v1/checks/backup", `{"kind":"heartbeat","schedule":"cron","cron":"@daily","grace_ms":60000}`); rec.Code != 204 {
		t.Fatalf("switch to cron = %d %s", rec.Code, rec.Body)
	}
	if got := beatToken(t, s, key, "backup"); got != token {
		t.Fatal("switching to cron over the API replaced the token")
	}

	// The snapshot carries the schedule; the deadline, the next runs and the
	// cells per run come out the same on both sides of the wire.
	d := config.BuiltinDefaults()
	d.Zone = "America/Argentina/Buenos_Aires"
	if err := s.SetDefaults(ctx, d); err != nil {
		t.Fatal(err)
	}
	nightly := beatToken(t, s, key, "nightly")
	monday := time.Date(2030, 1, 7, 5, 1, 0, 0, time.UTC) // 02:01 -03
	for _, at := range []time.Time{monday, monday.Add(time.Minute)} {
		if found, err := s.PingHeartbeat(ctx, nightly, false, at); err != nil || !found {
			t.Fatal(found, err)
		}
	}
	now := monday.Add(time.Hour)
	snap, err := tui.Load(ctx, s, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(NewSnapshotDTO(snap, now))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"deadline":"2030-01-08T05:30:00Z"`) {
		t.Fatalf("snapshot JSON lacks the cron deadline:\n%s", b)
	}
	var dto SnapshotDTO
	if err := json.Unmarshal(b, &dto); err != nil {
		t.Fatal(err)
	}
	back := dto.Snapshot()
	local, remote := snap.Services[2], back.Services[2]
	if !reflect.DeepEqual(remote.Check, want) || remote.Heartbeat != local.Heartbeat {
		t.Fatalf("round trip = %+v / %+v, want %+v / %+v", remote.Check, remote.Heartbeat, want, local.Heartbeat)
	}
	wantRuns := []time.Time{time.Date(2030, 1, 8, 5, 0, 0, 0, time.UTC), time.Date(2030, 1, 9, 5, 0, 0, 0, time.UTC), time.Date(2030, 1, 10, 5, 0, 0, 0, time.UTC)}
	for name, got := range map[string][]time.Time{"local": snap.NextRuns(local.Check, now, 3), "remote": back.NextRuns(remote.Check, now, 3)} {
		if len(got) != 3 {
			t.Fatalf("%s NextRuns() = %v", name, got)
		}
		for i := range got {
			if !got[i].Equal(wantRuns[i]) {
				t.Fatalf("%s NextRuns() = %v, want %v", name, got, wantRuns)
			}
		}
	}
	if got := back.NextRuns(back.Services[3].Check, now, 3); got != nil {
		t.Fatalf("NextRuns(period) = %v, want none", got)
	}
	cells := check.RunResults(remote.Check, remote.Recent, back.ScheduleZone())
	if len(cells) != 1 || !cells[0].At.Equal(monday.Add(time.Minute)) {
		t.Fatalf("cells per run = %+v, want the newest of Monday's two pings", cells)
	}
}
