package tui

import (
	"strings"
	"testing"
	"time"
)

func TestDetailSectionsAreSpacedAndIndented(t *testing.T) {
	m, _ := press(t, modelWith(seededSnapshot(t, now.Add(-5*time.Second))), keyDown, keyEnter)
	out := view(m)
	for _, want := range []string{
		"\n\nSTATUS\n  Status      DOWN",
		"  Last error  " + reason,
		"  Down since  Oct 25 03:44:00 (1m ago)",
		"\n\nCONFIG\n  Interval    1m0s\n  Timeout     10s\n  Expected    204\n  Channel     none\n",
		"\n\nLATENCY\n",
		"\n\nRECENT OUTAGES\n  Oct 25 03:44:00 → ongoing",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("detail missing %q:\n%s", want, out)
		}
	}
	for _, old := range []string{"Interval:", "Status:", "Recent outages:"} {
		if strings.Contains(out, old) {
			t.Errorf("detail still has %q", old)
		}
	}
}

func TestDetailHeadersUseHeaderStyle(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	m, _ := press(t, modelWith(seededSnapshot(t, now)), keyEnter)
	if !strings.Contains(m.View().Content, headerStyle.Render("CONFIG")) {
		t.Fatal("CONFIG header is not dim bold")
	}
}
