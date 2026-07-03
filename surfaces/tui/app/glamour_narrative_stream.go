package tuiapp

import (
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

// ---------------------------------------------------------------------------
// Streaming-safe glamour rendering
// ---------------------------------------------------------------------------

func glamourStreamingNarrativeRowsObserved(blockID, raw, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme, observeGlamour func()) ([]RenderedRow, int, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, 0, false
	}
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	stableRaw, tailRaw := splitStableStreamingMarkdown(raw)
	prefixWidth := maxInt(graphemeWidth(rolePrefix), 0)
	glamourWidth := maxInt(1, width-prefixWidth)
	if strings.TrimSpace(stableRaw) == "" {
		return renderStreamingNarrativeTailRows(blockID, raw, rolePrefix, roleStyle, glamourWidth, theme), 0, false
	}
	prefixRows, glamourCalls, cacheHit := cachedStreamingNarrativePrefixRows(blockID, stableRaw, rolePrefix, roleStyle, width, theme, observeGlamour)
	if len(prefixRows) == 0 {
		return renderStreamingNarrativeTailRows(blockID, raw, rolePrefix, roleStyle, glamourWidth, theme), glamourCalls, cacheHit
	}
	tailRows := renderStreamingNarrativeTailRows(blockID, tailRaw, "", roleStyle, glamourWidth, theme)
	if len(tailRows) == 0 {
		return prefixRows, glamourCalls, cacheHit
	}
	separatorRows := 0
	if hasStreamingParagraphBoundary(stableRaw) {
		separatorRows = 1
	}
	rows := make([]RenderedRow, 0, len(prefixRows)+separatorRows+len(tailRows))
	rows = append(rows, prefixRows...)
	if separatorRows > 0 {
		separator := strings.Repeat(" ", glamourWidth)
		rows = append(rows, RenderedRow{Styled: separator, Plain: separator, BlockID: blockID, PreWrapped: true})
	}
	rows = append(rows, tailRows...)
	return rows, glamourCalls, cacheHit
}

const streamingStableTailMinRunes = 96
const streamingNarrativeRendererVersion = "stream-md-v4"

func renderStreamingNarrativeTailRows(blockID, raw, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme) []RenderedRow {
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	streamLines := buildStreamingNarrativeTailLines(raw)
	if len(streamLines) == 0 {
		if strings.TrimSpace(raw) != "" {
			return []RenderedRow{{BlockID: blockID, PreWrapped: true}}
		}
		return nil
	}
	if width <= 0 {
		width = 1
	}
	baseStyle := narrativeBodyStyle(roleStyle, theme)
	styledRolePrefix := ""
	if rolePrefix != "" {
		styledRolePrefix = tuikit.ColorizeLogLine(rolePrefix, roleStyle, theme)
	}
	rows := make([]RenderedRow, 0, len(streamLines)+4)
	for idx, line := range streamLines {
		lineStyle := streamingNarrativeTailLineStyle(line.Kind, baseStyle, roleStyle, theme)
		styledSegments, plainSegments := renderStreamingNarrativeTailSegments(line.Raw, lineStyle, line.Kind, roleStyle, theme, width)
		for segIdx, styled := range styledSegments {
			plain := plainSegments[segIdx]
			if idx == 0 && rolePrefix != "" && segIdx == 0 {
				plain = rolePrefix + plain
				styled = styledRolePrefix + styled
			}
			rows = append(rows, RenderedRow{
				Styled:     styled,
				Plain:      plain,
				BlockID:    blockID,
				PreWrapped: true,
			})
		}
	}
	return rows
}

type streamingNarrativeTailLine struct {
	Kind  NarrativeBlockKind
	Raw   string
	Plain string
}

func buildStreamingNarrativeTailLines(raw string) []streamingNarrativeTailLine {
	nls, _ := buildNarrativeRows(raw)
	if len(nls) == 0 {
		return nil
	}
	lines := make([]streamingNarrativeTailLine, 0, len(nls))
	for _, nl := range nls {
		line, ok := streamingNarrativeTailLineTexts(nl)
		if !ok {
			continue
		}
		lines = append(lines, line)
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0].Plain) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1].Plain) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func streamingNarrativeTailLineTexts(line NarrativeLine) (streamingNarrativeTailLine, bool) {
	switch line.Kind {
	case NarrativeCodeFenceDelim:
		return streamingNarrativeTailLine{}, false
	case NarrativeCodeFence:
		raw := strings.TrimRight(line.Text, " \t")
		return streamingNarrativeTailLine{Kind: line.Kind, Raw: raw, Plain: raw}, true
	case NarrativeHeading:
		raw := stripHeadingMarker(line.Text)
		return streamingNarrativeTailLine{Kind: line.Kind, Raw: raw, Plain: simplifyInlineMarkers(raw)}, true
	case NarrativeTableRow:
		plain := formatTablePlainRow(line.Text)
		return streamingNarrativeTailLine{Kind: line.Kind, Raw: plain, Plain: plain}, true
	case NarrativeTableRule:
		plain := formatTableRuleRow(line.Text)
		return streamingNarrativeTailLine{Kind: line.Kind, Raw: plain, Plain: plain}, true
	case NarrativeBlockquote:
		raw := stripBlockquoteMarker(line.Text)
		return streamingNarrativeTailLine{Kind: line.Kind, Raw: raw, Plain: simplifyInlineMarkers(raw)}, true
	case NarrativeListItem:
		raw := strings.TrimRight(line.Text, " \t")
		return streamingNarrativeTailLine{Kind: line.Kind, Raw: raw, Plain: simplifyInlineMarkers(raw)}, true
	default:
		raw := strings.TrimRight(line.Text, " \t")
		return streamingNarrativeTailLine{Kind: line.Kind, Raw: raw, Plain: simplifyInlineMarkers(raw)}, true
	}
}

func wrapStyledStreamingTailLine(styled string, width int) ([]string, []string) {
	if styled == "" {
		return []string{""}, []string{""}
	}
	if width <= 0 {
		width = 1
	}
	plain := strings.TrimRight(xansi.Strip(styled), " ")
	if strings.TrimRight(plain, " ") == "" || graphemeWidth(plain) <= width {
		return []string{styled}, []string{plain}
	}
	wrapped := xansi.Wrap(styled, width, " ")
	styledSegments := strings.Split(wrapped, "\n")
	if len(styledSegments) == 0 {
		return []string{styled}, []string{plain}
	}
	return styledSegments, deriveViewportPlainLines(nil, styledSegments)
}

func streamingNarrativeTailLineStyle(kind NarrativeBlockKind, base lipgloss.Style, roleStyle tuikit.LineStyle, theme tuikit.Theme) lipgloss.Style {
	switch kind {
	case NarrativeCodeFence:
		style := theme.MarkdownCodeBlockStyle()
		if roleStyle == tuikit.LineStyleReasoning {
			style = style.Foreground(theme.ReasoningFg)
		}
		return style
	case NarrativeHeading:
		if roleStyle == tuikit.LineStyleReasoning {
			return theme.ReasoningStyle().Bold(true)
		}
		return theme.MarkdownHeadingStyle().Bold(true)
	default:
		return base
	}
}

func renderStreamingNarrativeTailSegment(segment string, style lipgloss.Style, kind NarrativeBlockKind, theme tuikit.Theme) string {
	if segment == "" {
		return ""
	}
	if kind == NarrativeCodeFence || kind == NarrativeTableRule {
		return style.Render(segment)
	}
	return renderInlineMarkdown(segment, style, theme)
}

func renderStreamingNarrativeTailSegments(segment string, style lipgloss.Style, kind NarrativeBlockKind, roleStyle tuikit.LineStyle, theme tuikit.Theme, width int) ([]string, []string) {
	if segment == "" {
		return []string{""}, []string{""}
	}
	if kind == NarrativeBlockquote {
		return renderStreamingBlockquoteTailSegments(segment, roleStyle, theme, width)
	}
	if kind == NarrativeCodeFence || kind == NarrativeTableRule {
		return wrapStyledStreamingTailLine(style.Render(segment), width)
	}
	return renderInlineMarkdownWrappedSegments(segment, style, theme, width)
}

func renderStreamingBlockquoteTailSegments(segment string, roleStyle tuikit.LineStyle, theme tuikit.Theme, width int) ([]string, []string) {
	if width <= 0 {
		width = 1
	}
	bodyStyle := theme.TextStyle()
	if roleStyle == tuikit.LineStyleReasoning {
		bodyStyle = theme.ReasoningStyle()
	}
	bodyWidth := maxInt(1, width-displayColumns(narrativeBlockquoteRail))
	var styledBody, plainBody []string
	if roleStyle == tuikit.LineStyleReasoning {
		styledBody, plainBody = wrapStyledStreamingTailLine(bodyStyle.Render(simplifyInlineMarkers(segment)), bodyWidth)
	} else {
		styledBody, plainBody = renderInlineMarkdownWrappedSegments(segment, bodyStyle, theme, bodyWidth)
	}
	styledRail := bodyStyle.Render(narrativeBlockquoteRail)
	styled := make([]string, 0, len(styledBody))
	plain := make([]string, 0, len(plainBody))
	for i := range styledBody {
		styled = append(styled, styledRail+styledBody[i])
		plain = append(plain, narrativeBlockquoteRail+plainBody[i])
	}
	return styled, plain
}

func cachedStreamingNarrativePrefixRows(blockID, stableRaw, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme, observeGlamour func()) ([]RenderedRow, int, bool) {
	if blockID == "" || strings.TrimSpace(stableRaw) == "" {
		return nil, 0, false
	}
	glamourStreamingCache.Lock()
	defer glamourStreamingCache.Unlock()
	if glamourStreamingCache.entries == nil {
		glamourStreamingCache.entries = map[string]streamingNarrativeCacheEntry{}
	}
	if entry, ok := glamourStreamingCache.entries[blockID]; ok {
		themeKey := themeRenderCacheKey(theme)
		if entry.width == width && entry.themeKey == themeKey && entry.role == roleStyle && entry.rendererVersion == streamingNarrativeRendererVersion && entry.stableRaw == stableRaw && entry.rolePrefix == rolePrefix {
			return cloneRenderedRows(entry.renderedRows), 0, true
		}
	}
	if observeGlamour != nil {
		observeGlamour()
	}
	rows := glamourNarrativeRows(blockID, stableRaw, rolePrefix, roleStyle, width, theme)
	if len(rows) == 0 {
		return nil, 1, false
	}
	glamourStreamingCache.entries[blockID] = streamingNarrativeCacheEntry{
		width:           width,
		themeKey:        themeRenderCacheKey(theme),
		role:            roleStyle,
		rendererVersion: streamingNarrativeRendererVersion,
		stableRaw:       stableRaw,
		rolePrefix:      rolePrefix,
		renderedRows:    cloneRenderedRows(rows),
	}
	return rows, 1, false
}

func cloneRenderedRows(rows []RenderedRow) []RenderedRow {
	if len(rows) == 0 {
		return nil
	}
	out := make([]RenderedRow, len(rows))
	copy(out, rows)
	return out
}

func hasStreamingParagraphBoundary(raw string) bool {
	raw = strings.TrimRight(raw, " \t\r")
	newlines := 0
	for i := len(raw) - 1; i >= 0; i-- {
		if raw[i] != '\n' {
			break
		}
		newlines++
	}
	return newlines >= 2
}

func splitStableStreamingMarkdown(raw string) (stableRaw string, tailRaw string) {
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	if strings.TrimSpace(raw) == "" {
		return "", ""
	}
	if utf8.RuneCountInString(raw) < streamingStableTailMinRunes*2 {
		return "", raw
	}
	lines := strings.SplitAfter(raw, "\n")
	if len(lines) < 3 {
		return "", raw
	}
	lastBoundary := 0
	offset := 0
	inFence := false
	fenceChar := byte(0)
	fenceLen := 0
	for idx, seg := range lines {
		line := strings.TrimSuffix(seg, "\n")
		trimmed := strings.TrimSpace(line)
		if len(trimmed) >= 3 {
			ch := trimmed[0]
			if ch == '`' || ch == '~' {
				count := 0
				for count < len(trimmed) && trimmed[count] == ch {
					count++
				}
				if count >= 3 {
					if !inFence {
						inFence = true
						fenceChar = ch
						fenceLen = count
					} else if ch == fenceChar && count >= fenceLen && strings.TrimSpace(trimmed[count:]) == "" {
						inFence = false
					}
				}
			}
		}
		offset += len(seg)
		if inFence || idx >= len(lines)-1 {
			continue
		}
		if strings.TrimSpace(line) != "" {
			continue
		}
		tailCandidate := strings.TrimSpace(raw[offset:])
		if utf8.RuneCountInString(tailCandidate) >= streamingStableTailMinRunes {
			lastBoundary = offset
		}
	}
	if lastBoundary <= 0 || lastBoundary >= len(raw) {
		return "", raw
	}
	stableRaw = raw[:lastBoundary]
	tailRaw = raw[lastBoundary:]
	if strings.TrimSpace(stableRaw) == "" || strings.TrimSpace(tailRaw) == "" {
		return "", raw
	}
	return stableRaw, tailRaw
}
