package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
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

func editHookForm(c config.Channel) hookForm {
	return hookForm{
		authName: c.WebhookAuthName, origin: c.WebhookOrigin,
		url: hookField{set: !c.WebhookURL.Empty()}, auth: hookField{set: !c.WebhookAuth.Empty()},
	}
}

// protected is the protected value of a row, if it has one.
func (f *channelForm) protected(r channelRow) *hookField {
	switch r {
	case rowURL:
		return &f.hook.url
	case rowAuth:
		return &f.hook.auth
	case rowToken:
		return &f.push.token
	}
	return nil
}

// actions are the choices for a stored value. Only the webhook auth value
// and the ntfy access token are optional, so only they can be cleared.
func (f channelForm) actions(r channelRow) []config.Edit {
	if r == rowAuth || (r == rowToken && f.kind() == config.KindNtfy) {
		return []config.Edit{config.EditKeep, config.EditReplace, config.EditClear}
	}
	return []config.Edit{config.EditKeep, config.EditReplace}
}

func editLabel(e config.Edit) string {
	return map[config.Edit]string{config.EditKeep: "keep", config.EditReplace: "replace", config.EditClear: "clear"}[e]
}

func (f *channelForm) protectedEdit(msg tea.KeyPressMsg) {
	r := f.row()
	p := f.protected(r)
	if p.set && (p.edit != config.EditReplace || p.text == "") {
		switch msg.String() {
		case "left", "right":
			actions := f.actions(r)
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
	editText(&p.text, &f.cursor, func(e *monitorForm) { e.edit(msg) })
	p.typed()
}

// typed makes typed text a replacement, whether or not a value is stored,
// so a typed value is never ignored on save. Nothing stored and nothing typed
// stays keep (no value).
func (p *hookField) typed() {
	switch {
	case p.text != "" && p.edit != config.EditReplace:
		p.edit = config.EditReplace
	case p.text == "" && !p.set:
		p.edit = config.EditKeep
	}
}

func (f *channelForm) protectedInsert(text string) {
	p := f.protected(f.row())
	if p.set && p.edit != config.EditReplace {
		if p.edit == config.EditClear {
			return
		}
		p.edit, f.cursor = config.EditReplace, 0
	}
	editText(&p.text, &f.cursor, func(e *monitorForm) { e.insert(text) })
	p.typed()
}

// webhookChannel builds the channel to save, sealing new protected values to
// the daemon's public key like an SMTP password.
func (f channelForm) webhookChannel(snap Snapshot) (config.Channel, error) {
	h := f.hook
	c := config.Channel{Name: f.values[0], Kind: config.KindWebhook, WebhookAuthName: strings.TrimSpace(h.authName)}
	if (h.url.text != "" || h.auth.text != "") && !snap.HasKey {
		return c, errors.New("webhook values need the encryption key: start `up serve` once, then come back")
	}
	if err := f.webhookURL(&c, snap, "URL is required"); err != nil {
		return c, err
	}
	var err error
	switch {
	case c.WebhookAuthName == "" && f.editing && h.auth.set:
		// No header name leaves nowhere to send the value.
		c.WebhookAuthEdit = config.EditClear
	case h.auth.edit == config.EditReplace || h.auth.text != "":
		if h.auth.text == "" {
			return c, errors.New("new auth value is empty (switch to keep or clear)")
		}
		c.WebhookAuthEdit = config.EditReplace
		if c.WebhookAuth, err = sealWebhook(snap, "auth value", h.auth.text); err != nil {
			return c, err
		}
	case h.auth.edit == config.EditClear:
		c.WebhookAuthEdit = config.EditClear
	}
	return c, nil
}

// webhookURL sets the origin and sealed URL of a webhook, discord or slack
// channel; a kept URL sends back the stored origin.
func (f channelForm) webhookURL(c *config.Channel, snap Snapshot, required string) error {
	u := f.hook.url
	if f.editing && u.edit == config.EditKeep && u.text == "" {
		c.WebhookOrigin, c.WebhookURLEdit = f.hook.origin, config.EditKeep
		return nil
	}
	if u.text == "" {
		return errors.New(required)
	}
	var err error
	if c.WebhookOrigin, err = config.WebhookOrigin(u.text); err != nil {
		return err
	}
	if !snap.HasKey {
		return errors.New("webhook URL needs the encryption key: start `up serve` once, then come back")
	}
	c.WebhookURLEdit = config.EditReplace
	c.WebhookURL, err = sealWebhook(snap, "URL", u.text)
	return err
}

func sealWebhook(snap Snapshot, what, text string) (secret.Sealed, error) {
	s, err := secret.Seal(snap.PublicKey, text)
	if err != nil {
		return s, fmt.Errorf("sealing the webhook %s failed", what)
	}
	return s, nil
}

// channelSummary is the parenthesised channel description in monitor details.
func channelSummary(c config.Channel) string {
	if c.Kind != config.KindEmail {
		return fmt.Sprintf("(%s → %s)", c.Kind, c.SendsTo())
	}
	noun := "recipients"
	if len(c.Recipients) == 1 {
		noun = "recipient"
	}
	return fmt.Sprintf("(%s via %s → %d %s)", c.Kind, c.SMTPServer, len(c.Recipients), noun)
}
