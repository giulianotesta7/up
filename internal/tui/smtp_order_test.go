package tui

import (
	"strings"
	"testing"
)

func TestSMTPNoLoginBeforeUsername(t *testing.T) {
	_, m := smtpSetup(t)
	m, _ = press(t, m, keyS, keyN, keyTab, keyTab, keyTab)
	text := view(m)
	tls := strings.Index(text, "TLS:")
	mode := strings.Index(text, "No login required:")
	user := strings.Index(text, "Username:")
	if tls < 0 || mode <= tls || user <= mode {
		t.Fatalf("expected TLS, No login required, Username in that order:\n%s", text)
	}
	m, _ = press(t, m, keyTab)
	requireText(t, m, "> No login required:", "←/→ choose")
	m, _ = press(t, m, keyTab)
	requireText(t, m, "> Username:")
	m, _ = press(t, m, keyShiftTab, keyRight, keyTab)
	requireText(t, m, "> Sender:", "Username: (not required)", "Password: (not required)")
	m, _ = press(t, m, keyShiftTab)
	requireText(t, m, "> No login required:")
}
