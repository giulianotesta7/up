package tui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/giulianotesta7/up/internal/store"
)

// pollInterval is how often the TUI rereads the store.
const pollInterval = 2 * time.Second

// loadTimeout bounds one snapshot read.
const loadTimeout = 5 * time.Second

type snapshotMsg struct {
	snap Snapshot
	err  error
}

type tickMsg struct{}

// Model is the Bubble Tea model of the TUI.
type Model struct {
	store    *store.Store
	now      func() time.Time
	snap     Snapshot
	err      error
	selected int
	detail   bool
}

// New returns a model reading from s; now is the clock used for ages and
// staleness.
func New(s *store.Store, now func() time.Time) Model {
	return Model{store: s, now: now}
}

// Init loads the first snapshot.
func (m Model) Init() tea.Cmd {
	return m.load
}

func (m Model) load() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
	snap, err := Load(ctx, m.store)
	return snapshotMsg{snap: snap, err: err}
}

// Update handles snapshots, poll ticks and keys.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case snapshotMsg:
		// A failed read keeps the last good snapshot on screen.
		m.err = msg.err
		if msg.err == nil {
			m.snap = msg.snap
		}
		if m.selected >= len(m.snap.Services) {
			m.selected = max(0, len(m.snap.Services)-1)
			if len(m.snap.Services) == 0 {
				m.detail = false
			}
		}
		return m, tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{} })
	case tickMsg:
		return m, m.load
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
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
	content := renderList(m.snap, m.selected, now, m.err)
	if m.detail && m.selected < len(m.snap.Services) {
		content = renderDetail(m.snap, m.snap.Services[m.selected], now, m.err)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// Run shows the TUI until the user quits or ctx is done.
func Run(ctx context.Context, s *store.Store) error {
	p := tea.NewProgram(New(s, time.Now), tea.WithContext(ctx))
	_, err := p.Run()
	if err != nil && ctx.Err() != nil && errors.Is(err, tea.ErrProgramKilled) {
		return nil
	}
	return err
}
