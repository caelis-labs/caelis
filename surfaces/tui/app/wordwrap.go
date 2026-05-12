package tuiapp

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// graphemeWordWrap splits a plain-text line into multiple display lines,
// wrapping at word boundaries when possible. Falls back to character-level
// breaking only when a single token exceeds the available width.
//
// Word boundaries are: after whitespace, before/after CJK characters.
// This prevents ugly mid-word breaks like "F" / "ullAccess".
func graphemeWordWrap(s string, width int) []string {
	if width <= 0 || s == "" {
		return []string{s}
	}
	if graphemeWidth(s) <= width {
		return []string{s}
	}

	tokens := splitWrapTokens(s)
	if len(tokens) == 0 {
		return []string{s}
	}

	var lines []string
	var cur strings.Builder
	curWidth := 0

	for _, tok := range tokens {
		tw := graphemeWidth(tok)
		if tw == 0 {
			cur.WriteString(tok)
			continue
		}

		// Skip whitespace-only tokens at line start.
		if curWidth == 0 && strings.TrimSpace(tok) == "" {
			continue
		}

		// Would adding this token exceed the width?
		// Compare against the trimmed result: curWidth already includes any
		// trailing space from the previous token (serving as inter-word space),
		// and we only need the new token's content width (trailing space is
		// either trimmed on output or serves as separator for the next token).
		contentWidth := graphemeWidth(strings.TrimRight(tok, " "))
		if curWidth > 0 && curWidth+contentWidth > width {
			lines = append(lines, strings.TrimRight(cur.String(), " "))
			cur.Reset()
			curWidth = 0

			// Trim leading whitespace for new line.
			tok = strings.TrimLeft(tok, " \t")
			tw = graphemeWidth(tok)
			if tw == 0 {
				continue
			}
		}

		// Single token wider than width — hard-break it.
		if tw > width && curWidth == 0 {
			parts := graphemeHardWrap(tok, width)
			for i, part := range parts {
				if i < len(parts)-1 {
					lines = append(lines, part)
				} else {
					cur.WriteString(part)
					curWidth = graphemeWidth(part)
				}
			}
			continue
		}

		cur.WriteString(tok)
		curWidth += tw
	}

	if cur.Len() > 0 {
		line := strings.TrimRight(cur.String(), " ")
		if line != "" || len(lines) == 0 {
			lines = append(lines, line)
		}
	}

	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

// splitWrapTokens breaks text into tokens suitable for word-wrapping.
// Boundaries: spaces separate ASCII words; each CJK character is its own token.
//
//	"Hello world"    → ["Hello ", "world"]
//	"你好世界"        → ["你", "好", "世", "界"]
//	"Hello 你好 world" → ["Hello ", "你", "好", " ", "world"]
//	"FullAccess"     → ["FullAccess"]
func splitWrapTokens(s string) []string {
	if s == "" {
		return nil
	}

	var tokens []string
	var cur strings.Builder
	curHasContent := false

	state := -1
	remaining := s

	for len(remaining) > 0 {
		cluster, rest, _, newState := uniseg.FirstGraphemeClusterInString(remaining, state)
		state = newState
		remaining = rest

		rn, _ := utf8.DecodeRuneInString(cluster)
		cjk := isCJKRune(rn)
		space := unicode.IsSpace(rn)

		switch {
		case cjk:
			// Each CJK character is its own token. Flush pending.
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
				curHasContent = false
			}
			tokens = append(tokens, cluster)
		case space:
			if curHasContent {
				// Trailing space belongs to the current word token, then flush.
				cur.WriteString(cluster)
				tokens = append(tokens, cur.String())
				cur.Reset()
				curHasContent = false
			} else {
				// Leading space before any content — accumulate.
				cur.WriteString(cluster)
			}
		default:
			cur.WriteString(cluster)
			curHasContent = true
		}
	}

	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}

	return tokens
}

// isCJKRune reports whether r is a CJK ideograph, kana, hangul, or related
// full-width / CJK-punctuation character that should be treated as an
// individual breakable unit in word-wrapping.
func isCJKRune(r rune) bool {
	return unicode.In(r,
		unicode.Han,
		unicode.Hiragana,
		unicode.Katakana,
		unicode.Hangul,
	) ||
		(r >= 0xFF01 && r <= 0xFF60) || // Fullwidth Latin
		(r >= 0xFFE0 && r <= 0xFFE6) || // Fullwidth symbols
		(r >= 0x3000 && r <= 0x303F) // CJK Symbols and Punctuation
}
