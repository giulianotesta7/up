package tui

import (
	"github.com/charmbracelet/x/ansi"
	"testing"
)

func TestSelectorShowsOnlySelectedValue(t *testing.T) {
	for _, focused := range []bool{false, true} {
		if got := ansi.Strip(renderChoiceUnit("3", "failures in a row", thresholdChoices, focused)); got != "‹ 3 › failures in a row" {
			t.Fatalf("got %q", got)
		}
		if got := ansi.Strip(renderChoice("none", []string{"none", "infra", "ops-team"}, focused)); got != "‹ none ›" {
			t.Fatalf("got %q", got)
		}
	}
	for _, options := range [][]string{thresholdChoices, tlsChoices, smtpTLSOptions, {"gmail", "relay"}, {"email"}} {
		for _, focused := range []bool{false, true} {
			if got := ansi.Strip(renderChoice(options[0], options, focused)); got != "‹ "+options[0]+" ›" {
				t.Fatalf("unexpected alternatives: %q", got)
			}
		}
	}
}
