package tuiapp

import "strings"

func streamSmoothingKey(targetKind string, sessionKey string, streamKind string, actor string) string {
	return strings.TrimSpace(targetKind) + "|" + strings.TrimSpace(sessionKey) + "|" + strings.TrimSpace(streamKind) + "|" + strings.TrimSpace(actor)
}

func chooseRevealClusterCount(clusters []string, desired int, maxPerFrame int) int {
	if len(clusters) == 0 || desired <= 0 {
		return 0
	}
	if maxPerFrame <= 0 || maxPerFrame > len(clusters) {
		maxPerFrame = len(clusters)
	}
	revealLimit := firstLogicalLineClusterLimit(clusters, maxPerFrame)
	if revealLimit <= 0 {
		revealLimit = maxPerFrame
	}
	if desired > len(clusters) {
		desired = len(clusters)
	}
	if desired > revealLimit {
		desired = revealLimit
	}
	minStable := minStableRevealCount(clusters, revealLimit)
	best := 0
	for idx := 1; idx <= desired; idx++ {
		if idx < minStable && idx < len(clusters) {
			continue
		}
		next := ""
		if idx < len(clusters) {
			next = clusters[idx]
		}
		if isNaturalRevealBoundary(clusters[idx-1], next) {
			best = idx
		}
	}
	if best > 0 {
		return best
	}
	lookaheadLimit := min(revealLimit, len(clusters))
	for idx := desired + 1; idx <= lookaheadLimit && idx <= desired+4; idx++ {
		if idx < minStable && idx < len(clusters) {
			continue
		}
		next := ""
		if idx < len(clusters) {
			next = clusters[idx]
		}
		if isNaturalRevealBoundary(clusters[idx-1], next) {
			return idx
		}
	}
	if desired < minStable && minStable <= lookaheadLimit {
		return minStable
	}
	return desired
}

func extendRevealToStableRenderedRows(existing string, pending []string, desired int, maxPerFrame int, wrapWidth int, streamKind string, actor string, upstreamDone bool) int {
	if len(pending) == 0 || desired <= 0 {
		return 0
	}
	if maxPerFrame <= 0 || maxPerFrame > len(pending) {
		maxPerFrame = len(pending)
	}
	if desired > maxPerFrame {
		desired = maxPerFrame
	}
	if wrapWidth <= 0 {
		return desired
	}

	beforeRows := renderStreamNarrativePlainRows(existing, streamKind, actor, wrapWidth)
	if renderedRevealRowsStable(beforeRows, renderStreamNarrativePlainRows(existing+joinGraphemeClusters(pending[:desired]), streamKind, actor, wrapWidth), desired, len(pending), streamKind, actor, upstreamDone) {
		return desired
	}
	for idx := desired + 1; idx <= maxPerFrame; idx++ {
		afterRows := renderStreamNarrativePlainRows(existing+joinGraphemeClusters(pending[:idx]), streamKind, actor, wrapWidth)
		if renderedRevealRowsStable(beforeRows, afterRows, idx, len(pending), streamKind, actor, upstreamDone) {
			return idx
		}
	}
	return desired
}

func renderedRevealRowsStable(beforeRows []string, afterRows []string, revealCount int, totalPending int, streamKind string, actor string, upstreamDone bool) bool {
	if len(afterRows) == 0 {
		return false
	}
	start := 0
	if len(beforeRows) > 0 {
		start = len(beforeRows) - 1
		if start >= len(afterRows) {
			start = len(afterRows) - 1
		}
	}
	for idx := start; idx < len(afterRows); idx++ {
		if idx < len(beforeRows) && afterRows[idx] == beforeRows[idx] {
			continue
		}
		allowTinyTail := upstreamDone && idx == len(afterRows)-1 && revealCount >= totalPending
		if !stableRenderedNarrativeRow(afterRows[idx], streamKind, actor, allowTinyTail) {
			return false
		}
	}
	return true
}

func stableRenderedNarrativeRow(row string, streamKind string, actor string, allowTinyTail bool) bool {
	const minStableColumns = 6

	payload := stripFragileNarrativePrefix(stripStreamRolePrefix(row, streamKind, actor))
	if strings.TrimSpace(payload) == "" {
		return false
	}
	if allowTinyTail {
		return true
	}
	return displayColumns(payload) >= minStableColumns
}

func stripStreamRolePrefix(row string, streamKind string, actor string) string {
	row = strings.TrimLeft(row, " ")
	base := "· "
	if streamKind == "reasoning" {
		base = "› "
	}
	if actor = strings.TrimSpace(actor); actor != "" {
		prefix := base + actor + ": "
		if strings.HasPrefix(row, prefix) {
			return row[len(prefix):]
		}
	}
	return strings.TrimPrefix(row, base)
}

func stripFragileNarrativePrefix(text string) string {
	text = strings.TrimLeft(text, " ")
	for {
		switch {
		case strings.HasPrefix(text, "- "), strings.HasPrefix(text, "* "), strings.HasPrefix(text, "+ "), strings.HasPrefix(text, "> "):
			text = strings.TrimLeft(text[2:], " ")
		case headingPrefixLen(text) > 0:
			text = strings.TrimLeft(text[headingPrefixLen(text):], " ")
		case numberedListPrefixLen(text) > 0:
			text = strings.TrimLeft(text[numberedListPrefixLen(text):], " ")
		default:
			return text
		}
	}
}

func headingPrefixLen(text string) int {
	count := 0
	for count < len(text) && text[count] == '#' {
		count++
	}
	if count == 0 || count > 6 || count >= len(text) || text[count] != ' ' {
		return 0
	}
	return count + 1
}

func numberedListPrefixLen(text string) int {
	count := 0
	for count < len(text) && text[count] >= '0' && text[count] <= '9' {
		count++
	}
	if count == 0 || count+1 >= len(text) || text[count] != '.' || text[count+1] != ' ' {
		return 0
	}
	return count + 2
}

func renderStreamNarrativePlainRows(raw string, streamKind string, actor string, wrapWidth int) []string {
	if wrapWidth <= 0 {
		wrapWidth = 1
	}

	var plainRows []string
	switch streamKind {
	case "reasoning":
		plainRows = renderReasoningPlainRows(raw, actor)
	default:
		plainRows = renderAssistantPlainRows(raw, actor)
	}

	rows := make([]string, 0, len(plainRows))
	for _, row := range plainRows {
		segments := graphemeHardWrap(row, wrapWidth)
		if len(segments) == 0 {
			rows = append(rows, "")
			continue
		}
		rows = append(rows, segments...)
	}
	if len(rows) == 0 {
		return []string{""}
	}
	return rows
}

func renderAssistantPlainRows(raw string, actor string) []string {
	_, plainRows := buildNarrativeRows(raw)
	actorPrefix := ""
	if actor = strings.TrimSpace(actor); actor != "" {
		actorPrefix = actor + ": "
	}
	if len(plainRows) == 0 {
		return []string{"· " + actorPrefix}
	}
	rows := make([]string, 0, len(plainRows))
	for i, pr := range plainRows {
		if i == 0 {
			rows = append(rows, "· "+actorPrefix+pr)
			continue
		}
		rows = append(rows, pr)
	}
	return rows
}

func renderReasoningPlainRows(raw string, actor string) []string {
	_, plainRows := buildNarrativeRows(raw)
	actorPrefix := ""
	if actor = strings.TrimSpace(actor); actor != "" {
		actorPrefix = actor + ": "
	}
	if len(plainRows) == 0 {
		return []string{"› " + actorPrefix}
	}
	rows := make([]string, 0, len(plainRows))
	for i, pr := range plainRows {
		prefix := "  "
		if i == 0 {
			prefix = "› " + actorPrefix
		}
		rows = append(rows, prefix+pr)
	}
	return rows
}

func firstLogicalLineClusterLimit(clusters []string, limit int) int {
	if len(clusters) == 0 || limit <= 0 {
		return 0
	}
	if limit > len(clusters) {
		limit = len(clusters)
	}
	for idx := 1; idx <= limit; idx++ {
		if strings.Contains(clusters[idx-1], "\n") {
			return idx
		}
	}
	return limit
}

func minStableRevealCount(clusters []string, limit int) int {
	if len(clusters) == 0 || limit <= 0 {
		return 0
	}
	if limit > len(clusters) {
		limit = len(clusters)
	}
	const minStableClusters = 2
	const minStableColumns = 6

	columns := 0
	for idx := 1; idx <= limit; idx++ {
		columns += graphemeWidth(clusters[idx-1])
		if idx >= minStableClusters && columns >= minStableColumns {
			return idx
		}
	}
	return limit
}
