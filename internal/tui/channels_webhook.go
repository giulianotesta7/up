package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
)

// Webhook channel form rows, in channelForm.focus order. Rows 0 (name) and 1
// (kind) are shared with email channels.
const (
	hookURLRow  = 2
	hookNameRow = 3
	hookAuthRow = 4
	hookRows    = 5
)

// hookField is a protected webhook value. A stored value is never read back:
// the form only knows that one is set and what to do with it (keep, replace
// or clear). text holds the replacement while it is being typed.
type hookField struct {
	text string
	edit config.Edit
	set  bool
}

// hookForm is the webhook half of channelForm.
type hookForm struct {
	url, auth hookField
	authName  string
	// origin is the stored scheme+host, sent back when the URL is kept.
	origin string
}

func (f channelForm) isWebhook() bool { return f.values[1] == config.KindWebhook }

// fieldCount is how many rows tab cycles through.
func (f channelForm) fieldCount() int {
	if f.isWebhook() {
		return hookRows
	}
	return len(f.values)
}

// fieldText is the text of the focused row.
func (f *channelForm) fieldText() string {
	if f.isWebhook() && f.focus >= hookURLRow {
		return *f.hookText()
	}
	return f.values[f.focus]
}

func (f *channelForm) hookText() *string {
	switch f.focus {
	case hookURLRow:
		return &f.hook.url.text
	case hookNameRow:
		return &f.hook.authName
	case hookAuthRow:
		return &f.hook.auth.text
	}
	return &f.values[f.focus]
}

func (f *channelForm) hookProtected() *hookField {
	switch f.focus {
	case hookURLRow:
		return &f.hook.url
	case hookAuthRow:
		return &f.hook.auth
	}
	return nil
}

func editHookForm(c config.Channel) hookForm {
	return hookForm{
		authName: c.WebhookAuthName, origin: c.WebhookOrigin,
		url: hookField{set: !c.WebhookURL.Empty()}, auth: hookField{set: !c.WebhookAuth.Empty()},
	}
}

// hookActions are the choices for a stored value; a URL cannot be cleared.
func (f *channelForm) hookActions() []config.Edit {
	if f.focus == hookURLRow {
		return []config.Edit{config.EditKeep, config.EditReplace}
	}
	return []config.Edit{config.EditKeep, config.EditReplace, config.EditClear}
}

func (f *channelForm) hookEdit(msg tea.KeyPressMsg) {
	p := f.hookProtected()
	if p != nil && p.set && (p.edit != config.EditReplace || p.text == "") {
		switch msg.String() {
		case "left", "right":
			actions := f.hookActions()
			delta := 1
			if msg.String() == "left" {
				delta = -1
			}
			for i, a := range actions {
				if a == p.edit {
					p.edit = actions[(i+delta+len(actions))%len(actions)]
					break
				}
			}
			p.text, f.cursor = "", 0
			return
		}
		if msg.Text == "" || p.edit == config.EditClear {
			return
		}
		p.edit, f.cursor = config.EditReplace, 0
	}
	editor := monitorForm{focus: 0, cursor: f.cursor}
	text := f.hookText()
	editor.values[0] = *text
	editor.edit(msg)
	*text = editor.values[0]
	f.cursor = editor.cursor
	f.hookTyped()
}

// hookTyped makes typed text a replacement, whether or not a value is stored,
// so a typed value is never ignored on save. Nothing stored and nothing typed
// stays keep (no value).
func (f *channelForm) hookTyped() {
	p := f.hookProtected()
	switch {
	case p == nil:
	case p.text != "" && p.edit != config.EditReplace:
		p.edit = config.EditReplace
	case p.text == "" && !p.set:
		p.edit = config.EditKeep
	}
}

func (f *channelForm) hookInsert(text string) {
	if p := f.hookProtected(); p != nil && p.set && p.edit != config.EditReplace {
		if p.edit == config.EditClear {
			return
		}
		p.edit, f.cursor = config.EditReplace, 0
	}
	editor := monitorForm{focus: 0, cursor: f.cursor}
	t := f.hookText()
	editor.values[0] = *t
	editor.insert(text)
	*t = editor.values[0]
	f.cursor = editor.cursor
	f.hookTyped()
}

// webhookChannel builds the channel to save, sealing new protected values to
// the daemon's public key like an SMTP password.
func (f channelForm) webhookChannel(snap Snapshot) (config.Channel, error) {
	h := f.hook
	c := config.Channel{Name: f.values[0], Kind: config.KindWebhook, WebhookAuthName: strings.TrimSpace(h.authName)}
	if (h.url.text != "" || h.auth.text != "") && !snap.HasKey {
		return c, errors.New("webhook values need the encryption key: start `up serve` once, then come back")
	}
	seal := func(what, text string) (secret.Sealed, error) {
		s, err := secret.Seal(snap.PublicKey, text)
		if err != nil {
			return s, fmt.Errorf("sealing the webhook %s failed", what)
		}
		return s, nil
	}
	var err error
	switch {
	case f.editing && h.url.edit == config.EditKeep && h.url.text == "":
		c.WebhookOrigin, c.WebhookURLEdit = h.origin, config.EditKeep
	default:
		if h.url.text == "" {
			return c, errors.New("URL is required")
		}
		if c.WebhookOrigin, err = config.WebhookOrigin(h.url.text); err != nil {
			return c, err
		}
		c.WebhookURLEdit = config.EditReplace
		if c.WebhookURL, err = seal("URL", h.url.text); err != nil {
			return c, err
		}
	}
	switch {
	case c.WebhookAuthName == "" && f.editing && h.auth.set:
		// No header name leaves nowhere to send the value.
		c.WebhookAuthEdit = config.EditClear
	case h.auth.edit == config.EditReplace || h.auth.text != "":
		if h.auth.text == "" {
			return c, errors.New("new auth value is empty (switch to keep or clear)")
		}
		c.WebhookAuthEdit = config.EditReplace
		if c.WebhookAuth, err = seal("auth value", h.auth.text); err != nil {
			return c, err
		}
	case h.auth.edit == config.EditClear:
		c.WebhookAuthEdit = config.EditClear
	}
	return c, nil
}

// channelTarget is the list and detail summary of a channel: the SMTP server
// and recipients, or the webhook origin and auth header name. Never the URL.
func channelTarget(c config.Channel) (target, detail string) {
	if c.Kind == config.KindWebhook {
		auth := "no auth"
		if c.WebhookAuthName != "" {
			auth = c.WebhookAuthName
		}
		return c.WebhookOrigin, auth
	}
	return c.SMTPServer, strings.Join(c.Recipients, ", ")
}

// channelSummary is the parenthesised channel description in monitor details.
func channelSummary(c config.Channel) string {
	if c.Kind == config.KindWebhook {
		return fmt.Sprintf("(webhook → %s)", c.WebhookOrigin)
	}
	noun := "recipients"
	if len(c.Recipients) == 1 {
		noun = "recipient"
	}
	return fmt.Sprintf("(%s via %s → %d %s)", c.Kind, c.SMTPServer, len(c.Recipients), noun)
}

func (f channelForm) renderHookRows(b *strings.Builder, snap Snapshot) {
	cursor := func(text string, focused bool) string {
		if !focused {
			return text
		}
		runes := []rune(text)
		return string(runes[:f.cursor]) + accentStyle.Render("│") + string(runes[f.cursor:])
	}
	protected := func(p hookField, row int, shown, origin string) string {
		focused := f.focus == row
		if p.set && (p.edit != config.EditReplace || p.text == "") {
			label := map[config.Edit]string{config.EditKeep: "keep", config.EditReplace: "replace", config.EditClear: "clear"}[p.edit]
			var state string
			if origin != "" {
				state = origin + " " + helpStyle.Render("set")
			} else {
				state = helpStyle.Render("set")
			}
			return state + " " + renderChoice(label, nil, focused)
		}
		return cursor(shown, focused) + map[bool]string{true: " " + renderChoice("replace", nil, focused), false: ""}[p.set]
	}
	rows := [hookRows]struct{ label, value string }{
		{"Name", f.values[0]},
		{"Kind", renderChoice(f.values[1], config.ChannelKinds, f.focus == 1)},
		{"URL", protected(f.hook.url, hookURLRow, f.hook.url.text, f.hook.origin)},
		{"Auth header name", cursor(f.hook.authName, f.focus == hookNameRow)},
		{"Auth value", protected(f.hook.auth, hookAuthRow, strings.Repeat("•", len([]rune(f.hook.auth.text))), "")},
	}
	if f.focus == 0 {
		rows[0].value = cursor(f.values[0], true)
	}
	if !snap.HasKey {
		b.WriteString(warnStyle.Render("The URL and auth value need the encryption key: start `up serve` once, then come back.") + "\n")
	}
	for i, r := range rows {
		marker, label := " ", r.label
		if i == f.focus {
			marker, label = accentStyle.Render(">"), accentStyle.Render(label)
		}
		fmt.Fprintf(b, "%s %s: %s\n", marker, label, r.value)
	}
}

func (f channelForm) hookHelp() string {
	b := "Sends a generic JSON POST (not a native Slack or Discord format).\n" +
		"Any 2xx is delivered; no retries. The URL and auth value are stored\n" +
		"encrypted and never shown again. Type to replace a saved value."
	return "\n" + helpStyle.Render(b) + "\n"
}
