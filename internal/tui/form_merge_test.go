package tui

import (
	"strings"
	"testing"

	"github.com/giulianotesta7/up/internal/config"
)

func TestOrderIncludesAdvancedForHTTPOnly(t *testing.T) {
	http := newMonitorForm()
	if got := http.order(); got[len(got)-1] != focusAdvanced || got[0] != focusType {
		t.Fatalf("HTTP order %v: want Type first and Advanced last", got)
	}
	tcp := newMonitorForm()
	tcp.setKind(kindTCP)
	for _, focus := range tcp.order() {
		if focus >= focusAdvanced {
			t.Fatalf("TCP order %v has an Advanced stop", tcp.order())
		}
	}
	open := newMonitorForm()
	open.adv.open = true
	if got := open.order(); got[len(got)-1] != focusCount+len(open.adv.stops())-1 {
		t.Fatalf("open fold order %v lacks its fields", got)
	}
}

func TestSwitchingTypeResetsAdvancedAndTCPIgnoresIt(t *testing.T) {
	f := newMonitorForm()
	f.adv = advancedForm{open: true, method: "POST", expect: "ok"}
	f.setKind(kindTCP)
	if f.adv.configured() || f.adv.open {
		t.Fatalf("advanced survived the switch to TCP: %+v", f.adv)
	}
	f.values[0], f.values[1], f.port = "db", "db.example.com", "5432"
	f.adv.expect = "stale"
	c, err := f.checkWith([]byte("ignored"))
	if err != nil || c.Kind != config.CheckTCP || c.ExpectText != "" || c.Method != "" || len(c.Headers) != 0 {
		t.Fatalf("TCP check %+v, %v", c, err)
	}
	f.setKind("")
	if f.adv.configured() || f.values[4] != "200" {
		t.Fatalf("switching back kept TCP state: %+v", f)
	}
}

func TestMergedFormViewsPerKind(t *testing.T) {
	_, m := formStore(t)
	m, _ = press(t, m, keyN)
	requireText(t, m, "Type: ‹ HTTP ›", "▸ Advanced")
	m, _ = press(t, m, keyShiftTab, keyRight)
	if out := view(m); strings.Contains(out, "Advanced") || !strings.Contains(out, "Type: ‹ TCP ›") {
		t.Fatalf("TCP form shows Advanced:\n%s", out)
	}
}
