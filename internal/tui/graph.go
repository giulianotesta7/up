package tui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/giulianotesta7/up/internal/check"
)

const graphLabelWidth = 10

var blockLevels = []rune("▁▂▃▄▅▆▇█")

// graphWindow keeps the newest width results; callers render them in reverse.
func graphWindow(results []check.Result, width int) []check.Result {
	return results[:min(len(results), max(0, width))]
}

func successfulLatencies(results []check.Result) []time.Duration {
	values := make([]time.Duration, 0, len(results))
	for _, r := range results {
		if r.Status == check.StatusUp {
			values = append(values, r.Latency)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values
}

// latencyStats excludes failures from the latency distribution. p95 uses the
// nearest rank (ceil(.95*n)), not an interpolated percentile.
func latencyStats(results []check.Result) (avg, p95, maximum time.Duration, failed, total int) {
	values := successfulLatencies(results)
	total = len(results)
	failed = total - len(values)
	if len(values) == 0 {
		return
	}
	// Divide before summing to avoid overflowing on long durations.
	var remainder time.Duration
	for _, v := range values {
		avg += v / time.Duration(len(values))
		remainder += v % time.Duration(len(values))
	}
	avg += remainder / time.Duration(len(values))
	p95 = values[(95*len(values)+99)/100-1]
	maximum = values[len(values)-1]
	return
}

// slowThreshold is relative to this monitor's successful window, not to a
// global latency target. Even-sized windows use the mean of the middle pair.
func slowThreshold(results []check.Result) (float64, bool) {
	values := successfulLatencies(results)
	if len(values) < 5 {
		return 0, false
	}
	median := float64(values[len(values)/2])
	if len(values)%2 == 0 {
		median = median/2 + float64(values[len(values)/2-1])/2
	}
	return 2 * median, true
}

func graphStyle(r check.Result, threshold float64, detectSlow bool) lipgloss.Style {
	if r.Status != check.StatusUp {
		return downStyle
	}
	if detectSlow && float64(r.Latency) > threshold {
		return slowStyle
	}
	return upStyle
}

// graphHeight rounds upward so even a zero-latency success occupies a cell.
func graphHeight(latency time.Duration, scale float64, levels int) int {
	if scale <= 0 {
		return 1
	}
	return min(levels, max(1, int(math.Ceil(float64(latency)/scale*float64(levels)))))
}

func sparkline(results []check.Result, width int) string {
	width = max(0, width)
	results = graphWindow(results, width)
	_, p95, _, _, _ := latencyStats(results)
	scale := max(float64(150*time.Millisecond), 1.3*float64(p95))
	threshold, detectSlow := slowThreshold(results)
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", width-len(results)))
	for i := len(results) - 1; i >= 0; i-- {
		r := results[i]
		glyph := "✕"
		if r.Status == check.StatusUp {
			glyph = string(blockLevels[graphHeight(r.Latency, scale, 8)-1])
		}
		b.WriteString(graphStyle(r, threshold, detectSlow).Render(glyph))
	}
	return b.String()
}

// chartScale zooms the chart to the typical range of successful checks, from
// just under the fastest up to just over p95. Slower checks are clipped, so one
// spike cannot flatten the rest. ok is false when nothing succeeded.
func chartScale(results []check.Result) (lo, hi time.Duration, ok bool) {
	values := successfulLatencies(results)
	if len(values) == 0 {
		return 0, 0, false
	}
	median := values[len(values)/2]
	_, p95, _, _, _ := latencyStats(results)
	lo = values[0] * 9 / 10
	hi = max(p95*11/10, lo+median/5)
	return lo, hi, true
}

// chart returns the bars, fixed-width axis, baseline and time direction.
// Blank padding denotes missing history; failures fill their entire column.
// Successes are scaled by chartScale: checks above it fill their column and
// are marked with ▲ on the top row.
func chart(results []check.Result, width, rows int) []string {
	if width <= 0 || rows <= 0 {
		return nil
	}
	results = graphWindow(results, width)
	_, _, maximum, _, _ := latencyStats(results)
	lo, hi, scaled := chartScale(results)
	threshold, detectSlow := slowThreshold(results)
	levels := rows * 8
	level := func(latency time.Duration) int {
		if hi <= lo {
			return 1
		}
		return min(levels, max(1, int(math.Ceil(float64(latency-lo)/float64(hi-lo)*float64(levels)))))
	}
	clipped := scaled && maximum > hi
	lines := make([]string, 0, rows+2)
	for row := 0; row < rows; row++ {
		label := ""
		switch {
		case row == 0 && !scaled:
			label = formatLatency(0)
		case row == 0 && clipped:
			label = formatLatency(maximum) + " ▲"
		case row == 0:
			label = formatLatency(hi)
		case row == rows-1 && scaled:
			label = formatLatency(lo)
		case row == rows-1:
			label = "0"
		}
		var b strings.Builder
		b.WriteString(helpStyle.Render(fmt.Sprintf("%*s", graphLabelWidth, label)) + "│")
		b.WriteString(strings.Repeat(" ", width-len(results)))
		for i := len(results) - 1; i >= 0; i-- {
			r := results[i]
			glyph := "░"
			if r.Status == check.StatusUp {
				fill := min(8, max(0, level(r.Latency)-(rows-row-1)*8))
				glyph = " "
				if fill > 0 {
					glyph = string(blockLevels[fill-1])
				}
				if row == 0 && r.Latency > hi {
					glyph = "▲"
				}
			}
			b.WriteString(graphStyle(r, threshold, detectSlow).Render(glyph))
		}
		lines = append(lines, b.String())
	}
	lines = append(lines, strings.Repeat(" ", graphLabelWidth)+"└"+strings.Repeat("─", width))
	lines = append(lines, strings.Repeat(" ", graphLabelWidth+1)+"older"+strings.Repeat(" ", max(1, width-8))+"now")
	return lines
}
