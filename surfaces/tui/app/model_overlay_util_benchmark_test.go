package tuiapp

import (
	"strconv"
	"strings"
	"testing"
)

var normalizedFullscreenFrameBenchmarkSink string

func BenchmarkNormalizeFullscreenFrame(b *testing.B) {
	const (
		width  = 120
		height = 32
	)
	for _, fixture := range []struct {
		name      string
		wide      bool
		scrollbar bool
	}{
		{name: "ASCII"},
		{name: "Wide", wide: true},
		{name: "WideScrollbar", wide: true, scrollbar: true},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			frame := benchmarkFullscreenFrame(width, height, fixture.wide, fixture.scrollbar)
			b.ReportAllocs()
			b.SetBytes(int64(len(frame)))
			for b.Loop() {
				normalizedFullscreenFrameBenchmarkSink = normalizeFullscreenFrame(frame, width, height)
			}
		})
	}
}

func benchmarkFullscreenFrame(width int, height int, wide bool, scrollbar bool) string {
	lines := make([]string, height)
	for row := range lines {
		line := "historical exploration row " + strconv.Itoa(row)
		if wide {
			line = "复现一下当时 Grep 返回 0 的调用方式 row " + strconv.Itoa(row)
		}
		if scrollbar {
			line = padRightDisplay(line, width-1) + "▏"
		}
		lines[row] = line
	}
	return strings.Join(lines, "\n")
}
