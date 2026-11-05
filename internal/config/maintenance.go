package config

import (
	"errors"
	"fmt"
	"slices"
	"time"
	// Embed the IANA database so zones load on hosts and containers that do
	// not ship one.
	_ "time/tzdata"
)

// Bounds on a maintenance window's duration.
const (
	MinMaintenance = time.Minute
	MaxMaintenance = 7 * 24 * time.Hour
)

// MaintenanceHorizon caps how far ahead EvaluateMaintenance looks for the
// end of back-to-back windows, so windows that cover all time still return.
const MaintenanceHorizon = 366 * 24 * time.Hour

// MaintenanceWindow is a recurring weekly window during which a monitor's
// alerts and certificate warnings are suppressed. It starts at StartMinute
// (minutes after local midnight) on each of Weekdays, measured in the IANA
// zone Zone, and lasts Duration of elapsed time.
//
// Weekdays are the local weekdays on which a window STARTS; one that crosses
// midnight keeps running into the next day. ID is assigned by the store.
type MaintenanceWindow struct {
	ID          int64
	Check       string
	Weekdays    []time.Weekday
	StartMinute int
	Duration    time.Duration
	Zone        string
}

// Maintenance is the evaluation of a monitor's windows at one instant.
type Maintenance struct {
	// Active reports whether some window covers the instant.
	Active bool
	// End is when suppression by windows ends (UTC): the end of the union of
	// overlapping and touching windows. Zero when not Active.
	End time.Time
}

// ValidateMaintenanceWindow validates a window definition: a check, a
// non-empty set of distinct weekdays 0 (Sunday) to 6, a start minute 0..1439,
// a duration from MinMaintenance (1m) to MaxMaintenance (7d) and an IANA
// zone that loads ("Local" and the empty zone are refused).
func ValidateMaintenanceWindow(w MaintenanceWindow) error {
	var errs []error
	if w.Check == "" {
		errs = append(errs, errors.New("check is required"))
	}
	if len(w.Weekdays) == 0 {
		errs = append(errs, errors.New("at least one weekday is required"))
	}
	var seen [7]bool
	for _, d := range w.Weekdays {
		switch {
		case d < time.Sunday || d > time.Saturday:
			errs = append(errs, fmt.Errorf("weekday %d must be between 0 (Sunday) and 6 (Saturday)", int(d)))
		case seen[d]:
			errs = append(errs, fmt.Errorf("weekday %d is repeated", int(d)))
		default:
			seen[d] = true
		}
	}
	if w.StartMinute < 0 || w.StartMinute > 1439 {
		errs = append(errs, fmt.Errorf("start minute %d must be between 0 and 1439", w.StartMinute))
	}
	if w.Duration < MinMaintenance || w.Duration > MaxMaintenance {
		errs = append(errs, errors.New("duration must be between 1m and 7d"))
	}
	if w.Zone == "" {
		errs = append(errs, errors.New("zone is required"))
	} else if _, err := LoadZone(w.Zone); err != nil {
		errs = append(errs, fmt.Errorf("zone %q is not an IANA time zone", w.Zone))
	}
	return errors.Join(errs...)
}

// LoadZone loads an IANA zone, refusing "Local" and "".
func LoadZone(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("zone %q is not an IANA time zone", name)
	}
	return time.LoadLocation(name)
}

// WeekdayMask packs weekdays into a bit set (bit n is weekday n); the store
// keeps windows this way.
func WeekdayMask(days []time.Weekday) int {
	m := 0
	for _, d := range days {
		m |= 1 << d
	}
	return m
}

// WeekdaysFromMask unpacks a WeekdayMask in ascending order; nil for 0.
func WeekdaysFromMask(m int) []time.Weekday {
	var days []time.Weekday
	for d := time.Sunday; d <= time.Saturday; d++ {
		if m&(1<<d) != 0 {
			days = append(days, d)
		}
	}
	return days
}

type span struct{ start, end time.Time }

// EvaluateMaintenance reports whether any window covers at and when
// suppression ends. It is pure: no clock, database or input mutation. Rules:
//
//   - An occurrence is [start, start+Duration): the start is inclusive, the
//     end exclusive, and the duration is elapsed time, so a window crossing a
//     clock change does not stretch or shrink.
//   - A local start time that does not exist (spring-forward gap) is skipped
//     for that day; one that happens twice (fall-back overlap) runs once, at
//     its first occurrence.
//   - Windows (of any zones) that overlap or touch form one union; End is the
//     end of that union from the occurrence covering at, looking at most
//     MaintenanceHorizon ahead.
//   - A window with an unknown zone or non-positive duration is ignored.
func EvaluateMaintenance(windows []MaintenanceWindow, at time.Time) Maintenance {
	var active []span
	for _, s := range occurrences(windows, at.Add(-MaxMaintenance), at) {
		if s.end.After(at) {
			active = append(active, s)
		}
	}
	if len(active) == 0 {
		return Maintenance{}
	}
	end := slices.MaxFunc(active, func(a, b span) int { return a.end.Compare(b.end) }).end
	horizon := at.Add(MaintenanceHorizon)
	for end.Before(horizon) {
		next := end
		for _, s := range occurrences(windows, at.Add(-MaxMaintenance), end) {
			if s.end.After(next) {
				next = s.end
			}
		}
		if !next.After(end) {
			break
		}
		end = next
	}
	if end.After(horizon) {
		end = horizon
	}
	return Maintenance{Active: true, End: end.UTC()}
}

// occurrences lists every occurrence whose start is in [from, to].
func occurrences(windows []MaintenanceWindow, from, to time.Time) []span {
	var out []span
	for _, w := range windows {
		loc, err := LoadZone(w.Zone)
		if err != nil || w.Duration <= 0 {
			continue
		}
		first, last := from.In(loc), to.In(loc)
		day := time.Date(first.Year(), first.Month(), first.Day()-1, 12, 0, 0, 0, loc)
		stop := time.Date(last.Year(), last.Month(), last.Day()+1, 12, 0, 0, 0, loc)
		for ; !day.After(stop); day = time.Date(day.Year(), day.Month(), day.Day()+1, 12, 0, 0, 0, loc) {
			if !slices.Contains(w.Weekdays, day.Weekday()) {
				continue
			}
			start, ok := localStart(loc, day.Year(), day.Month(), day.Day(), w.StartMinute)
			if ok && !start.Before(from) && !start.After(to) {
				out = append(out, span{start, start.Add(w.Duration)})
			}
		}
	}
	return out
}

// localStart resolves a local wall-clock time to an instant: the earliest
// instant whose local time in loc is exactly that wall time, or false when
// the wall time does not exist (a DST gap).
func localStart(loc *time.Location, y int, m time.Month, d, minute int) (time.Time, bool) {
	wall := time.Date(y, m, d, minute/60, minute%60, 0, 0, time.UTC)
	var best time.Time
	found := false
	for _, probe := range []time.Time{wall.Add(-48 * time.Hour), wall, wall.Add(48 * time.Hour)} {
		_, offset := probe.In(loc).Zone()
		instant := wall.Add(-time.Duration(offset) * time.Second)
		if _, got := instant.In(loc).Zone(); got != offset {
			continue
		}
		if !found || instant.Before(best) {
			best, found = instant, true
		}
	}
	return best, found
}
