package tui

import (
	"context"
	"errors"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/giulianotesta7/up/internal/config"
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
	hostname          string
	databasePath      string
	store             Store
	now               func() time.Time
	snap              Snapshot
	err               error
	width             int
	height            int
	selected          int
	detail            bool
	form              *monitorForm
	deletion          *deletePrompt
	mute              *mutePrompt
	notice            string
	smtpScreen        bool
	smtpSelected      int
	smtpSelectName    string
	smtpForm          *smtpForm
	smtpDelete        *deletePrompt
	channelScreen     bool
	channelSelected   int
	channelSelectName string
	channelForm       *channelForm
	channelDelete     *deletePrompt
	generation        uint64
	selectName        string
	animationOff      bool
	help              bool
	sweeping          bool
	sweepPosition     int
	sweepGeneration   uint64
}

// Options supplies display metadata for the open store.
type Options struct {
	DatabasePath string
	Hostname     string
}

// New returns a model reading from s; now is the clock used for ages and
// staleness. Options may specify the database path shown in the footer.
func New(s Store, now func() time.Time, options ...Options) Model {
	m := Model{store: s, now: now}
	if len(options) > 0 {
		m.databasePath = options[0].DatabasePath
		m.hostname = options[0].Hostname
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
	snap, err := Load(ctx, m.store, m.now())
	return snapshotMsg{snap: snap, err: err, generation: m.generation}
}

// Update handles snapshots, poll ticks and keys.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.splitMode() {
			m.detail = false
		}
		return m, nil
	case snapshotMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		var frame tea.Cmd
		// A failed read keeps the last good snapshot on screen.
		m.err = msg.err
		if msg.err == nil {
			if !m.animationOff && shouldSweep(m.snap, msg.snap, m.now(), os.Getenv("NO_COLOR"), os.Getenv("TERM")) {
				m.sweeping, m.sweepPosition = true, 0
				// A restart supersedes any pending frame. Only this generation
				// can continue the chain; old frames never schedule successors.
				m.sweepGeneration++
				frame = m.sweepTick()
			}
			m.snap = msg.snap
			if !m.snap.HasKey && m.smtpForm != nil && !m.smtpForm.saving {
				f := *m.smtpForm
				f.values[5] = ""
				if f.focus == 5 {
					f.focus = 6
					f.cursor = len([]rune(f.values[6]))
				}
				m.smtpForm = &f
			}
			if m.smtpSelectName != "" {
				for i, server := range m.snap.SMTPServers {
					if server.Name == m.smtpSelectName {
						m.smtpSelected = i
						m.smtpSelectName = ""
						break
					}
				}
			}
			m.smtpSelected = min(m.smtpSelected, max(0, len(m.snap.SMTPServers)-1))
			if m.channelSelectName != "" {
				for i, c := range m.snap.Channels {
					if c.Name == m.channelSelectName {
						m.channelSelected = i
						m.channelSelectName = ""
						break
					}
				}
			}
			m.channelSelected = min(m.channelSelected, max(0, len(m.snap.Channels)-1))
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
		m.sweepPosition++
		cells := ruleWidth(m.width)
		if m.splitMode() {
			cells = m.width
		}
		if m.sweepPosition >= cells-1 || !sweepEnabled(m.snap, m.now(), os.Getenv("NO_COLOR"), os.Getenv("TERM")) {
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
			if m.splitMode() {
				m.detail = false
			}
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
	case muteMsg:
		if msg.generation != m.generation || m.mute == nil || !m.mute.saving {
			return m, nil
		}
		if msg.err != nil {
			p := *m.mute
			p.saving, p.err = false, msg.err
			m.mute = &p
			return m, nil
		}
		m.mute = nil
		return m, m.load
	case unmuteMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		if msg.err != nil {
			m.notice = "Cannot unmute: " + msg.err.Error()
			return m, nil
		}
		return m, m.load
	case smtpSaveMsg:
		if msg.generation != m.generation || m.smtpForm == nil || !m.smtpForm.saving {
			return m, nil
		}
		if msg.err != nil {
			f := *m.smtpForm
			f.saving, f.err = false, msg.err
			m.smtpForm = &f
		} else {
			m.smtpForm = nil
			m.smtpSelectName = msg.name
		}
		return m, m.load
	case smtpDeleteMsg:
		if msg.generation != m.generation || m.smtpDelete == nil || !m.smtpDelete.saving {
			return m, nil
		}
		if msg.err != nil {
			p := *m.smtpDelete
			p.saving, p.err = false, msg.err
			m.smtpDelete = &p
		} else {
			servers := make([]config.SMTPServer, 0, len(m.snap.SMTPServers))
			for _, s := range m.snap.SMTPServers {
				if s.Name != m.smtpDelete.name {
					servers = append(servers, s)
				}
			}
			m.snap.SMTPServers = servers
			m.smtpSelected = min(m.smtpSelected, max(0, len(servers)-1))
			m.smtpDelete = nil
		}
		return m, m.load
	case channelSaveMsg:
		if msg.generation != m.generation || m.channelForm == nil || !m.channelForm.saving {
			return m, nil
		}
		if msg.err != nil {
			f := *m.channelForm
			f.saving, f.err = false, msg.err
			m.channelForm = &f
		} else {
			m.channelForm = nil
			m.channelSelectName = msg.name
		}
		return m, m.load
	case channelDeleteMsg:
		if msg.generation != m.generation || m.channelDelete == nil || !m.channelDelete.saving {
			return m, nil
		}
		if msg.err != nil {
			p := *m.channelDelete
			p.saving, p.err = false, msg.err
			m.channelDelete = &p
		} else {
			channels := make([]config.Channel, 0, len(m.snap.Channels))
			for _, c := range m.snap.Channels {
				if c.Name != m.channelDelete.name {
					channels = append(channels, c)
				}
			}
			m.snap.Channels = channels
			m.channelSelected = min(m.channelSelected, max(0, len(channels)-1))
			m.channelDelete = nil
		}
		return m, m.load
	case tea.PasteMsg:
		if m.channelForm != nil && !m.channelForm.saving {
			f := *m.channelForm
			f.insert(msg.Content)
			m.channelForm = &f
		} else if m.smtpForm != nil && !m.smtpForm.saving && (m.smtpForm.focus != 5 || m.snap.HasKey) {
			f := *m.smtpForm
			f.insert(msg.Content)
			m.smtpForm = &f
		} else if m.form != nil && !m.form.saving {
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
	if m.splitMode() {
		m.detail = false
	}
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.channelScreen {
		return m.handleChannelKey(msg)
	}
	if m.smtpScreen {
		return m.handleSMTPKey(msg)
	}
	if m.form != nil {
		return m.handleFormKey(msg)
	}
	if m.deletion != nil {
		return m.handleDeleteKey(msg)
	}
	if m.mute != nil {
		return m.handleMuteKey(msg)
	}
	m.notice = ""
	if m.help {
		if key := msg.String(); key == "?" || key == "esc" {
			m.help = false
		}
		return m, nil
	}
	switch msg.String() {
	case "?":
		m.help = true
	case "q":
		return m, tea.Quit
	case "c":
		m.channelScreen = true
		m.channelSelected = 0
	case "s":
		m.smtpScreen = true
		m.smtpSelected = 0
	case "a":
		m.animationOff = !m.animationOff
		if m.animationOff {
			m.sweeping = false
			m.sweepGeneration++
		}
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
	case "m", "u":
		if m.selected < len(m.snap.Services) {
			svc := m.snap.Services[m.selected]
			if msg.String() == "m" {
				m.mute = &mutePrompt{name: svc.Check.Name, wasAlerting: svc.Streak.Alerting}
			} else if svc.Muted(m.now()) {
				m.generation++
				return m, m.unmute(svc.Check.Name)
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
		if len(m.snap.Services) > 0 && !m.splitMode() {
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
	split := m.splitMode()
	if split {
		content = renderSplit(m.snap, m.selected, now, m.err, m.width, m.height, m.notice)
	} else if m.detail && m.selected < len(m.snap.Services) {
		content = renderDetail(m.snap, m.snap.Services[m.selected], now, m.err, m.width)
	}
	if m.help && !helpPopupFits(m.width, m.height) {
		content = renderKeys()
	}
	modal := ""
	if m.form != nil || m.deletion != nil || m.mute != nil {
		screen := ""
		switch {
		case m.mute != nil:
			screen = renderMute(*m.mute, m.snap, now, m.err)
		case m.deletion != nil:
			screen = renderDelete(*m.deletion, m.snap, now, m.err)
		default:
			screen = renderForm(*m.form, m.snap, now, m.err)
		}
		title, body := splitModalScreen(screen)
		var fits bool
		modal, fits = renderModal(title, body, m.width, m.height)
		if !fits {
			content = screen
			split = false
		}
	}
	if m.notice != "" && !split {
		content += "\n" + warnStyle.Render(m.notice)
	}
	if m.smtpScreen {
		content = renderSMTPList(m.snap, m.smtpSelected, now, m.err)
		if m.smtpForm != nil {
			content = renderSMTPForm(*m.smtpForm, m.snap, now, m.err)
		}
		if m.smtpDelete != nil {
			content = renderSMTPDelete(*m.smtpDelete, m.snap, now, m.err)
		}
	}
	if m.channelScreen {
		content = renderChannelList(m.snap, m.channelSelected, now, m.err)
		if m.channelForm != nil {
			content = renderChannelForm(*m.channelForm, m.snap, now, m.err)
		}
		if m.channelDelete != nil {
			content = renderChannelDelete(*m.channelDelete, m.snap, now, m.err)
		}
	}
	position := -1
	if m.sweeping && sweepEnabled(m.snap, now, os.Getenv("NO_COLOR"), os.Getenv("TERM")) {
		position = m.sweepPosition
	}
	rule := renderHostRule(m.snap, m.hostname, now, m.width, position)
	if split {
		rule = renderHostRuleCells(m.snap, m.hostname, now, m.width, position)
	}
	content = renderTitleWidth(m.snap, m.width) + "\n" + rule + "\n" + content
	if modal != "" || (m.help && helpPopupFits(m.width, m.height)) {
		content = padForPopup(content, m.height)
	}
	content += "\n" + helpStyle.Render("database: "+m.databasePath)
	if split {
		content = truncateLines(content, m.width)
	}
	if m.help && helpPopupFits(m.width, m.height) {
		if m.width > 0 {
			content = truncateLines(content, m.width)
		}
		content = overlayPopup(content, renderHelpPopup(m.splitMode()), m.width, m.height)
	}
	if modal != "" {
		if m.width > 0 {
			content = truncateLines(content, m.width)
		}
		content = overlayPopup(content, modal, m.width, m.height)
	}
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
