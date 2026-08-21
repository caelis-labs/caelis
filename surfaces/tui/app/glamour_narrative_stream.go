package tuiapp

import (
	"strings"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

// The active assistant renderer deliberately keeps the unstable suffix as raw
// Markdown source. Only a stable prefix is parsed by Glamour, which avoids a
// second, incomplete Markdown implementation while retaining the existing
// bounded repaint and viewport-row contracts.
func glamourStreamingNarrativeRowsObservedForKey(streamKey, blockID, raw, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme, observeGlamour func()) ([]RenderedRow, int, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, 0, false
	}
	raw = normalizeTextRenderRaw(raw)
	stableRaw, tailRaw := splitStableStreamingMarkdown(raw)
	prefixWidth := maxInt(graphemeWidth(rolePrefix), 0)
	bodyWidth := maxInt(1, width-prefixWidth)
	if strings.TrimSpace(stableRaw) == "" {
		return renderPlainStreamingNarrativeTailRows(blockID, raw, rolePrefix, roleStyle, bodyWidth, theme, true), 0, false
	}
	prefixRows, glamourCalls, cacheHit := cachedStreamingNarrativePrefixRowsForKey(streamKey, blockID, stableRaw, rolePrefix, roleStyle, width, theme, observeGlamour)
	if len(prefixRows) == 0 {
		return renderPlainStreamingNarrativeTailRows(blockID, raw, rolePrefix, roleStyle, bodyWidth, theme, true), glamourCalls, cacheHit
	}
	tailRows := renderPlainStreamingNarrativeTailRows(blockID, tailRaw, "", roleStyle, bodyWidth, theme, true)
	return joinStreamingNarrativeRows(blockID, stableRaw, prefixRows, tailRows, bodyWidth), glamourCalls, cacheHit
}

const (
	streamingStableTailMinRunes       = 96
	streamingNarrativeRendererVersion = "stream-raw-tail-v1"
	streamingNarrativeCacheMaxEntries = 128
)

func renderPlainStreamingNarrativeTailRows(blockID, raw, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme, active bool) []RenderedRow {
	raw = normalizeTextRenderRaw(raw)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	raw = strings.Trim(raw, "\n")
	if width <= 0 {
		width = 1
	}

	bodyStyle := theme.TextStyle()
	if roleStyle == tuikit.LineStyleReasoning {
		bodyStyle = theme.ReasoningStyle()
	}
	styledRolePrefix := ""
	if rolePrefix != "" {
		styledRolePrefix = tuikit.ColorizeLogLine(rolePrefix, roleStyle, theme)
	}

	rawLines := strings.Split(raw, "\n")
	rows := make([]RenderedRow, 0, len(rawLines)+4)
	firstOutputLine := true
	for _, rawLine := range rawLines {
		segments := graphemeHardWrap(rawLine, width)
		for _, segment := range segments {
			plain := segment
			styled := bodyStyle.Render(segment)
			if firstOutputLine && rolePrefix != "" {
				plain = rolePrefix + plain
				styled = styledRolePrefix + styled
			}
			firstOutputLine = false
			rows = append(rows, RenderedRow{
				Styled:     styled,
				Plain:      plain,
				BlockID:    blockID,
				PreWrapped: true,
				activeTail: active,
			})
		}
	}
	return rows
}

func joinStreamingNarrativeRows(blockID, stableRaw string, prefixRows, tailRows []RenderedRow, bodyWidth int) []RenderedRow {
	if len(prefixRows) == 0 {
		return tailRows
	}
	if len(tailRows) == 0 {
		return prefixRows
	}
	separatorRows := 0
	if hasStreamingParagraphBoundary(stableRaw) {
		separatorRows = 1
	}
	rows := make([]RenderedRow, 0, len(prefixRows)+separatorRows+len(tailRows))
	rows = append(rows, prefixRows...)
	if separatorRows > 0 {
		separator := strings.Repeat(" ", bodyWidth)
		rows = append(rows, RenderedRow{Styled: separator, Plain: "", BlockID: blockID, PreWrapped: true})
	}
	rows = append(rows, tailRows...)
	return rows
}

func cachedStreamingNarrativePrefixRowsForKey(streamKey, blockID, stableRaw, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme, observeGlamour func()) ([]RenderedRow, int, bool) {
	if streamKey == "" || blockID == "" || strings.TrimSpace(stableRaw) == "" {
		return nil, 0, false
	}
	themeKey := themeRenderCacheKey(theme)
	glamourStreamingCache.Lock()
	if entry, ok := glamourStreamingCache.entries[streamKey]; ok && streamingNarrativeCacheEntryMatches(entry, stableRaw, rolePrefix, roleStyle, width, themeKey) {
		touchStreamingNarrativeCacheKeyLocked(streamKey)
		rows := cloneRenderedRows(entry.renderedRows)
		glamourStreamingCache.Unlock()
		return rows, 0, true
	}
	glamourStreamingCache.Unlock()

	if observeGlamour != nil {
		observeGlamour()
	}
	rows := glamourNarrativeRows(blockID, stableRaw, rolePrefix, roleStyle, width, theme)
	if len(rows) == 0 {
		return nil, 1, false
	}
	storeStreamingNarrativeCacheEntry(streamKey, streamingNarrativeCacheEntry{
		width:           width,
		themeKey:        themeKey,
		role:            roleStyle,
		rendererVersion: streamingNarrativeRendererVersion,
		stableRaw:       stableRaw,
		rolePrefix:      rolePrefix,
		renderedRows:    cloneRenderedRows(rows),
	})
	return rows, 1, false
}

func streamingNarrativeCacheEntryMatches(entry streamingNarrativeCacheEntry, stableRaw, rolePrefix string, roleStyle tuikit.LineStyle, width int, themeKey string) bool {
	return entry.width == width &&
		entry.themeKey == themeKey &&
		entry.role == roleStyle &&
		entry.rendererVersion == streamingNarrativeRendererVersion &&
		entry.stableRaw == stableRaw &&
		entry.rolePrefix == rolePrefix
}

func storeStreamingNarrativeCacheEntry(streamKey string, entry streamingNarrativeCacheEntry) {
	if streamKey == "" {
		return
	}
	glamourStreamingCache.Lock()
	defer glamourStreamingCache.Unlock()
	if glamourStreamingCache.entries == nil {
		glamourStreamingCache.entries = make(map[string]streamingNarrativeCacheEntry, streamingNarrativeCacheMaxEntries)
	}
	if _, ok := glamourStreamingCache.entries[streamKey]; !ok && len(glamourStreamingCache.order) >= streamingNarrativeCacheMaxEntries {
		evict := glamourStreamingCache.order[0]
		glamourStreamingCache.order = glamourStreamingCache.order[1:]
		delete(glamourStreamingCache.entries, evict)
	}
	glamourStreamingCache.entries[streamKey] = entry
	touchStreamingNarrativeCacheKeyLocked(streamKey)
}

func touchStreamingNarrativeCacheKeyLocked(streamKey string) {
	for i, key := range glamourStreamingCache.order {
		if key != streamKey {
			continue
		}
		copy(glamourStreamingCache.order[i:], glamourStreamingCache.order[i+1:])
		glamourStreamingCache.order = glamourStreamingCache.order[:len(glamourStreamingCache.order)-1]
		break
	}
	glamourStreamingCache.order = append(glamourStreamingCache.order, streamKey)
}

func compatibleStreamingNarrativeFallbackRowsForKey(streamKey, blockID, raw, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme) []RenderedRow {
	if streamKey == "" || blockID == "" || strings.TrimSpace(raw) == "" {
		return nil
	}
	themeKey := themeRenderCacheKey(theme)
	glamourStreamingCache.Lock()
	entry, ok := glamourStreamingCache.entries[streamKey]
	if !ok || entry.width != width || entry.themeKey != themeKey || entry.role != roleStyle || entry.rendererVersion != streamingNarrativeRendererVersion || entry.rolePrefix != rolePrefix || entry.stableRaw == "" || !strings.HasPrefix(raw, entry.stableRaw) {
		glamourStreamingCache.Unlock()
		return nil
	}
	prefixRows := cloneRenderedRows(entry.renderedRows)
	glamourStreamingCache.Unlock()

	remaining := strings.TrimPrefix(raw, entry.stableRaw)
	prefixWidth := maxInt(graphemeWidth(rolePrefix), 0)
	bodyWidth := maxInt(1, width-prefixWidth)
	tailRows := renderPlainStreamingNarrativeTailRows(blockID, remaining, "", roleStyle, bodyWidth, theme, false)
	return joinStreamingNarrativeRows(blockID, entry.stableRaw, prefixRows, tailRows, bodyWidth)
}

func releaseStreamingNarrativeCacheEntry(streamKey string) {
	if streamKey == "" {
		return
	}
	glamourStreamingCache.Lock()
	delete(glamourStreamingCache.entries, streamKey)
	for i, key := range glamourStreamingCache.order {
		if key != streamKey {
			continue
		}
		copy(glamourStreamingCache.order[i:], glamourStreamingCache.order[i+1:])
		glamourStreamingCache.order = glamourStreamingCache.order[:len(glamourStreamingCache.order)-1]
		break
	}
	glamourStreamingCache.Unlock()
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

// splitStableStreamingMarkdown is only a promotion boundary detector. It does
// not parse or render Markdown; the suffix stays byte-for-byte source text
// (apart from normalized line endings) until it is promoted or sealed.
func splitStableStreamingMarkdown(raw string) (stableRaw string, tailRaw string) {
	return splitStableStreamingMarkdownContinuation(raw, false)
}

// splitStableStreamingMarkdownContinuation applies the same promotion rules
// after an already-rendered stable boundary. The initial stream keeps a
// two-tail window before invoking Glamour at all. Once a prefix exists, the
// retained tail already starts at a safe block boundary, so waiting for that
// whole initial window again would make fine-grained streams lag behind an
// equivalent one-shot projection.
func splitStableStreamingMarkdownContinuation(raw string, hasStablePrefix bool) (stableRaw string, tailRaw string) {
	raw = normalizeTextRenderRaw(raw)
	if strings.TrimSpace(raw) == "" {
		return "", ""
	}
	minimumRunes := streamingStableTailMinRunes * 2
	if hasStablePrefix {
		minimumRunes = streamingStableTailMinRunes + 1
	}
	if utf8.RuneCountInString(raw) < minimumRunes {
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
	for idx, segment := range lines {
		line := strings.TrimSuffix(segment, "\n")
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
		offset += len(segment)
		if inFence || idx >= len(lines)-1 || strings.TrimSpace(line) != "" {
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
