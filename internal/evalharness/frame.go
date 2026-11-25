package evalharness

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// NormalizeFrame removes terminal styling and trailing padding for text comparisons.
func NormalizeFrame(frame string) string {
	frame = ansi.Strip(frame)
	frame = strings.ReplaceAll(frame, "\r\n", "\n")
	frame = strings.ReplaceAll(frame, "\r", "\n")
	lines := strings.Split(frame, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
