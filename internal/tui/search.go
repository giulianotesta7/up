package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	searchFooter  = "type to search · tab status · enter keep · esc clear"
	noMatchLine   = "No monitors match. esc clears."
	searchPadding = 26
)

// filterTabs are the status filters, in the order tab cycles them.
var filterTabs = []string{"all", "down", "failing", "held"}

// listFilter narrows the monitor list: a case-insensitive query on name, URL
// or host, and a status tab. open is the search bar being edited; a closed
// filter that is still active stays applied until esc clears it. total and
// shown are filled for rendering.
type listFilter struct {
	query        string
	tab          int
	open         bool
	total, shown int
	// listWidth is the width the split layout's left pane keeps whatever
	// the filter hides.
	listWidth int
}

func (f listFilter) active() bool { return f.open || f.query != "" || f.tab != 0 }

func (f listFilter) counter() string {
	return fmt.Sprintf("%d of %d monitors", f.shown, f.total)
}

// summary is the header text while the filter is kept and the bar is closed.
func (f listFilter) summary() string {
	var parts []string
	if f.query != "" {
		parts = append(parts, f.query)
	}
	if f.tab != 0 {
		parts = append(parts, filterTabs[f.tab])
	}
	parts = append(parts, f.counter(), "esc clears")
	return strings.Join(parts, " · ")
}

// bar is the search row under the header; empty while the bar is closed.
func (f listFilter) bar(width int) string {
	if !f.open {
		return ""
	}
	query := accentStyle.Render("/") + " " + f.query + accentStyle.Render("█")
	line := query + strings.Repeat(" ", max(1, searchPadding-ansi.StringWidth(query)))
	tabs := make([]string, len(filterTabs))
	for i, tab := range filterTabs {
		if i == f.tab {
			tabs[i] = accentStyle.Reverse(true).Render(" " + tab + " ")
		} else {
			tabs[i] = helpStyle.Render(" " + tab + " ")
		}
	}
	line += strings.Join(tabs, " ") + "   " + helpStyle.Render(f.counter())
	if width > 1 {
		line = ansi.Truncate(line, width, "")
	}
	return line
}

// matches reports whether s passes the status tab and the query.
func (f listFilter) matches(s Service, now time.Time) bool {
	switch filterTabs[f.tab] {
	case "down":
		if s.Label() != "DOWN" {
			return false
		}
	case "failing":
		if !strings.HasPrefix(s.Label(), "FAILING") {
			return false
		}
	case "held":
		if !s.Muted(now) && !s.InMaintenance.Active {
			return false
		}
	}
	if f.query == "" {
		return true
	}
	query := strings.ToLower(f.query)
	for _, text := range []string{s.Check.Name, s.Check.URL, s.Check.Host} {
		if strings.Contains(strings.ToLower(text), query) {
			return true
		}
	}
	return false
}

// highlightMatch accents the first occurrence of query in name; base styles
// the rest.
func highlightMatch(name, query string, base lipgloss.Style) string {
	lower, q := strings.ToLower(name), strings.ToLower(query)
	i := strings.Index(lower, q)
	if q == "" || i < 0 || len(lower) != len(name) {
		return base.Render(name)
	}
	end := i + len(q)
	return base.Render(name[:i]) + accentStyle.Bold(true).Render(name[i:end]) + base.Render(name[end:])
}

// visible lists the indexes of the services the filter lets through.
func (m Model) visible() []int {
	now := m.now()
	var out []int
	for i, s := range m.snap.Services {
		if !m.filter.active() || m.filter.matches(s, now) {
			out = append(out, i)
		}
	}
	return out
}

// hasSelection reports whether selected points at a visible service.
func (m Model) hasSelection() bool {
	for _, i := range m.visible() {
		if i == m.selected {
			return true
		}
	}
	return false
}

// reselect keeps the selection on a visible service.
func (m Model) reselect() Model {
	vis := m.visible()
	if len(vis) == 0 {
		return m
	}
	for _, i := range vis {
		if i == m.selected {
			return m
		}
		if i > m.selected {
			m.selected = i
			return m
		}
	}
	m.selected = vis[len(vis)-1]
	return m
}

// move selects the previous (-1) or next (+1) visible service.
func (m Model) move(delta int) Model {
	vis := m.visible()
	for pos, i := range vis {
		if i == m.selected {
			if next := pos + delta; next >= 0 && next < len(vis) {
				m.selected = vis[next]
			}
			return m
		}
	}
	return m.reselect()
}

// filtered is the snapshot as the list shows it: only visible services, with
// the selection translated to their positions.
func (m Model) filtered() (Snapshot, int, listFilter) {
	f := m.filter
	f.total = len(m.snap.Services)
	if m.splitMode() {
		f.listWidth = splitListWidth(m.snap, m.selected, m.now())
	}
	if !f.active() {
		f.shown = f.total
		return m.snap, m.selected, f
	}
	snap := m.snap
	snap.Services = nil
	selected := 0
	for _, i := range m.visible() {
		if i == m.selected {
			selected = len(snap.Services)
		}
		snap.Services = append(snap.Services, m.snap.Services[i])
	}
	f.shown = len(snap.Services)
	return snap, selected, f
}

// handleSearchKey edits the open search bar.
func (m Model) handleSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key := msg.String(); key {
	case "esc":
		m.filter = listFilter{}
	case "enter":
		m.filter.open = false
	case "tab":
		m.filter.tab = (m.filter.tab + 1) % len(filterTabs)
	case "shift+tab":
		m.filter.tab = (m.filter.tab + len(filterTabs) - 1) % len(filterTabs)
	case "backspace":
		if runes := []rune(m.filter.query); len(runes) > 0 {
			m.filter.query = string(runes[:len(runes)-1])
		}
	case "up":
		return m.move(-1), nil
	case "down":
		return m.move(1), nil
	default:
		m.filter.query += msg.Text
	}
	return m.reselect(), nil
}

const normalFooter = "↑/↓ select · enter open · n new · e edit · d delete · m mute · ? more · q quit"

// listFooterFor is the footer of the list: the search keys while the bar is open.
func listFooterFor(f listFilter) string {
	if f.open {
		return searchFooter
	}
	return normalFooter
}
