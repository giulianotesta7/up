package check

import (
	"reflect"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

var beat = config.Check{Name: "backup", Kind: config.CheckHeartbeat, Period: time.Hour, Grace: 10 * time.Minute}

func TestHeartbeatResults(t *testing.T) {
	at := time.Date(2026, 11, 6, 2, 14, 0, 0, time.UTC)
	if got, want := Ping("backup", at), (Result{CheckName: "backup", At: at, Status: StatusUp}); got != want {
		t.Fatalf("Ping() = %+v, want %+v", got, want)
	}
	failed := Fail("backup", at)
	if want := (Result{CheckName: "backup", At: at, Status: StatusDown, Error: "reported failure"}); failed != want {
		t.Fatalf("Fail() = %+v, want %+v", failed, want)
	}
	if !Failed(failed) || Missed(failed) {
		t.Fatal("a reported failure is not classified as failed")
	}
	if Failed(Ping("backup", at)) || Missed(Ping("backup", at)) {
		t.Fatal("a ping is classified as a failure")
	}
	if got := NextDeadline(beat, at); !got.Equal(at.Add(70 * time.Minute)) {
		t.Fatalf("NextDeadline() = %v", got)
	}
}

func TestOverdue(t *testing.T) {
	ping := time.Date(2026, 11, 6, 2, 14, 0, 0, time.UTC)
	deadline := ping.Add(70 * time.Minute)
	missed := func(at time.Time, reason string) Result {
		return Result{CheckName: "backup", At: at, Status: StatusDown, Error: reason}
	}
	tests := []struct {
		name          string
		newest, now   time.Time
		contact       time.Time
		want          []Result
		missedResults bool
	}{
		{"before the deadline", ping, deadline.Add(-time.Second), ping, nil, false},
		{"at the deadline", ping, deadline, ping, nil, false},
		{"just after", ping, deadline.Add(time.Nanosecond), ping, []Result{missed(deadline, "no ping since 02:14 UTC")}, true},
		{"one missed, the next not due", ping, deadline.Add(70 * time.Minute), ping, []Result{missed(deadline, "no ping since 02:14 UTC")}, true},
		{"two missed after a restart", ping, deadline.Add(70*time.Minute + time.Second), ping,
			[]Result{missed(deadline, "no ping since 02:14 UTC"), missed(deadline.Add(70*time.Minute), "no ping since 02:14 UTC")}, true},
		{"many missed collapse into the first and the latest", ping, deadline.Add(10*70*time.Minute + time.Second), ping,
			[]Result{missed(deadline, "no ping since 02:14 UTC"), missed(deadline.Add(10*70*time.Minute), "no ping since 02:14 UTC")}, true},
		{"another day shows the date", ping, ping.Add(48 * time.Hour), ping,
			[]Result{missed(deadline, "no ping since 02:14 UTC"), missed(ping.Add(41*70*time.Minute), "no ping since Nov 6 02:14 UTC")}, true},
		{"no contact known", ping, deadline.Add(time.Second), time.Time{}, []Result{missed(deadline, "no ping")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Overdue(beat, tt.newest, tt.contact, tt.now, time.UTC)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Overdue() = %+v, want %+v", got, tt.want)
			}
			for _, r := range got {
				if !Missed(r) || Failed(r) {
					t.Fatalf("%+v is not classified as missed", r)
				}
			}
		})
	}
}

func TestOverdueNamesTheContactInTheZone(t *testing.T) {
	zone := func(name string) *time.Location {
		loc, err := config.LoadZone(name)
		if err != nil {
			t.Fatal(err)
		}
		return loc
	}
	ba := zone("America/Argentina/Buenos_Aires")
	tests := []struct {
		name    string
		contact time.Time
		loc     *time.Location
		want    string
	}{
		{"Buenos Aires", time.Date(2026, 11, 6, 15, 10, 0, 0, time.UTC), ba, "no ping since 12:10 -03"},
		{"Madrid in winter", time.Date(2026, 1, 6, 15, 10, 0, 0, time.UTC), zone("Europe/Madrid"), "no ping since 16:10 CET"},
		{"Madrid in summer", time.Date(2026, 7, 6, 15, 10, 0, 0, time.UTC), zone("Europe/Madrid"), "no ping since 17:10 CEST"},
		{"no zone is UTC", time.Date(2026, 11, 6, 15, 10, 0, 0, time.UTC), nil, "no ping since 15:10 UTC"},
		// 23:30 -03 on Nov 5, deadline 00:40 -03 on Nov 6: both Nov 6 in UTC.
		{"midnight in the zone shows the date", time.Date(2026, 11, 6, 2, 30, 0, 0, time.UTC), ba, "no ping since Nov 5 23:30 -03"},
		// 20:30 -03, deadline 21:40 -03 the same day: midnight only in UTC.
		{"midnight only in UTC shows no date", time.Date(2026, 11, 5, 23, 30, 0, 0, time.UTC), ba, "no ping since 20:30 -03"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Overdue(beat, tt.contact, tt.contact, tt.contact.Add(70*time.Minute+time.Second), tt.loc)
			if len(got) != 1 || got[0].Error != tt.want || !Missed(got[0]) {
				t.Fatalf("Overdue() = %+v, want one miss %q", got, tt.want)
			}
		})
	}
}
