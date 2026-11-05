package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/giulianotesta7/up/internal/config"
)

// weekdayOrder is how weekdays are listed: Monday first.
var weekdayOrder = [7]time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday}

// maintenanceMinWidth keeps the popup from changing width between its list and
// its form.
const maintenanceMinWidth = 64

var windowLabels = [...]string{"Weekdays", "Local start", "Duration", "IANA zone"}

// maintenancePopup is the per-monitor maintenance popup: the window list, or
// the add/edit form on top of it, or a delete confirmation.
type maintenancePopup struct {
	name     string
	selected int
	form     *windowForm
	confirm  bool
	saving   bool
	err      error
}

// windowForm edits one window. focus 0 is the weekday row (cursor day);
// 1..3 are the text fields start, duration and zone.
type windowForm struct {
	id     int64
	days   [7]bool // by weekdayOrder
	day    int
	values [3]string
	focus  int
	cursor int
}

type maintenanceMsg struct {
	generation uint64
	err        error
}

// errNoMaintenance is shown when the store cannot edit windows.
var errNoMaintenance = errors.New("this server does not support maintenance windows")

func newWindowForm(zone string) *windowForm {
	return &windowForm{values: [3]string{"02:00", "1h", zone}}
}

func editWindowForm(w config.MaintenanceWindow) *windowForm {
	f := &windowForm{id: w.ID, values: [3]string{fmt.Sprintf("%02d:%02d", w.StartMinute/60, w.StartMinute%60), shortDuration(w.Duration), w.Zone}}
	for i, d := range weekdayOrder {
		for _, wd := range w.Weekdays {
			f.days[i] = f.days[i] || wd == d
		}
	}
	return f
}

// window builds the window the form describes and validates it with
// config.ValidateMaintenanceWindow, the same rules the store enforces.
func (f windowForm) window(check string) (config.MaintenanceWindow, error) {
	w := config.MaintenanceWindow{ID: f.id, Check: check, Zone: strings.TrimSpace(f.values[2])}
	for i, on := range f.days {
		if on {
			w.Weekdays = append(w.Weekdays, weekdayOrder[i])
		}
	}
	var errs []error
	if hh, mm, ok := strings.Cut(strings.TrimSpace(f.values[0]), ":"); ok && len(hh) == 2 && len(mm) == 2 {
		h, herr := strconv.Atoi(hh)
		n, merr := strconv.Atoi(mm)
		if herr != nil || merr != nil || h < 0 || h > 23 || n < 0 || n > 59 {
			ok = false
		}
		w.StartMinute = h*60 + n
		if !ok {
			errs = append(errs, fmt.Errorf("start %q must be a 24-hour time like 23:00", f.values[0]))
		}
	} else {
		errs = append(errs, fmt.Errorf("start %q must be a 24-hour time like 23:00", f.values[0]))
	}
	d, err := time.ParseDuration(strings.TrimSpace(f.values[1]))
	if err != nil {
		errs = append(errs, fmt.Errorf("duration %q is not a duration (use values like 45m or 2h)", f.values[1]))
	}
	w.Duration = d
	if len(errs) > 0 {
		// Report the unparsable fields; the rest is checked once they parse.
		return w, errors.Join(errs...)
	}
	return w, config.ValidateMaintenanceWindow(w)
}

func (f *windowForm) insert(text string) {
	if f.focus == 0 {
		return
	}
	editor := monitorForm{cursor: f.cursor}
	editor.values[0] = f.values[f.focus-1]
	editor.insert(text)
	f.values[f.focus-1] = editor.values[0]
	f.cursor = editor.cursor
}

func (f *windowForm) edit(msg tea.KeyPressMsg) {
	if f.focus == 0 {
		switch key := msg.String(); {
		case key == "left" || key == "h":
			f.day = max(0, f.day-1)
		case key == "right" || key == "l":
			f.day = min(len(f.days)-1, f.day+1)
		case key == "space" || msg.Text == " ":
			f.days[f.day] = !f.days[f.day]
		}
		return
	}
	editor := monitorForm{cursor: f.cursor}
	editor.values[0] = f.values[f.focus-1]
	editor.edit(msg)
	f.values[f.focus-1] = editor.values[0]
	f.cursor = editor.cursor
}

// maintenanceService finds the monitor a popup is about in the snapshot.
func (m Model) maintenanceService(name string) (Service, bool) {
	for _, svc := range m.snap.Services {
		if svc.Check.Name == name {
			return svc, true
		}
	}
	return Service{}, false
}

func (m Model) openMaintenance() Model {
	if m.selected < len(m.snap.Services) {
		m.maint = &maintenancePopup{name: m.snap.Services[m.selected].Check.Name}
	}
	return m
}

func (m Model) handleMaintenanceKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := *m.maint
	m.maint = &p
	if p.saving {
		return m, nil
	}
	svc, ok := m.maintenanceService(p.name)
	if !ok {
		m.maint = nil
		return m, nil
	}
	windows := svc.Maintenance
	p.selected = min(p.selected, max(0, len(windows)-1))
	switch {
	case p.form != nil:
		return m.handleWindowFormKey(&p)(msg)
	case p.confirm:
		switch msg.String() {
		case "n", "esc":
			p.confirm = false
		case "y":
			return m.saveWindow(&p, func(ctx context.Context, store MaintenanceStore) error {
				return store.DeleteMaintenanceWindow(ctx, p.name, windows[p.selected].ID)
			})
		}
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.maint = nil
	case "up", "k":
		p.selected = max(0, p.selected-1)
	case "down", "j":
		p.selected = min(max(0, len(windows)-1), p.selected+1)
	case "a":
		p.err = nil
		p.form = newWindowForm(localZone(m.now().Location()))
	case "e", "enter":
		if len(windows) > 0 {
			p.err = nil
			p.form = editWindowForm(windows[p.selected])
		}
	case "d":
		if len(windows) > 0 {
			p.err, p.confirm = nil, true
		}
	}
	return m, nil
}

func (m Model) handleWindowFormKey(p *maintenancePopup) func(tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	return func(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
		f := *p.form
		p.form = &f
		focus := func(to int) {
			f.focus = (to + len(f.values) + 1) % (len(f.values) + 1)
			if f.focus > 0 {
				f.cursor = len([]rune(f.values[f.focus-1]))
			}
		}
		switch msg.String() {
		case "esc":
			p.form, p.err = nil, nil
		case "tab", "down":
			focus(f.focus + 1)
		case "shift+tab", "up":
			focus(f.focus - 1)
		case "enter":
			w, err := f.window(p.name)
			if p.err = err; err != nil {
				return m, nil
			}
			return m.saveWindow(p, func(ctx context.Context, store MaintenanceStore) error {
				if w.ID == 0 {
					_, err := store.AddMaintenanceWindow(ctx, w)
					return err
				}
				return store.UpdateMaintenanceWindow(ctx, w)
			})
		default:
			f.edit(msg)
		}
		return m, nil
	}
}

// saveWindow runs one window write; maintenanceMsg closes the form or confirm
// on success and shows the store's error otherwise.
func (m Model) saveWindow(p *maintenancePopup, write func(context.Context, MaintenanceStore) error) (tea.Model, tea.Cmd) {
	p.saving, p.err = true, nil
	m.generation++
	generation := m.generation
	writer, _ := m.store.(MaintenanceStore)
	return m, func() tea.Msg {
		if writer == nil {
			return maintenanceMsg{generation: generation, err: errNoMaintenance}
		}
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		return maintenanceMsg{generation: generation, err: write(ctx, writer)}
	}
}

func (m Model) handleMaintenanceMsg(msg maintenanceMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.generation || m.maint == nil || !m.maint.saving {
		return m, nil
	}
	p := *m.maint
	m.maint = &p
	p.saving = false
	if msg.err != nil {
		p.err = msg.err
		return m, nil
	}
	p.form, p.confirm = nil, false
	return m, m.load
}

func (m Model) handleMaintenancePaste(content string) Model {
	if m.maint == nil || m.maint.form == nil || m.maint.saving {
		return m
	}
	p := *m.maint
	f := *p.form
	f.insert(content)
	p.form = &f
	m.maint = &p
	return m
}

func (m Model) maintenanceScreen(now time.Time) string {
	svc, _ := m.maintenanceService(m.maint.name)
	return renderMaintenance(*m.maint, svc, m.snap, now, m.err, m.height)
}

// renderMaintenance draws the popup. With a known terminal height the window
// list scrolls to keep the selected row, the header and the footer on screen.
func renderMaintenance(p maintenancePopup, svc Service, snap Snapshot, now time.Time, loadErr error, height int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Maintenance · "+p.name) + "\n")
	b.WriteString(renderBanners(snap, now, loadErr))
	if held := heldLine(svc, now); held != "" {
		b.WriteString(held + "\n")
	}
	b.WriteString(helpStyle.Render("Alerts are held during a window or while muted. Checks and uptime keep counting.") + "\n\n")
	if p.form != nil {
		renderWindowForm(&b, *p.form)
	} else {
		if len(svc.Maintenance) == 0 {
			b.WriteString(helpStyle.Render("No windows yet. Press a to add one.") + "\n")
		}
		first, last := 0, len(svc.Maintenance)
		if height > 0 {
			// renderModal needs the body plus 4 rows; the rest of the body is
			// fixed text, a blank row and the footer, plus the confirmation
			// and error rows when shown.
			fixed := strings.Count(b.String(), "\n") - 1 + 2
			if p.confirm {
				fixed += 2
			}
			if p.err != nil {
				fixed += 1 + strings.Count(p.err.Error(), "\n") + 1
			}
			if room := height - 4 - fixed; room < last {
				// Two rows are reserved for the "more" markers.
				visible := max(1, room-2)
				first = min(max(0, p.selected-visible/2), last-visible)
				last = first + visible
			}
		}
		if first > 0 {
			b.WriteString(helpStyle.Render(fmt.Sprintf("  ↑ %d more", first)) + "\n")
		}
		for i := first; i < last; i++ {
			row := describeWindow(svc.Maintenance[i])
			if i == p.selected {
				fmt.Fprintf(&b, "  %s %s\n", accentStyle.Render(">"), accentStyle.Render(row))
			} else {
				fmt.Fprintf(&b, "    %s\n", row)
			}
		}
		if last < len(svc.Maintenance) {
			b.WriteString(helpStyle.Render(fmt.Sprintf("  ↓ %d more", len(svc.Maintenance)-last)) + "\n")
		}
	}
	if p.confirm && p.selected < len(svc.Maintenance) {
		b.WriteString("\n" + fmt.Sprintf("Delete the window %s?", describeWindow(svc.Maintenance[p.selected])) + "\n")
	}
	if p.err != nil {
		b.WriteString("\n")
		for _, line := range strings.Split(p.err.Error(), "\n") {
			b.WriteString(warnStyle.Render("Cannot save: "+line) + "\n")
		}
	}
	switch {
	case p.saving:
		b.WriteString("\n" + renderHelp("Saving… · ctrl+c quit"))
	case p.form != nil:
		b.WriteString("\n" + renderHelp("tab/shift+tab field · ←/→ day · space toggle · enter save · esc back"))
	case p.confirm:
		b.WriteString("\n" + renderHelp("y delete · n cancel"))
	default:
		b.WriteString("\n" + renderHelp("↑/↓ choose · a add · e edit · d delete · esc close"))
	}
	return b.String()
}

func renderWindowForm(b *strings.Builder, f windowForm) {
	title := "Add window"
	if f.id != 0 {
		title = "Edit window"
	}
	b.WriteString(headerStyle.Render(title) + "\n")
	marker := func(on bool) string {
		if on {
			return accentStyle.Render(">")
		}
		return " "
	}
	var days []string
	for i, d := range weekdayOrder {
		box := "[ ]"
		if f.days[i] {
			box = "[x]"
		}
		cell := box + " " + d.String()[:3]
		if f.focus == 0 && f.day == i {
			cell = accentStyle.Render(cell)
		}
		days = append(days, cell)
	}
	fmt.Fprintf(b, "%s %-12s %s\n", marker(f.focus == 0), windowLabels[0], strings.Join(days, "  "))
	hints := [...]string{"HH:MM, 24-hour", "e.g. 45m, 2h, 1h30m", "e.g. America/New_York"}
	for i, value := range f.values {
		if f.focus == i+1 {
			r := []rune(value)
			cursor := min(f.cursor, len(r))
			fmt.Fprintf(b, "%s %-12s %s%s%s   %s\n", marker(true), accentStyle.Render(windowLabels[i+1]), string(r[:cursor]), accentStyle.Render("│"), string(r[cursor:]), helpStyle.Render(hints[i]))
			continue
		}
		fmt.Fprintf(b, "%s %-12s %s\n", marker(false), windowLabels[i+1], value)
	}
}

// heldLine says what holds the monitor's alerts now: an active window with
// its end, a manual mute, or both. It is empty when nothing does.
func heldLine(s Service, now time.Time) string {
	var parts []string
	if s.InMaintenance.Active {
		parts = append(parts, warnStyle.Render("Maintenance active")+" until "+clockWithDay(s.InMaintenance.End, now)+" ("+timeLeft(s.InMaintenance.End.Sub(now))+" left)")
	}
	if s.Muted(now) {
		parts = append(parts, warnStyle.Render("Muted")+" until "+clockWithDay(s.Mute.Until, now)+" ("+timeLeft(s.Mute.Until.Sub(now))+" left)")
	}
	return strings.Join(parts, " · ")
}

// heldCell is the compact list cell for the same state.
func heldCell(s Service, now time.Time) string {
	var parts []string
	if s.InMaintenance.Active {
		parts = append(parts, "◷ until "+clockWithDay(s.InMaintenance.End, now))
	}
	if s.Muted(now) {
		parts = append(parts, "⏸ "+timeLeft(s.Mute.Until.Sub(now))+" left")
	}
	if len(parts) == 0 {
		return ""
	}
	return warnStyle.Render(strings.Join(parts, " · "))
}

// clockWithDay is the local HH:MM of t, with the weekday when it is not today.
func clockWithDay(t, now time.Time) string {
	local := t.In(now.Location())
	if local.YearDay() == now.YearDay() && local.Year() == now.Year() {
		return local.Format("15:04")
	}
	return local.Format("Mon 15:04")
}

// describeWindow is one list row: weekdays, local start, duration, zone and
// whether it crosses midnight.
func describeWindow(w config.MaintenanceWindow) string {
	var days []string
	on := map[time.Weekday]bool{}
	for _, d := range w.Weekdays {
		on[d] = true
	}
	for _, d := range weekdayOrder {
		if on[d] {
			days = append(days, d.String()[:3])
		}
	}
	list := strings.Join(days, ", ")
	if len(days) == 7 {
		list = "Every day"
	}
	row := fmt.Sprintf("%s %02d:%02d + %s · %s", list, w.StartMinute/60, w.StartMinute%60, shortDuration(w.Duration), w.Zone)
	if time.Duration(w.StartMinute)*time.Minute+w.Duration > 24*time.Hour {
		row += " · crosses midnight"
	}
	return row
}

// shortDuration formats d like the durations the mute prompt accepts: "2h",
// "1h30m", "45m".
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// localZone names the viewer's IANA zone, which is the default for a new
// window. A Local location carries no name, so the TZ variable and then the
// /etc/localtime link are consulted; UTC is the last resort.
func localZone(loc *time.Location) string {
	if name := loc.String(); name != "" && name != "Local" {
		return name
	}
	if name := os.Getenv("TZ"); name != "" {
		if _, err := config.LoadZone(strings.TrimPrefix(name, ":")); err == nil {
			return strings.TrimPrefix(name, ":")
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(target, "zoneinfo/"); ok {
			if _, err := config.LoadZone(name); err == nil {
				return name
			}
		}
	}
	return "UTC"
}

// heldPending is what the recipients will be told when nothing holds alerts:
// "down", "recovery" or "" (nothing).
func (s Service) heldPending() string {
	known := s.KnownAlerting
	if s.HasMute {
		known = s.Mute.WasAlerting
	}
	switch {
	case s.Streak.Alerting == known:
		return ""
	case s.Streak.Alerting:
		return "down"
	default:
		return "recovery"
	}
}

// heldColumn is the width and title of the list's held column. It is MUTED at
// 13 cells and grows only for a maintenance window, alone or with a mute.
func heldColumn(snap Snapshot, now time.Time) (width int, header string) {
	width, header = 13, "MUTED"
	for _, s := range snap.Services {
		if s.InMaintenance.Active {
			header = "HELD"
			width = max(width, ansi.StringWidth(heldCell(s, now)))
		}
	}
	return width, header
}
