package config_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
)

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func mustCron(t *testing.T, expr string) config.Cron {
	t.Helper()
	c, err := config.ParseCron(expr)
	if err != nil {
		t.Fatalf("ParseCron(%q) = %v", expr, err)
	}
	return c
}

// runs lists the next n runs after from, formatted in loc.
func runs(t *testing.T, expr string, from time.Time, n int, loc *time.Location) []string {
	t.Helper()
	var out []string
	for _, r := range mustCron(t, expr).NextRuns(from, n, loc) {
		out = append(out, r.In(loc).Format("Mon Jan 2 15:04 MST"))
	}
	return out
}

func TestCronNextRuns(t *testing.T) {
	ba := mustZone(t, "America/Argentina/Buenos_Aires")
	// Sunday Nov 8 2026, 12:00 -03.
	from := time.Date(2026, 11, 8, 12, 0, 0, 0, ba)
	tests := []struct {
		expr string
		want []string
	}{
		{"0 2 * * 1-5", []string{"Mon Nov 9 02:00 -03", "Tue Nov 10 02:00 -03", "Wed Nov 11 02:00 -03", "Thu Nov 12 02:00 -03", "Fri Nov 13 02:00 -03", "Mon Nov 16 02:00 -03"}},
		{"*/20 12 * * *", []string{"Sun Nov 8 12:20 -03", "Sun Nov 8 12:40 -03", "Mon Nov 9 12:00 -03"}},
		{"0,30 9-10 * * *", []string{"Mon Nov 9 09:00 -03", "Mon Nov 9 09:30 -03", "Mon Nov 9 10:00 -03", "Mon Nov 9 10:30 -03", "Tue Nov 10 09:00 -03"}},
		{"15 3 1-10/3 * *", []string{"Tue Nov 10 03:15 -03", "Tue Dec 1 03:15 -03", "Fri Dec 4 03:15 -03", "Mon Dec 7 03:15 -03", "Thu Dec 10 03:15 -03", "Fri Jan 1 03:15 -03"}},
		{"0 0 * jan,jul sun", []string{"Sun Jan 3 00:00 -03", "Sun Jan 10 00:00 -03"}},
		{"0 6 * * MON-wed", []string{"Mon Nov 9 06:00 -03", "Tue Nov 10 06:00 -03", "Wed Nov 11 06:00 -03", "Mon Nov 16 06:00 -03"}},
		{"0 6 * * 7", []string{"Sun Nov 15 06:00 -03", "Sun Nov 22 06:00 -03"}},
		{"0 6 * * 5-7", []string{"Fri Nov 13 06:00 -03", "Sat Nov 14 06:00 -03", "Sun Nov 15 06:00 -03"}},
		{"30 12 * * *", []string{"Sun Nov 8 12:30 -03", "Mon Nov 9 12:30 -03"}},
		{"0 12 * * *", []string{"Mon Nov 9 12:00 -03"}}, // strictly after from
		{"10/25 23 * * *", []string{"Sun Nov 8 23:10 -03", "Sun Nov 8 23:35 -03", "Mon Nov 9 23:10 -03"}},
		// The standard OR rule: with both day and weekday restricted, either matches.
		{"0 2 13 * 5", []string{"Fri Nov 13 02:00 -03", "Fri Nov 20 02:00 -03", "Fri Nov 27 02:00 -03", "Fri Dec 4 02:00 -03", "Fri Dec 11 02:00 -03", "Sun Dec 13 02:00 -03"}},
		// With either one starting with a star, both must match (as in cron).
		{"0 2 13 * *", []string{"Fri Nov 13 02:00 -03", "Sun Dec 13 02:00 -03"}},
		{"0 2 */10 * 5", []string{"Fri Dec 11 02:00 -03", "Fri Jan 1 02:00 -03"}},
		{"0 0 29 2 *", []string{"Tue Feb 29 00:00 -03", "Sun Feb 29 00:00 -03"}},
		{"@hourly", []string{"Sun Nov 8 13:00 -03", "Sun Nov 8 14:00 -03"}},
		{"@daily", []string{"Mon Nov 9 00:00 -03", "Tue Nov 10 00:00 -03"}},
		{"@midnight", []string{"Mon Nov 9 00:00 -03"}},
		{"@weekly", []string{"Sun Nov 15 00:00 -03", "Sun Nov 22 00:00 -03"}},
		{"@monthly", []string{"Tue Dec 1 00:00 -03", "Fri Jan 1 00:00 -03"}},
		{"@yearly", []string{"Fri Jan 1 00:00 -03", "Sat Jan 1 00:00 -03"}},
		{"@annually", []string{"Fri Jan 1 00:00 -03"}},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			if got := runs(t, tt.expr, from, len(tt.want), ba); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("NextRuns(%q) =\n%v\nwant\n%v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestCronFollowsTheZone(t *testing.T) {
	from := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	got := mustCron(t, "0 2 * * *").Next(from, mustZone(t, "America/Argentina/Buenos_Aires"))
	if want := time.Date(2026, 11, 9, 5, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("Next() = %v, want %v", got, want)
	}
	if got := mustCron(t, "0 2 * * *").Next(from, nil); !got.Equal(time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("Next() with a nil zone = %v, want 02:00 UTC", got)
	}
}

func TestCronPrev(t *testing.T) {
	ba := mustZone(t, "America/Argentina/Buenos_Aires")
	c := mustCron(t, "0 2 * * 1-5")
	at := time.Date(2026, 11, 9, 2, 0, 0, 0, ba)
	if got := c.Prev(at, ba); !got.Equal(at) {
		t.Fatalf("Prev(at a run) = %v, want the run itself", got)
	}
	if got, want := c.Prev(at.Add(-time.Nanosecond), ba), time.Date(2026, 11, 6, 2, 0, 0, 0, ba); !got.Equal(want) {
		t.Fatalf("Prev(before Monday's run) = %v, want Friday's %v", got, want)
	}
}

// Daylight saving in New York: 2026-03-08 02:00 EST jumps to 03:00 EDT and
// 2026-11-01 02:00 EDT falls back to 01:00 EST. Like cron, a job at a fixed
// time that does not exist runs once when the clock jumps, and one at a
// repeated time runs once, the first time; a job whose minute or hour is a
// star follows the clock: none in the skipped hour, both passes of the
// repeated one.
func TestCronDaylightSaving(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	format := func(ts []time.Time) string {
		var out []string
		for _, r := range ts {
			out = append(out, r.UTC().Format("Jan 2 15:04"))
		}
		return strings.Join(out, ",")
	}
	tests := []struct {
		name, expr string
		from       time.Time
		n          int
		want       string
	}{
		{"skipped fixed time runs at the jump", "30 2 * * *", time.Date(2026, 3, 7, 12, 0, 0, 0, ny), 3, "Mar 8 07:00,Mar 9 06:30,Mar 10 06:30"},
		{"several skipped times are one run", "0,15,30,45 2 * * *", time.Date(2026, 3, 8, 0, 0, 0, 0, ny), 2, "Mar 8 07:00,Mar 9 06:00"},
		{"a skipped time and the jump are one run", "30 2,3 * * *", time.Date(2026, 3, 8, 0, 0, 0, 0, ny), 3, "Mar 8 07:00,Mar 8 07:30,Mar 9 06:30"},
		{"a star hour skips the missing hour", "30 * * * *", time.Date(2026, 3, 8, 0, 0, 0, 0, ny), 3, "Mar 8 05:30,Mar 8 06:30,Mar 8 07:30"},
		{"repeated fixed time runs once", "30 1 * * *", time.Date(2026, 10, 31, 12, 0, 0, 0, ny), 3, "Nov 1 05:30,Nov 2 06:30,Nov 3 06:30"},
		{"a star hour runs both passes", "30 * * * *", time.Date(2026, 11, 1, 0, 0, 0, 0, ny), 4, "Nov 1 04:30,Nov 1 05:30,Nov 1 06:30,Nov 1 07:30"},
		{"a star minute runs both passes", "*/30 1 * * *", time.Date(2026, 11, 1, 0, 0, 0, 0, ny), 5, "Nov 1 05:00,Nov 1 05:30,Nov 1 06:00,Nov 1 06:30,Nov 2 06:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := format(mustCron(t, tt.expr).NextRuns(tt.from, tt.n, ny)); got != tt.want {
				t.Fatalf("NextRuns(%q) = %s, want %s", tt.expr, got, tt.want)
			}
		})
	}
	// Prev agrees with Next across both transitions.
	for _, expr := range []string{"30 2 * * *", "30 1 * * *", "30 * * * *", "*/30 1 * * *"} {
		c := mustCron(t, expr)
		for _, from := range []time.Time{time.Date(2026, 3, 7, 0, 0, 0, 0, ny), time.Date(2026, 10, 31, 0, 0, 0, 0, ny)} {
			list := c.NextRuns(from, 40, ny)
			for i := 1; i < len(list); i++ {
				if !list[i].After(list[i-1]) {
					t.Fatalf("%q runs not increasing: %v then %v", expr, list[i-1], list[i])
				}
				if got := c.Prev(list[i].Add(-time.Nanosecond), ny); !got.Equal(list[i-1]) {
					t.Fatalf("%q Prev(%v) = %v, want %v", expr, list[i], got, list[i-1])
				}
			}
		}
	}
}

func TestParseCronErrors(t *testing.T) {
	tests := []struct{ expr, want string }{
		{"", "cron is required"},
		{"   ", "cron is required"},
		{"0 2 * *", `cron "0 2 * *" is not valid: it needs 5 fields: minute hour day month weekday`},
		{"0 0 2 * * *", `cron "0 0 2 * * *" is not valid: it needs 5 fields: minute hour day month weekday`},
		{"0 25 * * *", `cron "0 25 * * *" is not valid: hour 25 is out of range 0-23`},
		{"60 2 * * *", `cron "60 2 * * *" is not valid: minute 60 is out of range 0-59`},
		{"0 2 0 * *", `cron "0 2 0 * *" is not valid: day 0 is out of range 1-31`},
		{"0 2 * 13 *", `cron "0 2 * 13 *" is not valid: month 13 is out of range 1-12`},
		{"0 2 * * 8", `cron "0 2 * * 8" is not valid: weekday 8 is out of range 0-7`},
		{"x 2 * * *", `cron "x 2 * * *" is not valid: minute "x" is not a number`},
		{"0 2 * foo *", `cron "0 2 * foo *" is not valid: month "foo" is not a number or month name`},
		{"0 2 * * funday", `cron "0 2 * * funday" is not valid: weekday "funday" is not a number or weekday name`},
		{"0 5-2 * * *", `cron "0 5-2 * * *" is not valid: hour range 5-2 must go from low to high`},
		{"*/0 2 * * *", `cron "*/0 2 * * *" is not valid: minute step "0" must be at least 1`},
		{"*/x 2 * * *", `cron "*/x 2 * * *" is not valid: minute step "x" is not a number`},
		{"1,,2 2 * * *", `cron "1,,2 2 * * *" is not valid: minute "" is not a number`},
		{"0 0 30 2 *", `cron "0 0 30 2 *" is not valid: it never runs`},
		{"@reboot", `cron "@reboot" is not valid: use 5 fields or @hourly, @daily, @weekly, @monthly or @yearly`},
		{"0 2 * * * root", `cron "0 2 * * * root" is not valid: it needs 5 fields: minute hour day month weekday`},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			if _, err := config.ParseCron(tt.expr); err == nil || err.Error() != tt.want {
				t.Fatalf("ParseCron(%q) = %v, want %q", tt.expr, err, tt.want)
			}
		})
	}
}

func cronHeartbeat() config.Check {
	return config.Check{Name: "backup", Kind: config.CheckHeartbeat, Schedule: config.ScheduleCron, Cron: "0 2 * * 1-5", Grace: 30 * time.Minute}
}

func TestValidateCronHeartbeat(t *testing.T) {
	tests := []struct {
		name   string
		change func(*config.Check)
		want   string
	}{
		{"valid", func(*config.Check) {}, ""},
		{"macro", func(c *config.Check) { c.Cron = "@daily" }, ""},
		{"no grace", func(c *config.Check) { c.Grace = 0 }, ""},
		{"grace too long", func(c *config.Check) { c.Grace = config.MaxHeartbeatGrace + time.Second }, "grace must be between 0s and 744h"},
		{"missing cron", func(c *config.Check) { c.Cron = "" }, "cron is required"},
		{"invalid cron", func(c *config.Check) { c.Cron = "0 25 * * *" }, `cron "0 25 * * *" is not valid: hour 25 is out of range 0-23`},
		{"too long", func(c *config.Check) { c.Cron = "0 " + strings.Repeat("1,", 200) + "2 * * *" }, "cron must be at most 256 bytes"},
		{"period with cron", func(c *config.Check) { c.Period = time.Hour }, "period is not used by cron heartbeats"},
		{"unknown schedule", func(c *config.Check) { c.Schedule = "weekly" }, `schedule "weekly" must be period or cron`},
		{"cron with the period schedule", func(c *config.Check) { c.Schedule, c.Period = config.SchedulePeriod, time.Hour }, "cron is only for cron heartbeats"},
		{"cron with the default schedule", func(c *config.Check) { c.Schedule, c.Period = "", time.Hour }, "cron is only for cron heartbeats"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := cronHeartbeat()
			tt.change(&c)
			err := config.ValidateCheck(c)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateCheck() = %v, want %q", err, tt.want)
			}
		})
	}
	// The explicit period schedule is the default one.
	p := heartbeatCheck()
	p.Schedule = config.SchedulePeriod
	if err := config.ValidateCheck(p); err != nil {
		t.Fatal(err)
	}
	if got := p.Canonical(); got.Schedule != "" || !got.SameDefinition(heartbeatCheck()) {
		t.Fatalf("Canonical() = %+v, want the default schedule", got)
	}
}

func TestScheduleAndCronAreOnlyForHeartbeats(t *testing.T) {
	for _, base := range []func() config.Check{httpCheck, tcpCheck} {
		for _, change := range []func(*config.Check){
			func(c *config.Check) { c.Schedule = config.ScheduleCron },
			func(c *config.Check) { c.Cron = "0 2 * * *" },
		} {
			c := base()
			change(&c)
			if err := config.ValidateCheck(c); err == nil || !strings.Contains(err.Error(), "schedule and cron are only for heartbeat checks") {
				t.Fatalf("ValidateCheck(%+v) = %v", c, err)
			}
		}
	}
}

func TestCronDefinitionIdentity(t *testing.T) {
	base := cronHeartbeat()
	for name, change := range map[string]func(*config.Check){
		"schedule": func(c *config.Check) { c.Schedule, c.Cron, c.Period = "", "", time.Hour },
		"cron":     func(c *config.Check) { c.Cron = "0 3 * * 1-5" },
	} {
		c := base
		change(&c)
		if c.SameDefinition(base) {
			t.Fatalf("%s change kept the same definition", name)
		}
	}
	spaced := base
	spaced.Cron = " 0  2 * *\t1-5 "
	if !spaced.SameDefinition(base) || spaced.Canonical().Cron != "0 2 * * 1-5" {
		t.Fatalf("Canonical() = %q, want the fields joined by one space", spaced.Canonical().Cron)
	}
}
