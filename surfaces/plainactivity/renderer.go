package plainactivity

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type Kind int

const (
	Reasoning Kind = iota
	Assistant
	ToolCall
)

// Prefix returns the display prefix for each event kind.
func (k Kind) Prefix() string {
	return prefixForKind(k)
}

type Event struct {
	Kind Kind
	Text string
}

type Options struct {
	Width    int
	MaxLines int
}

func Render(events []Event, opts Options) []string {
	lines := make([]string, 0, len(events))
	for _, ev := range events {
		prefix := prefixForKind(ev.Kind)
		if prefix == "" {
			continue
		}
		bodyWidth := bodyWidth(opts.Width, prefix)
		emptyPrefix := strings.Repeat(" ", ansi.StringWidthWc(prefix))
		isFirstLine := true
		for _, line := range normalizedLines(ev.Text) {
			for _, segment := range wrappedLines(line, bodyWidth) {
				if isFirstLine {
					lines = append(lines, prefix+segment)
					isFirstLine = false
				} else {
					lines = append(lines, emptyPrefix+segment)
				}
			}
		}
	}
	return tail(lines, opts.MaxLines)
}

func prefixForKind(kind Kind) string {
	switch kind {
	case Reasoning:
		return "› "
	case Assistant:
		return "· "
	case ToolCall:
		return "• "
	default:
		return ""
	}
}

func normalizedLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	raw := strings.Split(text, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func wrappedLines(line string, width int) []string {
	if width <= 0 || ansi.StringWidthWc(line) <= width {
		return []string{line}
	}
	wrapped := ansi.Hardwrap(line, width, true)
	parts := strings.Split(wrapped, "\n")
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			lines = append(lines, part)
		}
	}
	if len(lines) == 0 {
		return []string{line}
	}
	return lines
}

func bodyWidth(width int, prefix string) int {
	if width <= 0 {
		return 0
	}
	body := width - ansi.StringWidthWc(prefix)
	if body < 1 {
		return 1
	}
	return body
}

func tail(lines []string, max int) []string {
	if max <= 0 || len(lines) <= max {
		return lines
	}
	return append([]string(nil), lines[len(lines)-max:]...)
}
