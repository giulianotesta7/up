package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Heartbeat schedules (Check.Schedule). The stored form of SchedulePeriod is
// empty.
const (
	SchedulePeriod = "period"
	ScheduleCron   = "cron"
)

// MaxCronLength is the longest cron expression in bytes.
const MaxCronLength = 256

// cronHorizon bounds the search for a run: long enough for a February 29
// across a skipped leap year (2100).
const cronHorizon = 9 * 366

// Cron is a parsed cron expression: minute hour day month weekday, as in
// crontab(5). Fields take numbers, ranges (1-5), lists (1,3), steps (*/15,
// 1-30/5, 10/20 meaning 10-max/20) and, for month and weekday, names (jan,
// mon; any case). Weekday 0 and 7 are Sunday. When both day and weekday are
// restricted a day matching either runs, and when either starts with "*"
// both must match, as cron does. @hourly, @daily (@midnight), @weekly,
// @monthly and @yearly (@annually) stand for their usual fields. There are
// no seconds.
//
// Runs are wall-clock times in a zone and follow cron across daylight
// saving: a time the clock skips runs once at the jump, and a repeated time
// runs once, the first time, unless the minute or hour field starts with
// "*" (such a job follows the clock: no run in the skipped hour, a run in
// each pass of the repeated one).
type Cron struct {
	minute uint64 // bits 0-59
	hour   uint32 // bits 0-23
	day    uint32 // bits 1-31
	month  uint16 // bits 1-12
	week   uint8  // bits 0-6
	// dayStar and weekStar record a field starting with "*"; wild a minute
	// or hour field starting with "*".
	dayStar, weekStar, wild bool
}

var cronMacros = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

var (
	monthNames = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	weekNames  = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
)

// cronField describes one field: its name in messages, its range and the
// names its values may take (index + first is the value).
type cronField struct {
	name     string
	min, max int
	names    []string
	kind     string // "month" or "weekday" in the not-a-number message
}

var cronFields = [5]cronField{
	{name: "minute", max: 59},
	{name: "hour", max: 23},
	{name: "day", min: 1, max: 31},
	{name: "month", min: 1, max: 12, names: monthNames, kind: "month"},
	{name: "weekday", max: 7, names: weekNames, kind: "weekday"},
}

// ParseCron parses a cron expression. Fields are separated by spaces or
// tabs. An expression that never runs (0 0 30 2 *) is not valid.
func ParseCron(expr string) (Cron, error) {
	fields := strings.Fields(expr)
	if len(fields) == 0 {
		return Cron{}, errors.New("cron is required")
	}
	invalid := func(err error) (Cron, error) {
		return Cron{}, fmt.Errorf("cron %q is not valid: %w", expr, err)
	}
	if len(fields) == 1 && strings.HasPrefix(fields[0], "@") {
		macro, ok := cronMacros[strings.ToLower(fields[0])]
		if !ok {
			return invalid(errors.New("use 5 fields or @hourly, @daily, @weekly, @monthly or @yearly"))
		}
		fields = strings.Fields(macro)
	}
	if len(fields) != 5 {
		return invalid(errors.New("it needs 5 fields: minute hour day month weekday"))
	}
	var bits [5]uint64
	for i, f := range fields {
		b, err := cronFields[i].parse(f)
		if err != nil {
			return invalid(err)
		}
		bits[i] = b
	}
	if bits[4]&(1<<7) != 0 {
		bits[4] = bits[4]&^(1<<7) | 1 // 7 is Sunday
	}
	c := Cron{minute: bits[0], hour: uint32(bits[1]), day: uint32(bits[2]), month: uint16(bits[3]), week: uint8(bits[4]),
		dayStar: strings.HasPrefix(fields[2], "*"), weekStar: strings.HasPrefix(fields[4], "*"),
		wild: strings.HasPrefix(fields[0], "*") || strings.HasPrefix(fields[1], "*")}
	if c.Next(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.UTC).IsZero() {
		return invalid(errors.New("it never runs"))
	}
	return c, nil
}

// parse turns one field into a bit set of its values.
func (f cronField) parse(text string) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(text, ",") {
		spec, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil {
				return 0, fmt.Errorf("%s step %q is not a number", f.name, stepText)
			}
			if n < 1 {
				return 0, fmt.Errorf("%s step %q must be at least 1", f.name, stepText)
			}
			step = n
		}
		lo, hi := f.min, f.max
		if spec != "*" {
			loText, hiText, isRange := strings.Cut(spec, "-")
			var err error
			if lo, err = f.value(loText); err != nil {
				return 0, err
			}
			hi = lo
			if isRange {
				if hi, err = f.value(hiText); err != nil {
					return 0, err
				}
				if hi < lo {
					return 0, fmt.Errorf("%s range %s must go from low to high", f.name, spec)
				}
			} else if hasStep {
				hi = f.max
			}
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << v
		}
	}
	return bits, nil
}

// value is one number or name of the field, within its range.
func (f cronField) value(text string) (int, error) {
	n, err := strconv.Atoi(text)
	if err != nil {
		for i, name := range f.names {
			if strings.EqualFold(text, name) {
				return i + f.min, nil
			}
		}
		if f.kind != "" {
			return 0, fmt.Errorf("%s %q is not a number or %s name", f.name, text, f.kind)
		}
		return 0, fmt.Errorf("%s %q is not a number", f.name, text)
	}
	if n < f.min || n > f.max {
		return 0, fmt.Errorf("%s %d is out of range %d-%d", f.name, n, f.min, f.max)
	}
	return n, nil
}

// runsOn reports whether the date (y, m, d) has runs.
func (c Cron) runsOn(y int, m time.Month, d int) bool {
	if c.month&(1<<m) == 0 {
		return false
	}
	day := c.day&(1<<d) != 0
	week := c.week&(1<<time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Weekday()) != 0
	if c.dayStar || c.weekStar {
		return day && week
	}
	return day || week
}

// dayRuns appends the runs of the date (y, m, d) in loc, in order.
func (c Cron) dayRuns(out []time.Time, y int, m time.Month, d int, loc *time.Location) []time.Time {
	start := len(out)
	midnight := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	// Without a zone change around the day, every wall time is one instant.
	_, before := midnight.Add(-15 * time.Hour).In(loc).Zone()
	_, after := midnight.Add(39 * time.Hour).In(loc).Zone()
	_, end := midnight.Add(-15 * time.Hour).In(loc).ZoneBounds()
	steady := before == after && (end.IsZero() || end.After(midnight.Add(39*time.Hour)))
	for h := 0; h < 24; h++ {
		if c.hour&(1<<h) == 0 {
			continue
		}
		for mi := 0; mi < 60; mi++ {
			if c.minute&(1<<mi) == 0 {
				continue
			}
			wall := midnight.Add(time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute)
			if steady {
				out = append(out, wall.Add(-time.Duration(before)*time.Second).In(loc))
				continue
			}
			out = c.appendResolved(out, wall, loc)
		}
	}
	if steady {
		return out
	}
	// Times in a skipped hour all run at the jump, and the passes of a
	// repeated hour interleave with the hours around them: sort and dedupe.
	day := out[start:]
	for i := 1; i < len(day); i++ {
		for j := i; j > 0 && day[j].Before(day[j-1]); j-- {
			day[j], day[j-1] = day[j-1], day[j]
		}
	}
	n := start
	for i := start; i < len(out); i++ {
		if i == start || !out[i].Equal(out[n-1]) {
			out[n] = out[i]
			n++
		}
	}
	return out[:n]
}

// appendResolved appends the instants of the wall time wall (a time whose
// UTC fields are the local ones) in loc on a day with a zone change.
func (c Cron) appendResolved(out []time.Time, wall time.Time, loc *time.Location) []time.Time {
	_, early := wall.Add(-15 * time.Hour).In(loc).Zone()
	_, late := wall.Add(15 * time.Hour).In(loc).Zone()
	var found []time.Time
	for _, offset := range []int{early, late} {
		t := wall.Add(-time.Duration(offset) * time.Second).In(loc)
		y, m, d := t.Date()
		if y == wall.Year() && m == wall.Month() && d == wall.Day() && t.Hour() == wall.Hour() && t.Minute() == wall.Minute() &&
			(len(found) == 0 || !found[0].Equal(t)) {
			found = append(found, t)
		}
	}
	switch {
	case len(found) == 0 && c.wild:
		return out
	case len(found) == 0:
		// The clock skipped this time: the run is at the jump, the start of
		// the zone the wall time falls into.
		start, _ := wall.Add(-time.Duration(early) * time.Second).In(loc).ZoneBounds()
		return append(out, start)
	case len(found) == 2 && found[1].Before(found[0]):
		found[0], found[1] = found[1], found[0]
	}
	if !c.wild {
		found = found[:1]
	}
	return append(out, found...)
}

// Next is the first run after after in loc (nil is UTC), or zero when there
// is none within nine years.
func (c Cron) Next(after time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	y, m, d := after.In(loc).Date()
	var buf []time.Time
	for i := -1; i <= cronHorizon; i++ {
		date := time.Date(y, m, d+i, 0, 0, 0, 0, time.UTC)
		if !c.runsOn(date.Year(), date.Month(), date.Day()) {
			continue
		}
		buf = c.dayRuns(buf[:0], date.Year(), date.Month(), date.Day(), loc)
		for _, t := range buf {
			if t.After(after) {
				return t
			}
		}
	}
	return time.Time{}
}

// Prev is the last run at or before at in loc (nil is UTC), or zero when
// there is none within nine years.
func (c Cron) Prev(at time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	y, m, d := at.In(loc).Date()
	var buf []time.Time
	for i := 1; i >= -cronHorizon; i-- {
		date := time.Date(y, m, d+i, 0, 0, 0, 0, time.UTC)
		if !c.runsOn(date.Year(), date.Month(), date.Day()) {
			continue
		}
		buf = c.dayRuns(buf[:0], date.Year(), date.Month(), date.Day(), loc)
		for j := len(buf) - 1; j >= 0; j-- {
			if !buf[j].After(at) {
				return buf[j]
			}
		}
	}
	return time.Time{}
}

// NextRuns lists the next n runs after after in loc (nil is UTC).
func (c Cron) NextRuns(after time.Time, n int, loc *time.Location) []time.Time {
	var out []time.Time
	for len(out) < n {
		t := c.Next(after, loc)
		if t.IsZero() {
			break
		}
		out = append(out, t)
		after = t
	}
	return out
}

// validateSchedule checks a heartbeat's schedule fields.
func validateSchedule(c Check) []error {
	var errs []error
	switch c.Schedule {
	case "", SchedulePeriod:
		if c.Period < MinHeartbeatPeriod || c.Period > MaxHeartbeatPeriod {
			errs = append(errs, fmt.Errorf("period must be between %s and %s", ShortDuration(MinHeartbeatPeriod), ShortDuration(MaxHeartbeatPeriod)))
		}
		if c.Cron != "" {
			errs = append(errs, errors.New("cron is only for cron heartbeats"))
		}
	case ScheduleCron:
		if c.Period != 0 {
			errs = append(errs, errors.New("period is not used by cron heartbeats"))
		}
		if len(c.Cron) > MaxCronLength {
			errs = append(errs, fmt.Errorf("cron must be at most %d bytes", MaxCronLength))
		} else if _, err := ParseCron(c.Cron); err != nil {
			errs = append(errs, err)
		}
	default:
		errs = append(errs, fmt.Errorf("schedule %q must be period or cron", c.Schedule))
	}
	return errs
}
