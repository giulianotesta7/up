package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
)

const (
	// DefaultTelegramAPI is the Telegram Bot API that telegram channels use.
	DefaultTelegramAPI = "https://api.telegram.org"
	pushUserAgent      = "up/push"
	// Longest text each service takes; longer text is cut.
	telegramMaxText   = 4096
	discordMaxContent = 2000
	// telegramMaxDescription bounds the refusal reason kept from Telegram.
	telegramMaxDescription = 120
)

// pushSecretName names the protected value each push kind needs.
var pushSecretName = map[string]string{
	config.KindNtfy:     "access token",
	config.KindTelegram: "bot token",
	config.KindGotify:   "app token",
	config.KindDiscord:  "webhook URL",
	config.KindSlack:    "webhook URL",
}

// ntfyTags are the emoji tags ntfy shows for each event.
var ntfyTags = map[store.Event]string{
	store.EventDown:      "rotating_light",
	store.EventRecovered: "white_check_mark",
	store.EventCert:      "warning",
}

// deliverPush is the ntfy, telegram, discord, slack and gotify transport
// behind Deliver. It sends the email's subject and body as each service
// expects them, treats a 2xx answer as delivered and everything else as an
// error, with no retries and no redirects. It logs its own failures and
// returns them, like deliverWebhook; errors start with the kind and carry
// no token, secret URL or response body (only Telegram's refusal reason).
func (n *Notifier) deliverPush(ctx context.Context, ch config.Channel, e event) error {
	fail := func(msg string) error {
		n.opts.Logger.Error(ch.Kind+" delivery failed", "channel", e.check.Channel, "error", msg)
		return errors.New(ch.Kind + " " + msg)
	}
	what := pushSecretName[ch.Kind]
	sealed := ch.Token
	if ch.Kind == config.KindDiscord || ch.Kind == config.KindSlack {
		sealed = ch.WebhookURL
	}
	var value string
	if !sealed.Empty() || ch.Kind != config.KindNtfy {
		if n.opts.Key == nil {
			return fail(what + " cannot be opened: no encryption key")
		}
		var err error
		value, err = n.opts.Key.Open(sealed)
		switch {
		case errors.Is(err, secret.ErrWrongKey):
			return fail(what + " was sealed to another key; enter it again")
		case err != nil:
			return fail("opening the " + what + " failed")
		case value == "":
			return fail("has no " + what)
		}
	}
	e.trace.know(value)
	subject, body := "", ""
	if e.test {
		m := testMessage(ch, "", n.opts.Hostname, time.Now())
		subject, body = m.Subject, m.Body
	} else {
		subject, body = n.content(ctx, e)
	}
	req, err := n.pushRequest(ctx, ch, value, subject, body, e)
	if err != nil {
		return fail(err.Error())
	}
	req.Header.Set("User-Agent", pushUserAgent)
	resp, err := webhookClient.Do(req)
	if err != nil {
		return fail(webhookFailure(err))
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, webhookReadCap))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := fmt.Sprintf("answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
		if ch.Kind == config.KindTelegram {
			if why := telegramDescription(answer, value); why != "" {
				msg += " (" + why + ")"
			}
		}
		return fail(msg)
	}
	event := "test"
	if !e.test {
		event = string(e.name())
	}
	n.opts.Logger.Info("alert sent", "check", e.check.Name, "channel", e.check.Channel, "event", event)
	return nil
}

// pushRequest builds the request for one kind. value is the opened token or
// webhook URL. Its errors never include value.
func (n *Notifier) pushRequest(ctx context.Context, ch config.Channel, value, subject, body string, e event) (*http.Request, error) {
	post := func(target string, payload any) (*http.Request, error) {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, errors.New("encoding the message failed")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("%s is not valid", pushSecretName[ch.Kind])
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}
	switch ch.Kind {
	case config.KindNtfy:
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.ServerURL+"/"+url.PathEscape(ch.Topic), strings.NewReader(body))
		if err != nil {
			return nil, errors.New("server URL is not valid")
		}
		priority, tag := ch.Priority, "test_tube"
		if priority == "" {
			priority = config.DefaultNtfyPriority
		}
		if !e.test {
			tag = ntfyTags[e.name()]
		}
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		req.Header.Set("Title", mime.QEncoding.Encode("utf-8", oneLine(subject)))
		req.Header.Set("Priority", priority)
		req.Header.Set("Tags", tag)
		if value != "" {
			req.Header.Set("Authorization", "Bearer "+value)
		}
		return req, nil
	case config.KindTelegram:
		if !config.ValidTelegramToken(value) {
			return nil, errors.New("bot token is not valid; enter it again")
		}
		base := n.opts.TelegramAPI
		if base == "" {
			base = DefaultTelegramAPI
		}
		return post(strings.TrimRight(base, "/")+"/bot"+value+"/sendMessage", struct {
			ChatID string `json:"chat_id"`
			Text   string `json:"text"`
		}{ch.ChatID, cut(subject+"\n\n"+body, telegramMaxText)})
	case config.KindDiscord:
		type mentions struct {
			Parse []string `json:"parse"`
		}
		// No mentions: text from a monitor must not ping anyone.
		return post(value, struct {
			Content         string   `json:"content"`
			AllowedMentions mentions `json:"allowed_mentions"`
		}{cut(subject+"\n"+body, discordMaxContent), mentions{Parse: []string{}}})
	case config.KindSlack:
		return post(value, struct {
			Text string `json:"text"`
		}{slackEscape(subject + "\n" + body)})
	case config.KindGotify:
		priority, err := strconv.Atoi(ch.Priority)
		if ch.Priority == "" || err != nil {
			priority, _ = strconv.Atoi(config.DefaultGotifyPriority)
		}
		req, err := post(ch.ServerURL+"/message", struct {
			Title    string `json:"title"`
			Message  string `json:"message"`
			Priority int    `json:"priority"`
		}{subject, body, priority})
		if err != nil {
			return nil, errors.New("server URL is not valid")
		}
		req.Header.Set("X-Gotify-Key", value)
		return req, nil
	}
	return nil, fmt.Errorf("kind %q has no transport", ch.Kind)
}

// telegramDescription is the reason in a Telegram refusal ("chat not
// found"), short, on one line and without the token; empty when the answer
// has none.
func telegramDescription(answer []byte, token string) string {
	var v struct {
		Description string `json:"description"`
	}
	if json.Unmarshal(answer, &v) != nil {
		return ""
	}
	why := store.FoldSpace(v.Description)
	if token != "" {
		why = strings.ReplaceAll(why, token, "[hidden]")
	}
	for _, prefix := range []string{"Bad Request: ", "Forbidden: ", "Unauthorized: ", "Not Found: "} {
		why = strings.TrimPrefix(why, prefix)
	}
	return cut(why, telegramMaxDescription)
}

// cut shortens s to at most limit runes, marking the cut with "…".
func cut(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit-1]) + "…"
}

func oneLine(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }

// slackEscape escapes the characters Slack reads as markup.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
