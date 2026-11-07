package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
)

// noKeyHint takes the hint slot of every kind with a protected value until
// the daemon has created its key.
const noKeyHint = "Secrets need the encryption key: start `up serve` once, then come back."

// pushForm is the ntfy, telegram and gotify half of channelForm. Discord and
// slack use the webhook URL of hookForm. The server URL and priority are
// kept per kind (pushSlot), so cycling Kind never shows ntfy's default
// server or priority as gotify's.
type pushForm struct {
	server, priority [2]string
	topic, chat      string
	token            hookField
}

// pushSlot is the index of a kind's server URL and priority in pushForm.
func pushSlot(kind string) int {
	if kind == config.KindGotify {
		return 1
	}
	return 0
}

func newPushForm() pushForm {
	return pushForm{server: [2]string{config.DefaultNtfyServer, ""}, priority: [2]string{config.DefaultNtfyPriority, config.DefaultGotifyPriority}}
}

func editPushForm(c config.Channel) pushForm {
	p := newPushForm()
	p.server[0] = ""
	slot := pushSlot(c.Kind)
	p.server[slot] = c.ServerURL
	if c.Priority != "" {
		p.priority[slot] = c.Priority
	}
	p.topic, p.chat = c.Topic, c.ChatID
	p.token = hookField{set: !c.Token.Empty()}
	return p
}

// priorities are the ‹ › choices of a kind's Priority row.
func priorities(kind string) []string {
	if kind != config.KindGotify {
		return config.NtfyPriorities
	}
	levels := make([]string, 0, config.MaxGotifyPriority+1)
	for i := range config.MaxGotifyPriority + 1 {
		levels = append(levels, strconv.Itoa(i))
	}
	return levels
}

func tokenLabel(kind string) string {
	switch kind {
	case config.KindTelegram:
		return "Bot token"
	case config.KindGotify:
		return "App token"
	}
	return "Access token"
}

// pushHint is the hint of an ntfy, telegram, discord, slack or gotify form.
func (f channelForm) pushHint() string {
	switch f.kind() {
	case config.KindNtfy:
		if t := f.push.token; (!t.set && t.text == "") || (t.set && t.edit == config.EditClear) {
			return helpStyle.Render("No access token: anyone who knows the topic can read the alerts.")
		}
	case config.KindTelegram:
		return helpStyle.Render("Chat ID: a number like -1001234567890, or @name for a public channel.")
	case config.KindGotify:
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(f.push.server[1])), "http://") {
			return warnStyle.Render("! Plain http: the app token travels unencrypted.")
		}
	}
	return ""
}

// pushChannel builds an ntfy, telegram, discord, slack or gotify channel,
// checking the rows top to bottom so the error names the first one to fix.
// Formats only the store can judge (topic, chat ID) are left to it.
func (f channelForm) pushChannel(snap Snapshot) (config.Channel, error) {
	kind, p := f.kind(), f.push
	c := config.Channel{Name: f.values[0], Kind: kind}
	var err error
	switch kind {
	case config.KindDiscord, config.KindSlack:
		return c, f.webhookURL(&c, snap, "webhook URL is required")
	case config.KindNtfy, config.KindGotify:
		raw := strings.TrimSpace(p.server[pushSlot(kind)])
		if raw == "" {
			return c, errors.New("server URL is required")
		}
		if c.ServerURL, err = config.ServerURL(raw); err != nil {
			return c, err
		}
	}
	if kind == config.KindNtfy {
		if c.Topic = strings.TrimSpace(p.topic); c.Topic == "" {
			return c, errors.New("topic is required")
		}
	}
	if c.Token, c.TokenEdit, err = f.pushToken(snap); err != nil {
		return c, err
	}
	if kind == config.KindTelegram {
		if c.ChatID = strings.TrimSpace(p.chat); c.ChatID == "" {
			return c, errors.New("chat ID is required")
		}
		return c, nil
	}
	// The kind's default is stored as empty, which means the same.
	if priority := p.priority[pushSlot(kind)]; priority != defaultPriority(kind) {
		c.Priority = priority
	}
	return c, nil
}

func defaultPriority(kind string) string {
	if kind == config.KindGotify {
		return config.DefaultGotifyPriority
	}
	return config.DefaultNtfyPriority
}

// pushToken resolves the token row: keep, clear (ntfy only), nothing (an
// ntfy channel without a token) or a new value sealed to the daemon's key.
func (f channelForm) pushToken(snap Snapshot) (secret.Sealed, config.Edit, error) {
	kind, t := f.kind(), f.push.token
	what := strings.ToLower(tokenLabel(kind))
	text := strings.TrimSpace(t.text)
	switch {
	case t.set && t.edit == config.EditKeep && text == "":
		return secret.Sealed{}, config.EditKeep, nil
	case t.set && t.edit == config.EditClear:
		return secret.Sealed{}, config.EditClear, nil
	case text == "" && t.set:
		switch kind {
		case config.KindNtfy:
			return secret.Sealed{}, config.EditKeep, fmt.Errorf("new %s is empty (switch to keep or clear)", what)
		default:
			return secret.Sealed{}, config.EditKeep, fmt.Errorf("new %s is empty (switch to keep)", what)
		}
	case text == "" && kind == config.KindNtfy:
		return secret.Sealed{}, config.EditKeep, nil
	case text == "":
		return secret.Sealed{}, config.EditKeep, fmt.Errorf("%s is required", what)
	case kind == config.KindTelegram && !config.ValidTelegramToken(text):
		return secret.Sealed{}, config.EditKeep, errors.New("bot token must look like 123456:ABC-def")
	case !snap.HasKey:
		return secret.Sealed{}, config.EditKeep, fmt.Errorf("%s needs the encryption key: start `up serve` once, then come back", what)
	}
	sealed, err := secret.Seal(snap.PublicKey, text)
	if err != nil {
		return sealed, config.EditKeep, fmt.Errorf("sealing the %s failed", what)
	}
	return sealed, config.EditReplace, nil
}
