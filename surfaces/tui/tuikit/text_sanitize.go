package tuikit

import (
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
)

var (
	httpURL = regexp.MustCompile(`https?://[^\s\x00-\x1f\x7f\x1b]+`)
)

func LinkifyText(text string, style lipgloss.Style) string {
	text = stripBrokenOSC8(text)
	var out strings.Builder
	for text != "" {
		start := strings.Index(text, "\x1b]8;;")
		if start < 0 {
			out.WriteString(linkifyPlainHTTP(text, style))
			break
		}
		out.WriteString(linkifyPlainHTTP(text[:start], style))
		openEnd, openTermLen := oscSequenceEnd(text, start+len("\x1b]8;;"))
		if openEnd < 0 {
			out.WriteString(text[start:])
			break
		}
		closeRelative := strings.Index(text[openEnd+openTermLen:], "\x1b]8;;")
		if closeRelative < 0 {
			out.WriteString(text[start:])
			break
		}
		closeStart := openEnd + openTermLen + closeRelative
		closeEnd, closeTermLen := oscSequenceEnd(text, closeStart+len("\x1b]8;;"))
		if closeEnd < 0 {
			out.WriteString(text[start:])
			break
		}
		out.WriteString(text[start : closeEnd+closeTermLen])
		text = text[closeEnd+closeTermLen:]
	}
	return out.String()
}

func linkifyPlainHTTP(text string, style lipgloss.Style) string {
	return httpURL.ReplaceAllStringFunc(text, func(candidate string) string {
		url, suffix := trimURLSuffix(candidate)
		if url == "" {
			return candidate
		}
		return style.Hyperlink(url).Render(url) + suffix
	})
}

func oscSequenceEnd(text string, after int) (int, int) {
	if after < 0 || after > len(text) {
		return -1, 0
	}
	bel := strings.IndexByte(text[after:], '\a')
	st := strings.Index(text[after:], "\x1b\\")
	switch {
	case bel < 0 && st < 0:
		return -1, 0
	case bel >= 0 && (st < 0 || bel < st):
		return after + bel, 1
	default:
		return after + st, 2
	}
}

func trimURLSuffix(candidate string) (string, string) {
	url := candidate
	for url != "" {
		last := url[len(url)-1]
		switch last {
		case '.', ',', ';', ':', '!', '?', '\'', '"':
			url = url[:len(url)-1]
		case ')':
			if strings.Count(url, ")") > strings.Count(url, "(") {
				url = url[:len(url)-1]
				continue
			}
			return url, candidate[len(url):]
		case ']':
			if strings.Count(url, "]") > strings.Count(url, "[") {
				url = url[:len(url)-1]
				continue
			}
			return url, candidate[len(url):]
		default:
			return url, candidate[len(url):]
		}
	}
	return "", candidate
}

func stripBrokenOSC8(text string) string {
	if text == "" {
		return ""
	}
	const marker = "]8;;"
	var out strings.Builder
	for text != "" {
		start := strings.Index(text, marker)
		if start < 0 {
			out.WriteString(text)
			break
		}
		if start > 0 && text[start-1] == '\x1b' {
			out.WriteString(text[:start+len(marker)])
			text = text[start+len(marker):]
			continue
		}
		out.WriteString(text[:start])
		end, termLen := oscSequenceEnd(text, start+len(marker))
		if end < 0 {
			out.WriteString(text[start:])
			break
		}
		text = text[end+termLen:]
	}
	return out.String()
}
