package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
)

// newPushChannel opens the New channel form, names it and moves the focus to
// Kind, pressing right presses times.
func newPushChannel(t *testing.T, m Model, name string, presses int) Model {
	t.Helper()
	m, _ = press(t, m, keyC, keyN)
	m = typeText(t, m, name)
	m, _ = press(t, m, keyTab)
	for range presses {
		m, _ = press(t, m, keyRight)
	}
	return m
}

// kindPresses is how many right presses from email reach each kind.
var kindPresses = map[string]int{config.KindEmail: 0, config.KindWebhook: 1, config.KindNtfy: 2, config.KindTelegram: 3, config.KindDiscord: 4, config.KindSlack: 5, config.KindGotify: 6}

func onlyChannel(t *testing.T, s Store) config.Channel {
	t.Helper()
	list, err := s.Channels(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("channels %v %v", list, err)
	}
	return list[0]
}

func noChannels(t *testing.T, s Store) {
	t.Helper()
	if list, err := s.Channels(context.Background()); err != nil || len(list) != 0 {
		t.Fatalf("rejected save stored %v %v", list, err)
	}
}

func TestChannelKindCyclesAllKindsInOrder(t *testing.T) {
	m := channelServers(t)
	m = newPushChannel(t, m, "ops", 0)
	want := []string{"email", "webhook", "ntfy", "telegram", "discord", "slack", "gotify", "email"}
	for i, kind := range want {
		if i > 0 {
			m, _ = press(t, m, keyRight)
		}
		requireText(t, m, "Kind: ‹ "+kind+" ›")
	}
	for i := len(want) - 2; i >= 0; i-- {
		m, _ = press(t, m, keyLeft)
		requireText(t, m, "Kind: ‹ "+want[i]+" ›")
	}
}

func TestChannelKindRowsAndHints(t *testing.T) {
	labels := []string{"SMTP server:", "Recipients:", "URL:", "Auth header name:", "Auth value:", "Server URL:", "Topic:", "Access token:", "Priority:", "Bot token:", "Chat ID:", "Webhook URL:", "App token:"}
	for _, tc := range []struct {
		kind  string
		rows  []string
		shown []string
	}{
		{config.KindEmail, []string{"SMTP server:", "Recipients:"}, []string{"SMTP server: ‹ gmail ›", "comma-separated"}},
		{config.KindWebhook, []string{"URL:", "Auth header name:", "Auth value:"}, []string{"Sends a JSON POST. Any 2xx counts as delivered; no retries."}},
		{config.KindNtfy, []string{"Server URL:", "Topic:", "Access token:", "Priority:"}, []string{"Server URL: https://ntfy.sh", "Priority: ‹ default ›", "No access token: anyone who knows the topic can read the alerts."}},
		{config.KindTelegram, []string{"Bot token:", "Chat ID:"}, []string{"Chat ID: a number like -1001234567890, or @name for a public channel."}},
		{config.KindDiscord, []string{"Webhook URL:"}, nil},
		{config.KindSlack, []string{"Webhook URL:"}, nil},
		{config.KindGotify, []string{"Server URL:", "App token:", "Priority:"}, []string{"Priority: ‹ 5 ›"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			_, m := smtpSetup(t)
			if err := m.store.AddSMTPServer(context.Background(), config.SMTPServer{Name: "gmail", Host: "localhost", Port: 25, TLS: config.TLSNone, Sender: "up@example.com"}); err != nil {
				t.Fatal(err)
			}
			m = reload(t, m)
			m = newPushChannel(t, m, "ops", kindPresses[tc.kind])
			requireText(t, m, append(append([]string{"Name: ops", "Kind: ‹ " + tc.kind + " ›"}, tc.rows...), tc.shown...)...)
			for _, label := range labels {
				if strings.Contains(" "+strings.Join(tc.rows, " "), " "+label) {
					continue
				}
				for _, line := range screenLines(m) {
					if strings.Contains(line, "  "+label) || strings.Contains(line, "> "+label) {
						t.Fatalf("%s shows %q:\n%s", tc.kind, label, view(m))
					}
				}
			}
			if tc.kind == config.KindDiscord || tc.kind == config.KindSlack {
				requireNoText(t, m, "comma-separated", "No access token", "Chat ID: a number", "Plain http", "Sends a JSON POST")
			}
		})
	}
}

// channelBox is the New channel box's top row, bottom row, left column and
// width on screen.
func channelBox(t *testing.T, m Model) [4]int { return boxGeometry(t, m, "New channel") }

func TestChannelBoxKeepsItsSizeAcrossKinds(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 30}} {
		_, m := smtpSetup(t)
		m = sized(m, size[0], size[1])
		m = newPushChannel(t, m, "ops", 0)
		first := channelBox(t, m)
		for i := 1; i <= len(config.ChannelKinds); i++ {
			m, _ = press(t, m, keyRight)
			if got := channelBox(t, m); got != first {
				t.Fatalf("%dx%d: box %v at %s, want %v:\n%s", size[0], size[1], got, m.channelForm.values[1], first, view(m))
			}
			lines := screenLines(m)
			if len(lines) != size[1] {
				t.Fatalf("%dx%d: %d lines", size[0], size[1], len(lines))
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > size[0] {
					t.Fatalf("%dx%d: line too wide: %q", size[0], size[1], line)
				}
			}
		}
		// Focus does not move it either.
		m, _ = press(t, m, keyLeft) // gotify
		for range 4 {
			m, _ = press(t, m, keyTab)
			if got := channelBox(t, m); got != first {
				t.Fatalf("focus moved the box: %v, want %v", got, first)
			}
		}
	}
}

func TestNtfyChannelCreateEditAndClearToken(t *testing.T) {
	k, m := smtpSetup(t)
	m = newPushChannel(t, m, "phone", kindPresses[config.KindNtfy])
	m, _ = press(t, m, keyTab) // server URL, filled with the default
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "up-alerts")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "tk_SECRET")
	if v := view(m); strings.Contains(v, "tk_SECRET") || !strings.Contains(v, "Access token: •••••••••") {
		t.Fatalf("token not masked:\n%s", v)
	}
	requireNoText(t, m, "No access token")
	m, _ = press(t, m, keyTab, keyRight)
	requireText(t, m, "Priority: ‹ high ›")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	c := onlyChannel(t, m.store)
	if c.Kind != config.KindNtfy || c.ServerURL != config.DefaultNtfyServer || c.Topic != "up-alerts" || c.Priority != "high" || c.Token.Empty() {
		t.Fatalf("stored %+v", c)
	}
	if got, err := k.Open(c.Token); err != nil || got != "tk_SECRET" {
		t.Fatalf("token = %q %v", got, err)
	}
	requireText(t, m, "SENDS TO", "https://ntfy.sh · up-alerts", "phone  ntfy · https://ntfy.sh · topic up-alerts · priority high · access token")
	requireNoText(t, m, "tk_SECRET")

	// Edit: the kind is fixed and the token offers keep, replace and clear.
	m, _ = press(t, m, keyE)
	requireText(t, m, "Edit channel", "Kind: ‹ ntfy ›", "Server URL: https://ntfy.sh", "Topic: up-alerts", "Access token: set ‹ keep ›", "Priority: ‹ high ›")
	m, _ = press(t, m, keyRight)
	requireText(t, m, "Kind: ‹ ntfy ›")
	m, _ = press(t, m, keyTab, keyTab, keyTab)
	requireText(t, m, "> Access token: set ‹ keep ›", "←/→ keep/replace/clear")
	m, _ = press(t, m, keyRight)
	requireText(t, m, "Access token: set ‹ replace ›")
	m, _ = press(t, m, keyRight)
	requireText(t, m, "Access token: set ‹ clear ›", "No access token: anyone who knows the topic can read the alerts.")
	m, _ = press(t, m, keyRight)
	requireText(t, m, "Access token: set ‹ keep ›")
	m = typeText(t, m, "n3w-tok")
	requireText(t, m, "‹ replace ›")
	requireNoText(t, m, "n3w-tok")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	if got, _ := k.Open(onlyChannel(t, m.store).Token); got != "n3w-tok" {
		t.Fatalf("replace stored %q", got)
	}
	m, _ = press(t, m, keyE, keyTab, keyTab, keyTab, keyLeft)
	requireText(t, m, "‹ clear ›")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	if c := onlyChannel(t, m.store); !c.Token.Empty() || c.Topic != "up-alerts" {
		t.Fatalf("clear stored %+v", c)
	}
	requireText(t, m, "priority high · no access token")
}

func TestTelegramChannelCreateAndKeepReplaceOnly(t *testing.T) {
	k, m := smtpSetup(t)
	m = newPushChannel(t, m, "oncall-tg", kindPresses[config.KindTelegram])
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "123456:SECRET-tok_1")
	if v := view(m); strings.Contains(v, "SECRET") || !strings.Contains(v, "Bot token: •••") {
		t.Fatalf("bot token not masked:\n%s", v)
	}
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "-1001234567890")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	c := onlyChannel(t, m.store)
	if c.Kind != config.KindTelegram || c.ChatID != "-1001234567890" || c.Priority != "" {
		t.Fatalf("stored %+v", c)
	}
	if got, err := k.Open(c.Token); err != nil || got != "123456:SECRET-tok_1" {
		t.Fatalf("token = %q %v", got, err)
	}
	requireText(t, m, "telegram", "chat -1001234567890")
	requireNoText(t, m, "SECRET")

	m, _ = press(t, m, keyE, keyTab)
	requireText(t, m, "> Bot token: set ‹ keep ›", "←/→ keep/replace ·")
	m, _ = press(t, m, keyRight)
	requireText(t, m, "Bot token: set ‹ replace ›")
	m, _ = press(t, m, keyRight)
	requireText(t, m, "Bot token: set ‹ keep ›")
	m, _ = press(t, m, keyTab)
	m = replaceField(t, m, "-1001234567890", "@up_alerts")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	c = onlyChannel(t, m.store)
	if got, _ := k.Open(c.Token); got != "123456:SECRET-tok_1" || c.ChatID != "@up_alerts" {
		t.Fatalf("keep stored %+v %q", c, got)
	}
}

func TestDiscordAndSlackChannelsShowURLWhileTyping(t *testing.T) {
	for _, tc := range []struct{ kind, url, origin string }{
		{config.KindDiscord, "https://discord.com/api/webhooks/1187/SECRETPATH", "https://discord.com"},
		{config.KindSlack, "https://hooks.slack.com/services/T024/B07/SECRETPATH", "https://hooks.slack.com"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			k, m := smtpSetup(t)
			m = newPushChannel(t, m, "team", kindPresses[tc.kind])
			m, _ = press(t, m, keyTab)
			m = typeText(t, m, tc.url)
			requireText(t, m, "Webhook URL: "+tc.url)
			m, cmd := press(t, m, keyEnter)
			m = finishCommand(t, m, cmd)
			c := onlyChannel(t, m.store)
			if c.Kind != tc.kind || c.WebhookOrigin != tc.origin {
				t.Fatalf("stored %+v", c)
			}
			if got, err := k.Open(c.WebhookURL); err != nil || got != tc.url {
				t.Fatalf("url = %q %v", got, err)
			}
			requireText(t, m, tc.kind, tc.origin)
			requireNoText(t, m, "SECRETPATH")
			m, _ = press(t, m, keyE, keyTab)
			requireText(t, m, "> Webhook URL: "+tc.origin+" set ‹ keep ›", "←/→ keep/replace ·")
			m, _ = press(t, m, keyRight, keyRight)
			requireText(t, m, "‹ keep ›")
			requireNoText(t, m, "SECRETPATH")
		})
	}
}

func TestGotifyChannelWarnsAboutPlainHTTP(t *testing.T) {
	k, m := smtpSetup(t)
	m = newPushChannel(t, m, "nas", kindPresses[config.KindGotify])
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "https://nas.lan:8080")
	requireNoText(t, m, "Plain http")
	m = replaceField(t, m, "https://nas.lan:8080", "http://nas.lan:8080/")
	requireText(t, m, "! Plain http: the app token travels unencrypted.")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "Aa_SECRET")
	requireNoText(t, m, "Aa_SECRET")
	m, _ = press(t, m, keyTab, keyRight, keyRight, keyRight)
	requireText(t, m, "Priority: ‹ 8 ›")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	c := onlyChannel(t, m.store)
	if c.Kind != config.KindGotify || c.ServerURL != "http://nas.lan:8080" || c.Priority != "8" {
		t.Fatalf("stored %+v", c)
	}
	if got, err := k.Open(c.Token); err != nil || got != "Aa_SECRET" {
		t.Fatalf("token = %q %v", got, err)
	}
	m, _ = press(t, m, keyE, keyTab, keyTab)
	requireText(t, m, "> App token: set ‹ keep ›", "←/→ keep/replace ·")
	m, _ = press(t, m, keyRight, keyRight)
	requireNoText(t, m, "‹ clear ›")
}

// TestChannelErrorTakesHintSlotAndFocusesField covers every kind's required
// fields: the error replaces the hint on the same row and the focus moves
// to the named field. Nothing is stored.
func TestChannelErrorTakesHintSlotAndFocusesField(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		fill  func(t *testing.T, m Model) Model
		error string
		focus string
	}{
		{config.KindNtfy, func(t *testing.T, m Model) Model { return m }, "Cannot save channel: topic is required", "> Topic:"},
		{config.KindNtfy, func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keyTab)
			m = replaceField(t, m, config.DefaultNtfyServer, "")
			m, _ = press(t, m, keyTab)
			return typeText(t, m, "up-alerts")
		}, "Cannot save channel: server URL is required", "> Server URL:"},
		{config.KindTelegram, func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keyTab, keyTab)
			return typeText(t, m, "@up_alerts")
		}, "Cannot save channel: bot token is required", "> Bot token:"},
		{config.KindTelegram, func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keyTab)
			m = typeText(t, m, "SECRETPATH")
			m, _ = press(t, m, keyTab)
			return typeText(t, m, "@up_alerts")
		}, "Cannot save channel: bot token must look like 123456:ABC-def", "> Bot token:"},
		{config.KindTelegram, func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keyTab)
			return typeText(t, m, "123456:abc")
		}, "Cannot save channel: chat ID is required", "> Chat ID:"},
		{config.KindDiscord, func(t *testing.T, m Model) Model { return m }, "Cannot save channel: webhook URL is required", "> Webhook URL:"},
		{config.KindSlack, func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keyTab)
			return typeText(t, m, "ftp://user:SECRETPATH@host/x")
		}, "Cannot save channel: webhook URL must be http or https", "> Webhook URL:"},
		{config.KindGotify, func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keyTab)
			return typeText(t, m, "https://nas.lan")
		}, "Cannot save channel: app token is required", "> App token:"},
		{config.KindGotify, func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keyTab, keyTab)
			return typeText(t, m, "Aa_tok")
		}, "Cannot save channel: server URL is required", "> Server URL:"},
		{config.KindWebhook, func(t *testing.T, m Model) Model { return m }, "Cannot save channel: URL is required", "> URL:"},
	} {
		t.Run(tc.kind+" "+tc.focus, func(t *testing.T) {
			_, m := smtpSetup(t)
			m = sized(m, 80, 24)
			m = newPushChannel(t, m, "ops", kindPresses[tc.kind])
			// The hint slot is the two lines above the key help.
			hintRow := rowOf(screenLines(m), "esc cancel") - 2
			before := channelBox(t, m)
			m = tc.fill(t, m)
			m, cmd := press(t, m, keyEnter)
			if cmd != nil {
				t.Fatal("an invalid channel was saved")
			}
			lines := screenLines(m)
			if got := rowOf(lines, tc.error); got != hintRow {
				t.Fatalf("error on row %d, want hint row %d:\n%s", got, hintRow, view(m))
			}
			requireText(t, m, tc.focus)
			if strings.Contains(m.channelForm.err.Error(), "SECRETPATH") {
				t.Fatalf("error echoes the typed value: %v", m.channelForm.err)
			}
			if got := channelBox(t, m); got != before {
				t.Fatalf("error moved the box: %v, want %v", got, before)
			}
			noChannels(t, m.store)
		})
	}
}

func TestEmailStoreErrorTakesHintSlotAndFocusesField(t *testing.T) {
	m := channelServers(t)
	m = sized(m, 120, 30)
	m = newPushChannel(t, m, "ops", 0)
	hint := rowOf(screenLines(m), "comma-separated")
	box := channelBox(t, m)
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	lines := screenLines(m)
	// The error wraps into the spacer under the hint, not past it.
	if got := rowOf(lines, "Cannot save channel: store: add channel: at least one recipient is"); got != hint || !strings.Contains(lines[hint+1], "required") {
		t.Fatalf("error on row %d, want %d:\n%s", got, hint, view(m))
	}
	if got := channelBox(t, m); got != box {
		t.Fatalf("error moved the box: %v, want %v", got, box)
	}
	requireText(t, m, "> Recipients:")
	requireNoText(t, m, "comma-separated")
	noChannels(t, m.store)
	// Typing a valid recipient where the focus landed saves it.
	m = typeText(t, m, "ops@example.com")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	if c := onlyChannel(t, m.store); c.Recipients[0] != "ops@example.com" {
		t.Fatalf("stored %+v", c)
	}
}

func TestPushChannelsNeedTheKeyOnlyForSecrets(t *testing.T) {
	s, m := formStore(t) // no daemon key
	m = newPushChannel(t, m, "phone", kindPresses[config.KindNtfy])
	requireText(t, m, "Secrets need the encryption key: start `up serve` once, then come back.")
	m, _ = press(t, m, keyTab, keyTab)
	m = typeText(t, m, "up-alerts")
	m, cmd := press(t, m, keyEnter)
	requireText(t, finishCommand(t, m, cmd), "https://ntfy.sh · up-alerts")
	if c := onlyChannel(t, s); c.Kind != config.KindNtfy || !c.Token.Empty() {
		t.Fatalf("stored %+v", c)
	}

	_, m = formStore(t)
	m = newPushChannel(t, m, "tg", kindPresses[config.KindTelegram])
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "123456:abc")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "@up_alerts")
	m, cmd = press(t, m, keyEnter)
	if cmd != nil {
		t.Fatal("saved without an encryption key")
	}
	requireText(t, m, "Cannot save channel", "encryption key", "> Bot token:")
}

func TestChannelListSendsToColumn(t *testing.T) {
	s, m := formStore(t)
	ctx := context.Background()
	sealed := secret.Sealed{Key: []byte{1}, Data: []byte{2}}
	if err := s.AddSMTPServer(ctx, config.SMTPServer{Name: "gmail", Host: "localhost", Port: 25, TLS: config.TLSNone, Sender: "up@example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []config.Channel{
		{Name: "ops-mail", Kind: config.KindEmail, SMTPServer: "gmail", Recipients: []string{"ana@acme.com", "b@acme.com"}},
		{Name: "ops-hook", Kind: config.KindWebhook, WebhookOrigin: "https://hooks.acme.com", WebhookURL: sealed, WebhookURLEdit: config.EditReplace},
		{Name: "phone", Kind: config.KindNtfy, ServerURL: "https://ntfy.sh", Topic: "up-alerts-payments-and-billing-eu", Priority: "high"},
		{Name: "oncall-tg", Kind: config.KindTelegram, ChatID: "-1001234567890", Token: sealed, TokenEdit: config.EditReplace},
		{Name: "team-discord", Kind: config.KindDiscord, WebhookOrigin: "https://discord.com", WebhookURL: sealed, WebhookURLEdit: config.EditReplace},
		{Name: "nas-gotify", Kind: config.KindGotify, ServerURL: "http://nas.lan:8080", Token: sealed, TokenEdit: config.EditReplace},
	} {
		if err := s.AddChannel(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	m = sized(reload(t, m), 120, 30)
	m, _ = press(t, m, keyC)
	lines := screenLines(m)
	header := rowOf(lines, "NAME")
	if got := strings.Join(strings.Fields(lines[header]), " "); got != "NAME KIND SENDS TO MONITORS LAST DELIVERY" {
		t.Fatalf("header %q", got)
	}
	requireNoText(t, m, "SMTP SERVER", "RECIPIENTS", "ORIGIN", "AUTH")
	for name, sendsTo := range map[string]string{
		"ops-mail": "gmail → ana@acme.com +1", "ops-hook": "https://hooks.acme.com", "phone": "https://ntfy.sh · up-alerts-payments-and-billing-eu",
		"oncall-tg": "chat -1001234567890", "team-discord": "https://discord.com", "nas-gotify": "http://nas.lan:8080",
	} {
		row := rowOf(lines, " "+name+" ")
		if row < 0 || !strings.Contains(lines[row], sendsTo) {
			t.Fatalf("%s row lacks %q:\n%s", name, sendsTo, view(m))
		}
	}
	// The selected channel's line under the table, per kind.
	for _, want := range []string{
		"nas-gotify  gotify · http://nas.lan:8080 · priority 5",
		"oncall-tg  telegram · chat -1001234567890",
		"ops-hook  webhook · https://hooks.acme.com · no auth",
		"ops-mail  email · gmail → ana@acme.com +1",
		"phone  ntfy · https://ntfy.sh · topic up-alerts-payments-and-billing-eu · priority high · no access token",
		"team-discord  discord · https://discord.com",
	} {
		requireText(t, m, want)
		m, _ = press(t, m, keyDown)
	}
	// 80 columns drop SENDS TO; the line under the table keeps it.
	m = sized(m, 80, 24)
	requireText(t, m, "MON", "team-discord  discord · https://discord.com")
	requireNoText(t, m, "SENDS TO")
	for _, line := range screenLines(m) {
		if ansi.StringWidth(line) > 80 {
			t.Fatalf("line wider than 80: %q", line)
		}
	}
}

func TestPushChannelTestSaysAcceptedLikeWebhook(t *testing.T) {
	s, m := testChannelModel(t, true)
	ctx := context.Background()
	if err := s.AddChannel(ctx, config.Channel{Name: "phone", Kind: config.KindNtfy, ServerURL: "https://ntfy.sh", Topic: "up"}); err != nil {
		t.Fatal(err)
	}
	m = reload(t, m)
	m, _ = press(t, m, keyDown) // mail, phone, zhook
	m, _ = press(t, m, keyT)
	requireText(t, m, "Channel: phone (ntfy)")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	claimed, _, _ := s.ClaimChannelTest(ctx, now)
	if err := s.CompleteChannelTest(ctx, claimed.ID, "", now); err != nil {
		t.Fatal(err)
	}
	requireText(t, reload(t, m), "✓ Accepted (HTTP 2xx)")
	requireNoText(t, reload(t, m), "mail server")
}

func TestMonitorDetailSummarizesPushChannel(t *testing.T) {
	s, m := formStore(t)
	ctx := context.Background()
	if err := s.AddChannel(ctx, config.Channel{Name: "phone", Kind: config.KindNtfy, ServerURL: "https://ntfy.sh", Topic: "up-alerts"}); err != nil {
		t.Fatal(err)
	}
	c := testChecks[0]
	c.Channel = "phone"
	if err := s.AddCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	m = sized(reload(t, m), 120, 35)
	requireText(t, m, "phone (ntfy → https://ntfy.sh · up-alerts)")
}
