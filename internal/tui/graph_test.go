package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/giulianotesta7/up/internal/check"
)

func samples(ms ...int) []check.Result {
	var results []check.Result
	for _, n := range ms {
		status := check.StatusUp
		if n < 0 {
			status = check.StatusDown
			n = -n
		}
		results = append(results, check.Result{Status: status, Latency: time.Duration(n) * time.Millisecond})
	}
	return results
}

func TestSparkline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input []check.Result
		width int
		want  string
	}{
		{"empty", nil, 4, "    "},
		{"single", samples(150), 1, "▇"},
		{"padding and order", samples(150, 0), 4, "  ▁▇"},
		{"all down", samples(-1, -400), 3, " ✕✕"},
		{"flat", samples(10, 10, 10), 3, "▁▁▁"},
		{"newest window", samples(10, -1, 1000), 2, "✕▁"},
		{"zero width", samples(10), 0, ""},
		{"negative width", samples(10), -1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ansi.Strip(sparkline(tc.input, tc.width)); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// Nearest-rank p95 excludes one outlier in a 20-success window.
	input := append(samples(1000), samples(10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10)...)
	if got := ansi.Strip(sparkline(input, 20)); got != strings.Repeat("▁", 19)+"█" {
		t.Fatal(got)
	}
}

func TestGraphColors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input []check.Result
		slow  bool
	}{
		{"needs five successes", samples(21, 10, 10, 10), false},
		{"exactly twice", samples(20, 10, 10, 10, 10), false},
		{"above twice", samples(21, 10, 10, 10, 10), true},
		{"even median exactly twice", samples(30, 10, 10, 10, 20, 20), false},
		{"even median above twice", samples(31, 10, 10, 10, 20, 20), true},
		{"failure not a success", samples(21, 10, 10, 10, -1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := upStyle.Render("▂")
			if tc.slow {
				want = slowStyle.Render("▂")
			}
			if got := sparkline(tc.input, len(tc.input)); !strings.HasSuffix(got, want) {
				t.Errorf("sparkline = %q, want suffix %q", got, want)
			}
			bars := chart(tc.input, len(tc.input), 4)
			want = upStyle.Render("█")
			if tc.slow {
				want = slowStyle.Render("█")
			}
			if !strings.HasSuffix(bars[3], want) {
				t.Errorf("chart bottom = %q, want suffix %q", bars[3], want)
			}
		})
	}
	if got := sparkline(samples(-400), 1); got != downStyle.Render("✕") {
		t.Errorf("down = %q", got)
	}
	for _, line := range chart(samples(-400), 1, 4)[:4] {
		if !strings.HasSuffix(line, downStyle.Render("░")) {
			t.Fatal(line)
		}
	}
}

func TestLatencyStats(t *testing.T) {
	for _, tc := range []struct {
		input             []check.Result
		avg, p95, maximum time.Duration
		failed, total     int
	}{
		{nil, 0, 0, 0, 0, 0},
		{samples(-5, -9), 0, 0, 0, 2, 2},
		{samples(10, 20, 30, -400), 20 * time.Millisecond, 30 * time.Millisecond, 30 * time.Millisecond, 1, 4},
		{samples(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 100), 14500 * time.Microsecond, 19 * time.Millisecond, 100 * time.Millisecond, 0, 20},
	} {
		avg, p95, maximum, failed, total := latencyStats(tc.input)
		if avg != tc.avg || p95 != tc.p95 || maximum != tc.maximum || failed != tc.failed || total != tc.total {
			t.Errorf("stats = %v %v %v %d %d; want %+v", avg, p95, maximum, failed, total, tc)
		}
	}
}

func TestChart(t *testing.T) {
	for _, tc := range []struct {
		name        string
		input       []check.Result
		width, rows int
		want        []string
	}{
		{"empty", nil, 3, 4, []string{"         —│   ", "          │   ", "          │   ", "         0│   ", "          └───", "           older now"}},
		{"single padded", samples(80), 3, 4, []string{"      88ms│   ", "          │   ", "          │  █", "      72ms│  █", "          └───", "           older now"}},
		{"mixed", samples(-1, 80, 20), 3, 4, []string{"      88ms│ ▅░", "          │ █░", "          │ █░", "      18ms│▁█░", "          └───", "           older now"}},
		{"all down", samples(-1, -2), 2, 2, []string{"         —│░░", "         0│░░", "          └──", "           older now"}},
		{"window", samples(10, 1000), 1, 2, []string{"      11ms│ ", "       9ms│█", "          └─", "           older now"}},
		{"zero width", samples(1), 0, 4, nil},
		{"negative rows", samples(1), 4, -1, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := chart(tc.input, tc.width, tc.rows)
			if len(got) != len(tc.want) {
				t.Fatalf("lines = %q", got)
			}
			for i := range got {
				if ansi.Strip(got[i]) != tc.want[i] {
					t.Errorf("line %d = %q, want %q", i, ansi.Strip(got[i]), tc.want[i])
				}
			}
		})
	}
}
