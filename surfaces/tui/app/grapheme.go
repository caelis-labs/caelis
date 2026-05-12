package tuiapp

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// graphemeWidth returns the display width of s in terminal cells,
// iterating by grapheme cluster. ANSI escape codes are stripped first.
func graphemeWidth(s string) int {
	return ansi.StringWidth(s)
}

// graphemeSlice extracts the substring from display column [start, end)
// by iterating over grapheme clusters. The input must be plain text
// (no ANSI escapes).
func graphemeSlice(s string, start, end int) string {
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if s == "" || start == end {
		return ""
	}

	var b strings.Builder
	col := 0
	state := -1
	remaining := s
	for len(remaining) > 0 {
		cluster, rest, w, newState := uniseg.FirstGraphemeClusterInString(remaining, state)
		state = newState
		remaining = rest

		if w <= 0 {
			w = 0
		}
		if w == 0 {
			// Zero-width cluster (e.g. standalone combining mark); skip.
			continue
		}
		if col >= end {
			break
		}
		if col >= start && col < end {
			b.WriteString(cluster)
		}
		col += w
	}
	return b.String()
}

// graphemeHardWrap splits a plain-text line into multiple lines, each at
// most width display columns wide. It breaks at grapheme cluster boundaries.
func graphemeHardWrap(s string, width int) []string {
	if width <= 0 || s == "" {
		return []string{s}
	}
	if graphemeWidth(s) <= width {
		return []string{s}
	}

	var lines []string
	var cur strings.Builder
	curWidth := 0
	state := -1
	remaining := s
	for len(remaining) > 0 {
		cluster, rest, w, newState := uniseg.FirstGraphemeClusterInString(remaining, state)
		state = newState
		remaining = rest

		if w <= 0 {
			w = 0
		}
		if w == 0 {
			cur.WriteString(cluster)
			continue
		}
		if curWidth+w > width && curWidth > 0 {
			lines = append(lines, cur.String())
			cur.Reset()
			curWidth = 0
		}
		cur.WriteString(cluster)
		curWidth += w
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func splitGraphemeClusters(s string) []string {
	if s == "" {
		return nil
	}
	clusters := make([]string, 0, len(s))
	state := -1
	remaining := s
	for len(remaining) > 0 {
		cluster, rest, _, newState := uniseg.FirstGraphemeClusterInString(remaining, state)
		state = newState
		remaining = rest
		clusters = append(clusters, cluster)
	}
	return clusters
}

func joinGraphemeClusters(clusters []string) string {
	if len(clusters) == 0 {
		return ""
	}
	return strings.Join(clusters, "")
}

func isNaturalRevealBoundary(prev string, next string) bool {
	prev = ansi.Strip(prev)
	next = ansi.Strip(next)
	if prev == "" {
		return false
	}
	if strings.Contains(prev, "\n") {
		return true
	}
	prevRune, _ := lastRune(prev)
	nextRune, _ := firstRune(next)
	switch {
	case unicode.IsSpace(prevRune):
		return true
	case revealBoundaryPunctuation(prevRune):
		return true
	case next != "" && (unicode.IsSpace(nextRune) || revealBoundaryPunctuation(nextRune) || nextRune == '\n'):
		return true
	default:
		return false
	}
}

func revealBoundaryPunctuation(r rune) bool {
	switch r {
	case '.', ',', '!', '?', ';', ':', ')', ']', '}', '>', '-', '*', '`', '~',
		'。', '，', '！', '？', '；', '：', '）', '】', '》', '、', '”', '’':
		return true
	default:
		return false
	}
}

func firstRune(s string) (rune, bool) {
	for _, r := range s {
		return r, true
	}
	return 0, false
}

func lastRune(s string) (rune, bool) {
	var out rune
	var ok bool
	for _, r := range s {
		out = r
		ok = true
	}
	return out, ok
}
