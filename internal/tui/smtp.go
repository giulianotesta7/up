package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
)

var smtpLabels = [...]string{"Name", "Host", "Port", "TLS", "Username", "Password", "Sender"}
var smtpTLSOptions = []string{string(config.TLSStartTLS), string(config.TLSImplicit), string(config.TLSNone)}

type smtpForm struct {
	values                       [7]string
	focus, cursor                int
	editing, hadPassword, saving bool
	err                          error
}
type smtpSaveMsg struct {
	generation uint64
	name       string
	err        error
}
type smtpDeleteMsg struct {
	generation uint64
	err        error
}

func newSMTPForm() *smtpForm {
	return &smtpForm{values: [7]string{"", "", "587", "starttls", "", "", ""}}
}
func editSMTPForm(s config.SMTPServer) *smtpForm {
	return &smtpForm{values: [7]string{s.Name, s.Host, strconv.Itoa(s.Port), string(s.TLS), s.Username, "", s.Sender}, focus: 1, cursor: len([]rune(s.Host)), editing: true, hadPassword: !s.Password.Empty()}
}
func (f *smtpForm) insert(text string) {
	if f.focus == 3 {
		return
	}
	editor := monitorForm{focus: 0, cursor: f.cursor}
	editor.values[0] = f.values[f.focus]
	editor.insert(text)
	f.values[f.focus] = editor.values[0]
	f.cursor = editor.cursor
}
func (f *smtpForm) edit(msg tea.KeyPressMsg) {
	if f.focus == 3 {
		switch msg.String() {
		case "left":
			f.values[3] = cycleChoice(smtpTLSOptions, f.values[3], -1)
		case "right":
			f.values[3] = cycleChoice(smtpTLSOptions, f.values[3], 1)
		}
		return
	}
	editor := monitorForm{focus: 0, cursor: f.cursor}
	editor.values[0] = f.values[f.focus]
	editor.edit(msg)
	f.values[f.focus] = editor.values[0]
	f.cursor = editor.cursor
}
func (m Model) handleSMTPFormKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	f := *m.smtpForm
	m.smtpForm = &f
	if f.saving {
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.smtpForm = nil
	case "tab", "shift+tab":
		delta := 1
		if msg.String() == "shift+tab" {
			delta = -1
		}
		for {
			f.focus = (f.focus + delta + len(f.values)) % len(f.values)
			if !(f.editing && f.focus == 0) && !(f.focus == 5 && !m.snap.HasKey) {
				break
			}
		}
		f.cursor = len([]rune(f.values[f.focus]))
	case "enter":
		port, err := strconv.Atoi(f.values[2])
		if err != nil {
			f.err = fmt.Errorf("port: %w", err)
			return m, nil
		}
		server := config.SMTPServer{Name: f.values[0], Host: f.values[1], Port: port, TLS: config.TLSMode(f.values[3]), Username: f.values[4], Sender: f.values[6]}
		password := f.values[5]
		key := bytes.Clone(m.snap.PublicKey)
		editing := f.editing
		f.saving = true
		m.generation++
		generation := m.generation
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
			defer cancel()
			sealed, err := secret.Seal(key, password)
			if err == nil {
				server.Password = sealed
				if editing {
					err = m.store.UpdateSMTPServer(ctx, server)
				} else {
					err = m.store.AddSMTPServer(ctx, server)
				}
			}
			return smtpSaveMsg{generation: generation, name: server.Name, err: err}
		}
	default:
		f.edit(msg)
	}
	return m, nil
}
func (m Model) handleSMTPKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.smtpForm != nil {
		return m.handleSMTPFormKey(msg)
	}
	if m.smtpDelete != nil {
		p := *m.smtpDelete
		m.smtpDelete = &p
		if p.saving {
			return m, nil
		}
		switch msg.String() {
		case "n", "esc":
			m.smtpDelete = nil
		case "y":
			p.saving = true
			m.generation++
			generation := m.generation
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
				defer cancel()
				return smtpDeleteMsg{generation: generation, err: m.store.DeleteSMTPServer(ctx, p.name)}
			}
		}
		return m, nil
	}
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		m.smtpScreen = false
		m.detail = false
	case "up", "k":
		if m.smtpSelected > 0 {
			m.smtpSelected--
		}
	case "down", "j":
		if m.smtpSelected < len(m.snap.SMTPServers)-1 {
			m.smtpSelected++
		}
	case "n":
		m.smtpForm = newSMTPForm()
	case "e", "d":
		if m.smtpSelected < len(m.snap.SMTPServers) {
			s := m.snap.SMTPServers[m.smtpSelected]
			if msg.String() == "e" {
				m.smtpForm = editSMTPForm(s)
			} else {
				m.smtpDelete = &deletePrompt{name: s.Name}
			}
		}
	}
	return m, nil
}
func renderSMTPList(snap Snapshot, selected int, now time.Time, loadErr error) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("SMTP servers") + "\n" + renderBanners(snap, now, loadErr))
	if len(snap.SMTPServers) == 0 {
		b.WriteString(helpStyle.Render("No SMTP servers configured.") + "\n")
	} else {
		widths := [5]int{4, 4, 4, 3, 4}
		rows := make([][5]string, len(snap.SMTPServers))
		for i, s := range snap.SMTPServers {
			rows[i] = [5]string{s.Name, s.Host, strconv.Itoa(s.Port), string(s.TLS), orDash(s.Username)}
			for j, v := range rows[i] {
				widths[j] = max(widths[j], ansi.StringWidth(v))
			}
		}
		line := func(marker string, cols [5]string, last string) string {
			var out strings.Builder
			out.WriteString(marker + "   ")
			for j, v := range cols {
				out.WriteString(v)
				out.WriteString(strings.Repeat(" ", widths[j]-ansi.StringWidth(v)+2))
			}
			out.WriteString(last)
			return out.String()
		}
		b.WriteString(headerStyle.Render(line(" ", [5]string{"NAME", "HOST", "PORT", "TLS", "USER"}, "PASSWORD")) + "\n")
		for i, s := range snap.SMTPServers {
			marker := " "
			cols := rows[i]
			if i == selected {
				marker = accentStyle.Render("▌")
				cols[0] = selectedNameStyle.Render(cols[0])
			}
			password := "—"
			if !s.Password.Empty() {
				if snap.HasKey && bytes.Equal(s.Password.Key, snap.PublicKey) {
					password = upStyle.Render("set")
				} else {
					password = warnStyle.Render("needs re-entry")
				}
			}
			b.WriteString(line(marker, cols, password) + "\n")
		}
	}
	b.WriteString("\n" + renderHelp("↑/↓ select (k/j) · n new · e edit · d delete · esc back · q quit · ctrl+c quit"))
	return b.String()
}
func renderSMTPForm(f smtpForm, snap Snapshot, now time.Time, loadErr error) string {
	var b strings.Builder
	title := "New SMTP server"
	if f.editing {
		title = "Edit SMTP server"
	}
	b.WriteString(titleStyle.Render(title) + "\n")
	if !snap.HasKey {
		b.WriteString(warnStyle.Render("Passwords need the encryption key: start `up serve` once, then come back.") + "\n")
	}
	b.WriteString(renderBanners(snap, now, loadErr))
	for i, label := range smtpLabels {
		marker := " "
		value := f.values[i]
		focused := i == f.focus
		if i == 3 {
			value = renderChoice(value, smtpTLSOptions, focused)
		} else if i == 5 && !snap.HasKey {
			value = helpStyle.Render("(unavailable until `up serve` runs)")
		} else {
			runes := []rune(value)
			if i == 5 {
				value = strings.Repeat("•", len(runes))
			}
			if focused {
				shown := []rune(value)
				value = string(shown[:f.cursor]) + accentStyle.Render("│") + string(shown[f.cursor:])
			}
			if i == 5 && f.editing && f.hadPassword && len(runes) == 0 {
				value += " " + helpStyle.Render("(leave empty to keep the current password)")
			}
		}
		if focused {
			marker = accentStyle.Render(">")
			label = accentStyle.Render(label)
		}
		fmt.Fprintf(&b, "%s %s: %s\n", marker, label, value)
	}
	if f.err != nil {
		b.WriteString("\n" + warnStyle.Render("Cannot save SMTP server: "+f.err.Error()) + "\n")
	}
	if f.saving {
		b.WriteString("\n" + renderHelp("Saving SMTP server… · ctrl+c quit"))
	} else if f.focus == 3 {
		b.WriteString("\n" + renderHelp("←/→ choose · tab/shift+tab focus · enter save · esc cancel · ctrl+c quit"))
	} else {
		b.WriteString("\n" + renderHelp("tab/shift+tab focus · enter save · esc cancel · ctrl+c quit"))
	}
	return b.String()
}
func renderSMTPDelete(p deletePrompt, snap Snapshot, now time.Time, loadErr error) string {
	content := titleStyle.Render("Delete SMTP server") + "\n" + renderBanners(snap, now, loadErr) + fmt.Sprintf("Delete SMTP server %q? This cannot be undone.\n", p.name)
	if p.err != nil {
		var in *store.InUseError
		message := "Cannot delete SMTP server: " + p.err.Error()
		if errors.As(p.err, &in) {
			message = fmt.Sprintf("Cannot delete SMTP server %q: %s", p.name, in.Reason())
		}
		content += "\n" + warnStyle.Render(message) + "\n"
	}
	if p.saving {
		content += "\n" + renderHelp("Deleting SMTP server… · ctrl+c quit")
	} else {
		content += "\n" + renderHelp("y delete · n cancel")
	}
	return content
}
