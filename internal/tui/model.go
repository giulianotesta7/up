package tui

import (
	"context"
	"errors"
	"os"
	"strings"
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
	destination       string
	store             Store
	now               func() time.Time
	snap              Snapshot
	err               error
	loaded            bool
	width             int
	height            int
	selected          int
	detail            bool
	form              *monitorForm
	deletion          *deletePrompt
	mute              *mutePrompt
	maint             *maintenancePopup
	defaults          *defaultsPopup
	notice            string
	noticeOK          bool // notice is a confirmation, not a warning
	notify            bool // the notifications view is open, on notifyTab
	notifyTab         int
	deliveries        deliveryFilter
	deliverySelected  int
	deliveryTop       int
	smtpSelected      int
	smtpSelectName    string
	smtpForm          *smtpForm
	smtpDelete        *deletePrompt
	channelSelected   int
	channelSelectName string
	channelForm       *channelForm
	channelDelete     *deletePrompt
	channelTest       *channelTestPrompt
	generation        uint64
	selectName        string
	animationOff      bool
	help              bool
	filter            listFilter
	sweeping          bool
	sweepPosition     int
	sweepGeneration   uint64
	// incidentName is the monitor whose incident list incidentCursor and
	// incidentOffset scroll; another monitor starts at the top.
	incidentName   string
	incidentCursor int
	incidentOffset int
	// revealName is the heartbeat whose ping token v showed (revealToken)
	// until the next other key; pingError is why it could not be shown.
	revealName, revealToken string
	pingError               *pingErrorBox
}

// incidentScroll places the incident window of the selected monitor; ping
// is its ping URL state.
type incidentScroll struct {
	cursor, offset int
	ping           pingView
}

func (m Model) scroll() incidentScroll {
	if !m.hasSelection() || m.snap.Services[m.selected].Check.Name != m.incidentName {
		return incidentScroll{}
	}
	return incidentScroll{cursor: m.incidentCursor, offset: m.incidentOffset}
}

// detailHeight is the number of lines the full-screen detail may use: the
// screen without the title, the rule, the footer and a notice.
func (m Model) detailHeight() int {
	if m.height <= 0 {
		return 0
	}
	height := m.height - 3
	if m.notice != "" {
		height--
	}
	return max(1, height)
}

// incidentRows is how many incidents the selected monitor's detail shows in
// the current layout.
func (m Model) incidentRows() int {
	svc := m.snap.Services[m.selected]
	if m.splitMode() {
		shown, selected, filter := m.filtered()
		_, opts := splitDetail(shown, selected, m.now(), m.err, m.width, m.height, incidentScroll{ping: m.pingView()}, filter)
		return opts.incidentRows
	}
	opts := fullDetail()
	opts.ping = m.pingView()
	_, opts = fitDetail(m.snap, svc, m.now(), m.err, m.width, m.detailHeight(), opts)
	return opts.incidentRows
}

// scrollIncidents moves the incident cursor by delta rows, or by delta pages.
func (m Model) scrollIncidents(delta int, page bool) Model {
	if !m.hasSelection() {
		return m
	}
	svc := m.snap.Services[m.selected]
	n := len(svc.Incidents)
	if n == 0 {
		return m
	}
	scroll := m.scroll()
	rows := max(1, min(n, m.incidentRows()))
	if page {
		delta *= rows
	}
	m.incidentName = svc.Check.Name
	m.incidentCursor, m.incidentOffset = incidentWindow(n, rows, scroll.cursor+delta, scroll.offset)
	return m
}

// Options supplies display metadata for the open store.
type Options struct {
	DatabasePath string
	Hostname     string
	// Destination names a remote server. When set, the footer shows it
	// instead of the database path, so a remote session is never mistaken
	// for the local one.
	Destination string
}

// New returns a model reading from s; now is the clock used for ages and
// staleness. Options may specify the database path shown in the footer.
func New(s Store, now func() time.Time, options ...Options) Model {
	m := Model{store: s, now: now}
	if len(options) > 0 {
		m.databasePath = options[0].DatabasePath
		m.destination = options[0].Destination
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
	var snap Snapshot
	var err error
	if loader, ok := m.store.(SnapshotLoader); ok {
		snap, err = loader.LoadSnapshot(ctx, m.now())
	} else {
		snap, err = Load(ctx, m.store, m.now())
	}
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
		if m.destination != "" && msg.err != nil {
			m.err = serverError{msg.err}
		}
		if msg.err == nil {
			m.loaded = true
			if !m.animationOff && shouldSweep(m.snap, msg.snap, m.now(), os.Getenv("NO_COLOR"), os.Getenv("TERM")) {
				m.sweeping, m.sweepPosition = true, 0
				// A restart supersedes any pending frame. Only this generation
				// can continue the chain; old frames never schedule successors.
				m.sweepGeneration++
				frame = m.sweepTick()
			}
			previous := ""
			if m.filter.active() && m.selected < len(m.snap.Services) {
				previous = m.snap.Services[m.selected].Check.Name
			}
			m.snap = msg.snap
			for i, svc := range m.snap.Services {
				if previous != "" && svc.Check.Name == previous {
					m.selected = i
				}
			}
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
		m = m.reselect()
		if !m.hasSelection() {
			m.detail = false
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
			m = m.handleSaved(msg)
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
			m = m.reselect()
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
	case maintenanceMsg:
		return m.handleMaintenanceMsg(msg)
	case defaultsMsg:
		return m.handleDefaultsMsg(msg)
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
	case channelTestMsg:
		return m.channelTestResult(msg)
	case revealMsg:
		return m.handleReveal(msg), nil
	case tea.PasteMsg:
		if m.notify && m.deliveries.open && m.smtpForm == nil && m.channelForm == nil {
			return m.pasteDeliverySearch(msg.Content), nil
		}
		if m.filter.open && m.form == nil && m.deletion == nil && m.mute == nil && !m.help && !m.notify {
			m.filter.query += strings.NewReplacer("\r", "", "\n", " ").Replace(msg.Content)
			return m.reselect(), nil
		}
		if m.defaults != nil {
			return m.handleDefaultsPaste(msg.Content), nil
		}
		if m.maint != nil {
			return m.handleMaintenancePaste(msg.Content), nil
		}
		if m.channelForm != nil && !m.channelForm.saving {
			f := *m.channelForm
			f.insert(msg.Content)
			m.channelForm = &f
		} else if m.smtpForm != nil && !m.smtpForm.saving && (m.smtpForm.focus != 5 || m.snap.HasKey) {
			f := *m.smtpForm
			f.insert(msg.Content)
			m.smtpForm = &f
		} else if m.form != nil && !m.form.saving && !m.form.confirm && m.form.pingToken == "" {
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
	// A shown ping token lasts until the next key.
	if msg.String() != "v" {
		m.revealName, m.revealToken = "", ""
	}
	if m.pingError != nil {
		if key := msg.String(); key == "esc" || key == "enter" || key == "v" {
			m.pingError = nil
		}
		return m, nil
	}
	if m.notify {
		return m.handleNotifyKey(msg)
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
	if m.maint != nil {
		return m.handleMaintenanceKey(msg)
	}
	if m.defaults != nil {
		return m.handleDefaultsKey(msg)
	}
	m.notice, m.noticeOK = "", false
	if m.help {
		if key := msg.String(); key == "?" || key == "esc" {
			m.help = false
		}
		return m, nil
	}
	if m.filter.open {
		return m.handleSearchKey(msg)
	}
	switch msg.String() {
	case "/":
		if !m.detail {
			m.filter.open = true
		}
	case "r":
		if m.destination != "" {
			m.generation++
			return m, m.load
		}
	case "?":
		m.help = true
	case "q":
		return m, tea.Quit
	case "c":
		m = m.openNotifications(tabChannels)
	case "s":
		m = m.openNotifications(tabMail)
	case "a":
		m.animationOff = !m.animationOff
		if m.animationOff {
			m.sweeping = false
			m.sweepGeneration++
		}
	case "n":
		m.form = newMonitorForm(m.snap)
	case "y":
		if m.selected < len(m.snap.Services) {
			m.form = copyMonitorForm(m.snap.Services[m.selected].Check, m.snap)
		}
	case "f":
		m.defaults = newDefaultsPopup(m.snap)
	case "e", "d":
		if m.hasSelection() {
			c := m.snap.Services[m.selected].Check
			if msg.String() == "e" {
				m.form = editMonitorForm(c, m.detail)
			} else {
				m.deletion = &deletePrompt{name: c.Name}
			}
		}
	case "w":
		m = m.openMaintenance()
	case "v":
		return m.toggleReveal()
	case "m", "u":
		if m.hasSelection() {
			svc := m.snap.Services[m.selected]
			if msg.String() == "m" {
				m.mute = &mutePrompt{name: svc.Check.Name, wasAlerting: svc.KnownAlerting}
			} else if svc.Muted(m.now()) {
				m.generation++
				return m, m.unmute(svc.Check.Name)
			}
		}
	case "up", "k":
		if m.detail {
			m = m.scrollIncidents(-1, false)
		} else {
			m = m.move(-1)
			m.incidentName = ""
		}
	case "down", "j":
		if m.detail {
			m = m.scrollIncidents(1, false)
		} else {
			m = m.move(1)
			m.incidentName = ""
		}
	case "pgup", "pgdown":
		if m.detail || m.splitMode() {
			delta := 1
			if msg.String() == "pgup" {
				delta = -1
			}
			m = m.scrollIncidents(delta, true)
		}
	case "enter":
		if m.hasSelection() && !m.splitMode() {
			m.detail = true
			m.incidentName = ""
		}
	case "esc", "backspace":
		if msg.String() == "esc" && !m.detail {
			m.filter = listFilter{}
		}
		m.detail = false
	}
	return m, nil
}

// notifyChild is the popup open over the notifications view, if any, and the
// width it keeps whatever field has the focus.
func (m Model) notifyChild(now time.Time) (string, int) {
	minimum := 0
	switch {
	case m.smtpForm != nil:
		f := *m.smtpForm
		for focus := range f.values {
			if (f.editing && focus == 0) || (focus == 5 && !m.snap.HasKey) {
				continue
			}
			f.focus, f.cursor = focus, 0
			minimum = max(minimum, modalContentWidth(renderSMTPForm(f, m.snap, now, m.err)))
		}
		return renderSMTPForm(*m.smtpForm, m.snap, now, m.err), minimum
	case m.smtpDelete != nil:
		return renderSMTPDelete(*m.smtpDelete, m.snap, now, m.err), 0
	case m.channelForm != nil:
		f := *m.channelForm
		for focus := 0; focus < f.fieldCount(); focus++ {
			if f.editing && focus == 0 {
				continue
			}
			f.focus, f.cursor = focus, 0
			minimum = max(minimum, modalContentWidth(renderChannelForm(f, m.snap, now, m.err)))
		}
		return renderChannelForm(*m.channelForm, m.snap, now, m.err), minimum
	case m.channelDelete != nil:
		return renderChannelDelete(*m.channelDelete, m.snap, now, m.err), 0
	case m.channelTest != nil:
		return renderChannelTest(*m.channelTest, m.snap, now, m.err), 0
	}
	return "", 0
}

// View renders the list, or the detail of the selected service.
func (m Model) View() tea.View {
	now := m.now()
	shown, shownSelected, filter := m.filtered()
	content := renderList(shown, shownSelected, now, m.err, m.width, filter)
	split := m.splitMode()
	scroll := m.scroll()
	scroll.ping = m.pingView()
	if split {
		content = renderSplit(shown, shownSelected, now, m.err, m.width, m.height, m.noticeLine(), scroll, filter)
	} else if m.detail && m.hasSelection() {
		opts := fullDetail()
		opts.cursor, opts.offset, opts.ping = scroll.cursor, scroll.offset, scroll.ping
		content, _ = fitDetail(m.snap, m.snap.Services[m.selected], now, m.err, m.width, m.detailHeight(), opts)
	}
	if m.help && !helpPopupFits(m.width, m.height) {
		content = renderKeys(m.destination != "")
	}
	if m.notify {
		content, split = m.renderNotifications(now), false
	}
	modal := ""
	if m.form != nil || m.deletion != nil || m.mute != nil || m.maint != nil || m.defaults != nil || m.pingError != nil {
		screen, minimum := "", 0
		switch {
		case m.pingError != nil:
			screen = renderPingError(*m.pingError, m.width)
		case m.form != nil && m.form.pingToken != "":
			screen = renderPingURLScreen(*m.form, m.snap, hostOnly(m.hostname), m.width)
		case m.defaults != nil:
			screen = renderDefaults(*m.defaults, m.snap, now, m.err)
			minimum = defaultsMinWidth(*m.defaults, m.snap, now, m.err)
		case m.maint != nil:
			screen, minimum = m.maintenanceScreen(now), maintenanceMinWidth
		case m.mute != nil:
			screen = renderMute(*m.mute, m.snap, now, m.err)
			p := *m.mute
			p.custom = !p.custom
			minimum = modalContentWidth(renderMute(p, m.snap, now, m.err))
		case m.deletion != nil:
			screen = renderDelete(*m.deletion, m.snap, now, m.err)
		default:
			// The box keeps one size for every Type, focus and the token
			// question, so none of them moves it.
			var focusLine, height int
			for _, f := range formVariants(*m.form) {
				height = max(height, strings.Count(renderForm(f, m.snap, now, m.err), "\n")+1)
				for focus := 0; focus < focusCount; focus++ {
					if f.editing && focus == 0 {
						continue
					}
					f.focus, f.cursor = focus, 0
					minimum = max(minimum, modalContentWidth(renderForm(f, m.snap, now, m.err)))
				}
			}
			screen, focusLine = renderFormFocus(*m.form, m.snap, now, m.err)
			screen = scrollForm(padFormHeight(screen, height), focusLine, m.height)
		}
		title, body := splitModalScreen(screen)
		var fits bool
		modal, fits = renderModal(title, body, m.width, m.height, minimum)
		if !fits {
			content = screen
			split = false
		}
	}
	if m.notify {
		if child, minimum := m.notifyChild(now); child != "" {
			title, body := splitModalScreen(child)
			var fits bool
			if modal, fits = renderModal(title, body, m.width, m.height, minimum); !fits {
				content = child
			}
		}
	}
	if m.notice != "" && !split && !m.notify {
		content += "\n" + m.noticeLine()
	}
	position := -1
	if m.sweeping && sweepEnabled(m.snap, now, os.Getenv("NO_COLOR"), os.Getenv("TERM")) {
		position = m.sweepPosition
	}
	rule := renderHostRule(m.snap, m.hostname, now, m.width, position)
	if split {
		rule = renderHostRuleCells(m.snap, m.hostname, now, m.width, position)
	}
	header := renderTitleWidth(m.snap, m.width)
	if filter.active() && !filter.open {
		header = ansi.Truncate(header+"  "+helpStyle.Render(filter.summary()), max(m.width, ansi.StringWidth(header)), "")
	}
	content = header + "\n" + rule + "\n" + content
	if modal != "" || (m.help && helpPopupFits(m.width, m.height)) {
		content = padForPopup(content, m.popupPadHeight(modal))
	}
	if m.destination != "" {
		footer := helpStyle.Render("remote · " + displayText(m.destination))
		if m.err != nil {
			footer = warnStyle.Render("●") + helpStyle.Render(" disconnected · "+displayText(m.destination)+" — retrying (r retry · q quit)")
		} else if m.loaded {
			footer = upStyle.Render("●") + helpStyle.Render(" remote · "+displayText(m.destination))
		}
		if m.width > 0 {
			footer = ansi.Truncate(footer, m.width, "")
		}
		content += "\n" + footer
	} else {
		content += "\n" + helpStyle.Render("database: "+m.databasePath)
	}
	if split {
		content = truncateLines(content, m.width)
	}
	if m.help && helpPopupFits(m.width, m.height) {
		if m.width > 0 {
			content = truncateLines(content, m.width)
		}
		content = overlayPopup(content, renderHelpPopup(m.splitMode(), m.destination != ""), m.width, m.height)
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

// popupPadHeight is the height to pad the content to before a popup. Before
// the terminal size is known it grows to hold the tallest box between the title
// row and the footer, so the box covers neither.
func (m Model) popupPadHeight(boxes ...string) int {
	if m.height > 0 {
		return m.height
	}
	height := 0
	for _, box := range boxes {
		if box != "" {
			height = max(height, popupHeight+2, strings.Count(box, "\n")+1+3)
		}
	}
	return height
}

// Run shows the TUI until the user quits or ctx is done.
func Run(ctx context.Context, s *store.Store, options ...Options) error {
	return RunStore(ctx, s, options...)
}

// RunStore runs the TUI against any Store, such as a remote server client.
func RunStore(ctx context.Context, s Store, options ...Options) error {
	p := tea.NewProgram(New(s, time.Now, options...), tea.WithContext(ctx))
	_, err := p.Run()
	if err != nil && ctx.Err() != nil && errors.Is(err, tea.ErrProgramKilled) {
		return nil
	}
	return err
}
