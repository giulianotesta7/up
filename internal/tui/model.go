package tui

import (
	"context"
	"errors"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/giulianotesta7/up/internal/store"
)

// pollInterval is how often the TUI rereads the store.
const pollInterval = 2 * time.Second

// loadTimeout bounds one snapshot read.
const loadTimeout = 5 * time.Second

type snapshotMsg struct {
	snap       Snapshot
	err        error
	generation uint64
}

type tickMsg struct{ generation uint64 }

const sweepFrameInterval = 40 * time.Millisecond

type sweepFrameMsg struct{ generation uint64 }

func sweepEnabled(snap Snapshot, now time.Time, noColor, term string) bool {
	return !snap.Stale(now) && noColor == "" && term != "dumb"
}

func shouldSweep(previous, next Snapshot, now time.Time, noColor, term string) bool {
	return previous.HasBeat && next.LastBeat.After(previous.LastBeat) && sweepEnabled(next, now, noColor, term)
}

func (m Model) sweepTick() tea.Cmd {
	generation := m.sweepGeneration
	return tea.Tick(sweepFrameInterval, func(time.Time) tea.Msg {
		return sweepFrameMsg{generation: generation}
	})
}

// Model is the Bubble Tea model of the TUI.
type Model struct {
	databasePath    string
	store           Store
	now             func() time.Time
	snap            Snapshot
	err             error
	width           int
	selected        int
	detail          bool
	form            *monitorForm
	deletion        *deletePrompt
	generation      uint64
	selectName      string
	sweeping        bool
	sweepPosition   int
	sweepGeneration uint64
}

// Options supplies display metadata for the open store.
type Options struct {
	DatabasePath string
}

// New returns a model reading from s; now is the clock used for ages and
// staleness. Options may specify the database path shown in the footer.
func New(s Store, now func() time.Time, options ...Options) Model {
	m := Model{store: s, now: now}
	if len(options) > 0 {
		m.databasePath = options[0].DatabasePath
	}
	return m
}

// Init loads the first snapshot.
func (m Model) Init() tea.Cmd {
	return m.load
}

func (m Model) load() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
	snap, err := Load(ctx, m.store)
	return snapshotMsg{snap: snap, err: err, generation: m.generation}
}

// Update handles snapshots, poll ticks and keys.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case snapshotMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		var frame tea.Cmd
		// A failed read keeps the last good snapshot on screen.
		m.err = msg.err
		if msg.err == nil {
			if shouldSweep(m.snap, msg.snap, m.now(), os.Getenv("NO_COLOR"), os.Getenv("TERM")) {
				m.sweeping, m.sweepPosition = true, 0
				// A restart supersedes any pending frame. Only this generation
				// can continue the chain; old frames never schedule successors.
				m.sweepGeneration++
				frame = m.sweepTick()
			}
			m.snap = msg.snap
			if !sweepEnabled(m.snap, m.now(), os.Getenv("NO_COLOR"), os.Getenv("TERM")) {
				m.sweeping = false
			}
			if m.selectName != "" {
				for i, svc := range m.snap.Services {
					if svc.Check.Name == m.selectName {
						m.selected = i
						m.selectName = ""
						break
					}
				}
			}
		}
		if m.selected >= len(m.snap.Services) {
			m.selected = max(0, len(m.snap.Services)-1)
			if len(m.snap.Services) == 0 {
				m.detail = false
			}
		}
		poll := tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{generation: m.generation} })
		if frame != nil {
			return m, tea.Batch(poll, frame)
		}
		return m, poll
	case sweepFrameMsg:
		if !m.sweeping || msg.generation != m.sweepGeneration {
			return m, nil
		}
		m.sweepPosition += 2
		if m.sweepPosition >= ruleWidth(m.width)-1 || !sweepEnabled(m.snap, m.now(), os.Getenv("NO_COLOR"), os.Getenv("TERM")) {
			m.sweeping = false
			return m, nil
		}
		return m, m.sweepTick()
	case tickMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		return m, m.load
	case saveMsg:
		if msg.generation != m.generation || m.form == nil || !m.form.saving {
			return m, nil
		}
		if msg.err != nil {
			f := *m.form
			f.saving, f.err = false, msg.err
			m.form = &f
		} else {
			m.detail = m.form.editing && m.form.returnDetail
			m.form = nil
			m.selectName = msg.name
		}
		return m, m.load
	case deleteMsg:
		if msg.generation != m.generation || m.deletion == nil || !m.deletion.saving {
			return m, nil
		}
		if msg.err != nil {
			p := *m.deletion
			p.saving, p.err = false, msg.err
			m.deletion = &p
		} else {
			// Drop the deleted row even if the subsequent refresh fails.
			services := make([]Service, 0, len(m.snap.Services))
			for _, svc := range m.snap.Services {
				if svc.Check.Name != m.deletion.name {
					services = append(services, svc)
				}
			}
			m.snap.Services = services
			m.selected = min(m.selected, max(0, len(services)-1))
			m.deletion, m.detail, m.selectName = nil, false, ""
		}
		return m, m.load
	case tea.PasteMsg:
		if m.form != nil && !m.form.saving {
			f := *m.form
			f.insert(msg.Content)
			m.form = &f
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.form != nil {
		return m.handleFormKey(msg)
	}
	if m.deletion != nil {
		return m.handleDeleteKey(msg)
	}
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "n":
		m.form = newMonitorForm()
	case "e", "d":
		if m.selected < len(m.snap.Services) {
			c := m.snap.Services[m.selected].Check
			if msg.String() == "e" {
				m.form = editMonitorForm(c, m.detail)
			} else {
				m.deletion = &deletePrompt{name: c.Name}
			}
		}
	case "up", "k":
		if !m.detail && m.selected > 0 {
			m.selected--
		}
	case "down", "j":
		if !m.detail && m.selected < len(m.snap.Services)-1 {
			m.selected++
		}
	case "enter":
		if len(m.snap.Services) > 0 {
			m.detail = true
		}
	case "esc", "backspace":
		m.detail = false
	}
	return m, nil
}

// View renders the list, or the detail of the selected service.
func (m Model) View() tea.View {
	now := m.now()
	content := renderList(m.snap, m.selected, now, m.err, m.width)
	if m.detail && m.selected < len(m.snap.Services) {
		content = renderDetail(m.snap, m.snap.Services[m.selected], now, m.err, m.width)
	}
	if m.form != nil {
		content = renderForm(*m.form, m.snap, now, m.err)
	}
	if m.deletion != nil {
		content = renderDelete(*m.deletion, m.snap, now, m.err)
	}
	position := -1
	if m.sweeping && sweepEnabled(m.snap, now, os.Getenv("NO_COLOR"), os.Getenv("TERM")) {
		position = m.sweepPosition
	}
	content = renderSweepHeader(m.snap, m.width, position) + content
	content += "\n" + helpStyle.Render("database: "+m.databasePath)
	// Lipgloss v2 emits truecolor styles; colorprofile only recognizes boolean
	// NO_COLOR values. Honor every nonempty value, including in direct renders.
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		content = ansi.Strip(content)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// Run shows the TUI until the user quits or ctx is done.
func Run(ctx context.Context, s *store.Store, options ...Options) error {
	p := tea.NewProgram(New(s, time.Now, options...), tea.WithContext(ctx))
	_, err := p.Run()
	if err != nil && ctx.Err() != nil && errors.Is(err, tea.ErrProgramKilled) {
		return nil
	}
	return err
}
