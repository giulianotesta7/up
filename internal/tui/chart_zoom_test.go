package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func plainChart(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}
	return out
}

func TestZoomedChartFlatSeriesIsNotASolidBlock(t *testing.T) {
	lines := plainChart(chart(samples(400, 400, 400, 400, 400, 400), 6, 5))
	if strings.Contains(lines[0], "█") {
		t.Fatalf("steady series fills the top row:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(strings.Join(lines[:5], "\n"), "█") {
		t.Fatalf("steady series draws no bar:\n%s", strings.Join(lines, "\n"))
	}
}

func TestZoomedChartClipsSpikeAndKeepsVariation(t *testing.T) {
	input := samples(1150, 400, 396, 404, 400, 392, 408, 400, 398, 402, 400, 395, 405, 400, 399, 401, 400, 397, 403, 400)
	lines := plainChart(chart(input, 20, 5))
	// Newest is rendered on the right, so the spike (first sample) is the last cell.
	if !strings.HasSuffix(lines[0], "▲") {
		t.Errorf("clipped spike is not marked: %q", lines[0])
	}
	for _, line := range lines[1:5] {
		if !strings.HasSuffix(line, "█") {
			t.Errorf("clipped spike does not fill its column: %q", line)
		}
	}
	if !strings.Contains(lines[0], "1.15s ▲") {
		t.Errorf("top label should name the clipped maximum: %q", lines[0])
	}
	// The 392..408ms bars must differ: sum each column's glyph levels.
	heights := map[int]bool{}
	for col := 0; col < 19; col++ {
		height := 0
		for _, line := range lines[:5] {
			cells := []rune(line[strings.Index(line, "│")+len("│"):])
			if i := strings.IndexRune("▁▂▃▄▅▆▇█", cells[19-1-col]); i >= 0 {
				height += i/3 + 1 // byte offset of a 3-byte glyph
			}
		}
		heights[height] = true
	}
	if len(heights) < 2 {
		t.Errorf("spike flattened the rest of the bars:\n%s", strings.Join(lines, "\n"))
	}
}

func TestZoomedChartLabelsOnlyScaleLimits(t *testing.T) {
	input := samples(400, 400, 400, 400, 400, 420, 380, 400)
	lines := plainChart(chart(input, 8, 5))
	for _, row := range lines[1:4] {
		if strings.TrimSpace(row[:strings.Index(row, "│")]) != "" {
			t.Errorf("only the top and bottom rows carry a label: %q", row)
		}
	}
	if strings.Contains(lines[4], "         0│") {
		t.Errorf("bottom label should be the scale floor, not 0: %q", lines[4])
	}
	if strings.Contains(strings.Join(lines[:5], "\n"), "▲") {
		t.Errorf("nothing is clipped:\n%s", strings.Join(lines, "\n"))
	}
}

func TestChartScaleNote(t *testing.T) {
	if _, _, ok := chartScale(samples(-1, -2)); ok {
		t.Fatal("a window without successes has no scale")
	}
	lo, hi, ok := chartScale(samples(400, 400, 400, 400, 400))
	if !ok || lo >= 400*1e6 || hi <= 400*1e6 {
		t.Fatalf("scale %v..%v must bracket the median", lo, hi)
	}
}

func TestDetailShowsSixtyChecksAndStatsTable(t *testing.T) {
	if recentLimit != 60 {
		t.Fatalf("recentLimit = %d, want 60", recentLimit)
	}
	m, _ := press(t, modelWith(seededSnapshot(t, now)), keyEnter)
	out := view(m)
	if !strings.Contains(out, "AVG      P95      MAX      FAILED") {
		t.Errorf("detail lacks the stats header row:\n%s", out)
	}
	if strings.Contains(out, "scale ") || strings.Contains(out, "avg ") {
		t.Errorf("detail keeps the old scale note or stats line:\n%s", out)
	}
}
