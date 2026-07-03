package tuiapp

import (
	"fmt"
	"hash"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/ports/displaypolicy"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/x/ansi"
)

type viewportRenderEntry struct {
	blockID     string
	cacheKey    string
	rhythm      viewportRhythmClass
	lineStart   int
	lineCount   int
	styledLines []string
	plainLines  []string
	clickTokens []string
}

type viewportRhythmClass string

const (
	viewportRhythmNone      viewportRhythmClass = ""
	viewportRhythmUser      viewportRhythmClass = "user"
	viewportRhythmAssistant viewportRhythmClass = "assistant"
	viewportRhythmReasoning viewportRhythmClass = "reasoning"
	viewportRhythmOperation viewportRhythmClass = "operation"
	viewportRhythmPanel     viewportRhythmClass = "panel"
)

func (m *Model) rebuildViewportRenderCache(ctx BlockRenderContext) {
	oldEntries := make(map[string]viewportRenderEntry, len(m.viewportRenderEntries))
	for _, entry := range m.viewportRenderEntries {
		oldEntries[entry.blockID] = entry
	}

	nextEntries := make([]viewportRenderEntry, 0, m.doc.Len())
	for _, block := range m.doc.Blocks() {
		key := viewportBlockRenderKey(block, ctx)
		if cached, ok := oldEntries[block.BlockID()]; ok && cached.cacheKey == key {
			nextEntries = append(nextEntries, cached)
			continue
		}
		nextEntries = append(nextEntries, m.renderViewportEntry(block, key, ctx))
	}
	m.viewportRenderEntries = nextEntries
}

func (m *Model) viewportRenderCacheMatchesDocument(ctx BlockRenderContext) bool {
	if m == nil || m.doc == nil {
		return false
	}
	blocks := m.doc.Blocks()
	if len(m.viewportRenderEntries) != len(blocks) {
		return false
	}
	for i, block := range blocks {
		if block == nil {
			return false
		}
		entry := m.viewportRenderEntries[i]
		if entry.blockID != block.BlockID() {
			return false
		}
		if entry.cacheKey != viewportBlockRenderKey(block, ctx) {
			return false
		}
	}
	return true
}

func (m *Model) renderViewportEntry(block Block, cacheKey string, ctx BlockRenderContext) viewportRenderEntry {
	m.observeBlockRender(block.Kind())
	styledLines, plainLines, clickTokens := m.wrapRenderedRowsForViewport(block, block.Render(ctx), ctx.Width, ctx)
	return viewportRenderEntry{
		blockID:     block.BlockID(),
		cacheKey:    cacheKey,
		rhythm:      viewportRhythmForBlock(block),
		styledLines: styledLines,
		plainLines:  plainLines,
		clickTokens: clickTokens,
	}
}

func (m *Model) wrapRenderedRowsForViewport(block Block, rawRows []RenderedRow, wrapWidth int, ctx BlockRenderContext) ([]string, []string, []string) {
	if wrapWidth <= 0 {
		wrapWidth = 1
	}
	styledLines := make([]string, 0, len(rawRows)+8)
	plainLines := make([]string, 0, len(rawRows)+8)
	clickTokens := make([]string, 0, len(rawRows)+8)

	for _, row := range rawRows {
		styledLine := m.adaptHistoryLineForViewport(row.Styled, wrapWidth)
		plainLine := strings.TrimRight(ansi.Strip(styledLine), " ")
		if isACPTranscriptBlockKind(block.Kind()) && row.ACPHeader {
			sourcePlain := strings.TrimRight(row.Plain, " ")
			if strings.TrimSpace(sourcePlain) == "" {
				sourcePlain = plainLine
			}
			if wrappedPlain, wrappedStyled, ok := wrapACPTranscriptHeaderForViewport(sourcePlain, wrapWidth, ctx); ok {
				styledLines = append(styledLines, wrappedStyled...)
				plainLines = append(plainLines, wrappedPlain...)
				for range wrappedStyled {
					clickTokens = append(clickTokens, row.ClickToken)
				}
				continue
			}
		}
		if isACPTranscriptBlockKind(block.Kind()) {
			sourcePlain := strings.TrimRight(row.Plain, " ")
			if strings.TrimSpace(sourcePlain) == "" {
				sourcePlain = plainLine
			}
			if wrappedPlain, wrappedStyled, ok := wrapACPTranscriptNarrativeForViewport(sourcePlain, styledLine, wrapWidth, ctx); ok {
				styledLines = append(styledLines, wrappedStyled...)
				plainLines = append(plainLines, wrappedPlain...)
				for range wrappedStyled {
					clickTokens = append(clickTokens, row.ClickToken)
				}
				continue
			}
		}

		var wrappedStyled string
		var plainParts []string

		if row.PreWrapped {
			if graphemeWidth(plainLine) > wrapWidth {
				wrappedStyled = hardWrapDisplayLine(styledLine, wrapWidth)
				plainParts = normalizeWrappedPlainSegments(graphemeHardWrap(plainLine, wrapWidth))
			} else {
				wrappedStyled = styledLine
				plainParts = []string{plainLine}
			}
		} else {
			switch block.Kind() {
			case BlockAssistant, BlockReasoning:
				wrappedStyled = m.wrapNarrativeRowStyled(row, wrapWidth)
				plainParts = m.wrapNarrativeRowPlain(row, wrapWidth)
			case BlockMainACPTurn, BlockParticipantTurn:
				wrappedStyled = hardWrapDisplayLine(styledLine, wrapWidth)
				plainParts = normalizeWrappedPlainSegments(graphemeHardWrap(plainLine, wrapWidth))
			default:
				wrappedStyled = hardWrapDisplayLine(styledLine, wrapWidth)
				plainParts = normalizeWrappedPlainSegments(graphemeHardWrap(plainLine, wrapWidth))
			}
		}

		if wrappedStyled == "" {
			styledLines = append(styledLines, "")
			plainLines = append(plainLines, "")
			clickTokens = append(clickTokens, row.ClickToken)
			continue
		}

		sParts := strings.Split(wrappedStyled, "\n")
		if len(plainParts) != len(sParts) {
			plainParts = deriveViewportPlainLines(plainParts[:0], sParts)
		}
		styledLines = append(styledLines, sParts...)
		plainLines = append(plainLines, plainParts...)
		for range sParts {
			clickTokens = append(clickTokens, row.ClickToken)
		}
	}

	return styledLines, plainLines, clickTokens
}

func isACPTranscriptBlockKind(kind BlockKind) bool {
	return kind == BlockMainACPTurn || kind == BlockParticipantTurn
}

func wrapACPTranscriptNarrativeForViewport(plain string, styled string, width int, ctx BlockRenderContext) ([]string, []string, bool) {
	if width <= 0 {
		width = 1
	}
	prefix, lineStyle, ok := splitACPTranscriptNarrativePrefix(plain)
	if !ok || strings.TrimSpace(strings.TrimPrefix(plain, prefix)) == "" || graphemeWidth(plain) <= width {
		return nil, nil, false
	}
	continuationPrefix := strings.Repeat(" ", displayColumns(prefix))
	bodyWidth := maxInt(1, width-displayColumns(prefix))
	body := strings.ReplaceAll(strings.ReplaceAll(strings.TrimPrefix(plain, prefix), "\r\n", "\n"), "\r", "\n")
	styledBody := styledACPTranscriptNarrativeBody(styled, displayColumns(prefix), body)
	bodyStyledSegments := wrapStyledACPTranscriptNarrativeBody(styledBody, bodyWidth)
	bodyPlainSegments := deriveViewportPlainLines(nil, bodyStyledSegments)
	plainLines := make([]string, 0, len(bodyPlainSegments))
	styledLines := make([]string, 0, len(bodyStyledSegments))
	prefixStyled := styleACPTranscriptNarrativePrefix(ctx, prefix, lineStyle)
	for i, segment := range bodyStyledSegments {
		linePrefix := prefix
		linePrefixStyled := prefixStyled
		if i > 0 {
			linePrefix = continuationPrefix
			linePrefixStyled = continuationPrefix
		}
		plainLines = append(plainLines, linePrefix+bodyPlainSegments[i])
		styledLines = append(styledLines, linePrefixStyled+segment)
	}
	if len(plainLines) < 2 {
		return nil, nil, false
	}
	return plainLines, styledLines, true
}

func styledACPTranscriptNarrativeBody(styled string, prefixWidth int, fallbackPlain string) string {
	styled = strings.ReplaceAll(strings.ReplaceAll(styled, "\r\n", "\n"), "\r", "\n")
	body := strings.TrimRight(ansi.Cut(styled, prefixWidth, ansi.StringWidth(styled)), " ")
	if strings.TrimSpace(ansi.Strip(body)) != "" {
		return body
	}
	return fallbackPlain
}

func wrapStyledACPTranscriptNarrativeBody(styledBody string, bodyWidth int) []string {
	if bodyWidth <= 0 {
		bodyWidth = 1
	}
	wrapped := ansi.Wrap(styledBody, bodyWidth, " ")
	segments := strings.Split(wrapped, "\n")
	if len(segments) == 0 {
		return []string{styledBody}
	}
	return segments
}

func splitACPTranscriptNarrativePrefix(plain string) (string, tuikit.LineStyle, bool) {
	switch {
	case strings.HasPrefix(plain, "› "):
		return "› ", tuikit.LineStyleReasoning, true
	case strings.HasPrefix(plain, "· "):
		return "· ", tuikit.LineStyleAssistant, true
	default:
		return "", tuikit.LineStyleDefault, false
	}
}

func styleACPTranscriptNarrativePrefix(ctx BlockRenderContext, prefix string, lineStyle tuikit.LineStyle) string {
	switch lineStyle {
	case tuikit.LineStyleReasoning:
		return ctx.Theme.ReasoningStyle().Render(prefix)
	case tuikit.LineStyleAssistant:
		return ctx.Theme.AssistantStyle().Render(prefix)
	default:
		return ctx.Theme.TextStyle().Render(prefix)
	}
}

func wrapACPTranscriptHeaderForViewport(plain string, width int, ctx BlockRenderContext) ([]string, []string, bool) {
	if width <= 0 {
		width = 1
	}
	if isApprovalReviewHeaderPlain(plain) {
		return nil, nil, false
	}
	prefix, detail, ok := splitACPTranscriptHeaderPrefix(plain)
	if !ok || strings.TrimSpace(detail) == "" {
		return nil, nil, false
	}
	verb := strings.TrimSpace(strings.TrimPrefix(prefix, "•"))
	continuationPrefix := strings.Repeat(" ", displayColumns(prefix))
	if acpTranscriptHeaderUsesRailContinuation(verb) {
		continuationPrefix = "  │ "
	}
	firstBodyWidth := maxInt(1, width-displayColumns(prefix))
	continuationBodyWidth := maxInt(1, width-displayColumns(continuationPrefix))
	rawLines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(detail, "\r\n", "\n"), "\r", "\n"), "\n")
	plainLines := make([]string, 0, len(rawLines)+2)
	for i, raw := range rawLines {
		text := strings.TrimSpace(raw)
		if text == "" {
			continue
		}
		bodyWidth := firstBodyWidth
		if len(plainLines) > 0 || i > 0 {
			bodyWidth = continuationBodyWidth
		}
		segments := graphemeWordWrap(text, bodyWidth)
		for j, segment := range segments {
			if i == 0 && j == 0 {
				plainLines = append(plainLines, prefix+segment)
			} else {
				plainLines = append(plainLines, continuationPrefix+segment)
			}
		}
	}
	if len(plainLines) == 0 {
		return nil, nil, false
	}
	styledLines := make([]string, 0, len(plainLines))
	for i, line := range plainLines {
		if i == 0 {
			styledLines = append(styledLines, styleACPTranscriptHeader(ctx, line))
			continue
		}
		styledLines = append(styledLines, styleACPTranscriptHeaderContinuation(ctx, verb, continuationPrefix, line))
	}
	return plainLines, styledLines, true
}

func isApprovalReviewHeaderPlain(plain string) bool {
	return strings.HasPrefix(strings.TrimSpace(plain), "• Automatic approval review ")
}

func acpTranscriptHeaderUsesRailContinuation(verb string) bool {
	switch strings.ToLower(strings.TrimSpace(verb)) {
	case "ran", "spawned":
		return true
	default:
		return false
	}
}

func styleACPTranscriptHeaderContinuation(ctx BlockRenderContext, verb string, prefix string, line string) string {
	if prefix != "" && strings.HasPrefix(line, prefix) {
		detail := strings.TrimPrefix(line, prefix)
		styled := ctx.Theme.TranscriptMetaStyle().Render(prefix)
		if strings.EqualFold(strings.TrimSpace(verb), "Ran") {
			return styled + styleShellCommandText(ctx, detail)
		}
		return styled + ctx.Theme.ToolArgsStyle().Render(detail)
	}
	return ctx.Theme.SecondaryTextStyle().Render(line)
}

func splitACPTranscriptHeaderPrefix(plain string) (prefix string, detail string, ok bool) {
	plain = strings.TrimRight(plain, " ")
	if !strings.HasPrefix(plain, "• ") {
		return "", "", false
	}
	rest := strings.TrimPrefix(plain, "• ")
	verb, detail, found := strings.Cut(rest, " ")
	if !found || strings.TrimSpace(verb) == "" {
		return "", "", false
	}
	return "• " + verb + " ", detail, true
}

func (m *Model) rebuildViewportLineCaches(ctx BlockRenderContext) {
	styledLines := make([]string, 0, 64)
	plainLines := make([]string, 0, 64)
	blockIDs := make([]string, 0, 64)
	clickTokens := make([]string, 0, 64)

	var prevEntry *viewportRenderEntry
	for i := range m.viewportRenderEntries {
		entry := &m.viewportRenderEntries[i]
		if shouldInsertViewportRhythmGap(prevEntry, entry) {
			styledLines = append(styledLines, "")
			plainLines = append(plainLines, "")
			blockIDs = append(blockIDs, "")
			clickTokens = append(clickTokens, "")
		}
		entry.lineStart = len(styledLines)
		entry.lineCount = len(entry.styledLines)
		styledLines = append(styledLines, entry.styledLines...)
		plainLines = append(plainLines, entry.plainLines...)
		clickTokens = append(clickTokens, entry.clickTokens...)
		for range entry.styledLines {
			blockIDs = append(blockIDs, entry.blockID)
		}
		if viewportEntryHasVisibleContent(*entry) {
			prevEntry = entry
		}
	}

	streamStyled, streamPlain, streamBlockIDs := m.renderStreamViewportLines(ctx)
	styledLines = append(styledLines, streamStyled...)
	plainLines = append(plainLines, streamPlain...)
	blockIDs = append(blockIDs, streamBlockIDs...)
	for range streamStyled {
		clickTokens = append(clickTokens, "")
	}

	m.viewportStyledLines = append(m.viewportStyledLines[:0], styledLines...)
	m.viewportPlainLines = append(m.viewportPlainLines[:0], plainLines...)
	m.viewportBlockIDs = append(m.viewportBlockIDs[:0], blockIDs...)
	m.viewportClickTokens = append(m.viewportClickTokens[:0], clickTokens...)
}

func (m *Model) syncDirtyViewportRenderEntries(ctx BlockRenderContext) bool {
	if m == nil ||
		len(m.dirtyViewportBlocks) == 0 ||
		m.viewportStructureDirty ||
		m.lastViewportRenderContextKey != viewportRenderContextKey(ctx) {
		return false
	}
	entryIndexes := make([]int, 0, len(m.dirtyViewportBlocks))
	seen := make(map[int]struct{}, len(m.dirtyViewportBlocks))
	for blockID := range m.dirtyViewportBlocks {
		idx := m.viewportRenderEntryIndex(blockID)
		if idx < 0 {
			return false
		}
		if _, ok := seen[idx]; ok {
			continue
		}
		seen[idx] = struct{}{}
		entryIndexes = append(entryIndexes, idx)
	}
	slices.SortFunc(entryIndexes, func(a, b int) int {
		return m.viewportRenderEntries[b].lineStart - m.viewportRenderEntries[a].lineStart
	})
	for _, idx := range entryIndexes {
		old := m.viewportRenderEntries[idx]
		block := m.doc.Find(old.blockID)
		if block == nil {
			return false
		}
		key := viewportBlockRenderKey(block, ctx)
		next := m.renderViewportEntry(block, key, ctx)
		if next.rhythm != old.rhythm || viewportEntryHasVisibleContent(next) != viewportEntryHasVisibleContent(old) {
			return false
		}
		start := old.lineStart
		count := old.lineCount
		if start < 0 || count < 0 || start+count > len(m.viewportStyledLines) {
			return false
		}
		m.viewportStyledLines = spliceStrings(m.viewportStyledLines, start, count, next.styledLines)
		m.viewportPlainLines = spliceStrings(m.viewportPlainLines, start, count, next.plainLines)
		m.viewportClickTokens = spliceStrings(m.viewportClickTokens, start, count, next.clickTokens)
		blockIDs := make([]string, len(next.styledLines))
		for i := range blockIDs {
			blockIDs[i] = next.blockID
		}
		m.viewportBlockIDs = spliceStrings(m.viewportBlockIDs, start, count, blockIDs)
		next.lineStart = start
		next.lineCount = len(next.styledLines)
		m.viewportRenderEntries[idx] = next
		if delta := next.lineCount - count; delta != 0 {
			m.shiftViewportEntryLineStartsAfter(idx, delta)
		}
	}
	if m.streamLine != m.lastViewportStreamLine {
		m.rebuildViewportLineCaches(ctx)
	}
	return true
}

func viewportRenderContextKey(ctx BlockRenderContext) string {
	return strconv.Itoa(ctx.Width) + "|" + strconv.Itoa(ctx.TermWidth) + "|" + ctx.renderThemeKey() + "|" + strings.TrimSpace(ctx.Workspace)
}

func (m *Model) viewportRenderEntryIndex(blockID string) int {
	blockID = strings.TrimSpace(blockID)
	if m == nil || blockID == "" {
		return -1
	}
	for i := range m.viewportRenderEntries {
		if m.viewportRenderEntries[i].blockID == blockID {
			return i
		}
	}
	return -1
}

func (m *Model) shiftViewportEntryLineStartsAfter(changedIndex int, delta int) {
	if m == nil {
		return
	}
	for i := range m.viewportRenderEntries {
		if i > changedIndex {
			m.viewportRenderEntries[i].lineStart += delta
		}
	}
}

func spliceStrings(base []string, start int, count int, repl []string) []string {
	out := make([]string, 0, len(base)-count+len(repl))
	out = append(out, base[:start]...)
	out = append(out, repl...)
	out = append(out, base[start+count:]...)
	return out
}

func viewportRhythmForBlock(block Block) viewportRhythmClass {
	switch b := block.(type) {
	case *UserNarrativeBlock:
		return viewportRhythmUser
	case *AssistantBlock:
		return viewportRhythmAssistant
	case *ReasoningBlock:
		return viewportRhythmReasoning
	case *MainACPTurnBlock, *ParticipantTurnBlock:
		return viewportRhythmPanel
	case *TranscriptBlock:
		if strings.TrimSpace(b.Raw) == "" {
			return viewportRhythmNone
		}
		switch b.Style {
		case tuikit.LineStyleUser:
			return viewportRhythmUser
		case tuikit.LineStyleTool, tuikit.LineStyleWarn, tuikit.LineStyleError, tuikit.LineStyleNote:
			return viewportRhythmOperation
		default:
			return viewportRhythmOperation
		}
	default:
		return viewportRhythmNone
	}
}

func shouldInsertViewportRhythmGap(prev, current *viewportRenderEntry) bool {
	if prev == nil || current == nil {
		return false
	}
	if !viewportEntryHasVisibleContent(*current) {
		return false
	}
	prevClass := prev.rhythm
	currentClass := current.rhythm
	if prevClass == viewportRhythmNone || currentClass == viewportRhythmNone {
		return false
	}
	if prevClass == viewportRhythmOperation && currentClass == viewportRhythmOperation {
		return false
	}
	return prevClass != currentClass
}

func viewportEntryHasVisibleContent(entry viewportRenderEntry) bool {
	for _, line := range entry.plainLines {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

func (m *Model) renderStreamViewportLines(ctx BlockRenderContext) ([]string, []string, []string) {
	if strings.TrimSpace(m.streamLine) == "" {
		return nil, nil, nil
	}

	wrapWidth := maxInt(1, ctx.Width)
	if rows, ok := m.renderStreamViewportNarrativeRows(ctx, wrapWidth); ok {
		return renderedRowsToViewportLineSlices(rows)
	}

	var styledLines []string
	var plainLines []string
	var blockIDs []string

	streamLines := strings.Split(m.streamLine, "\n")
	prevStyle := m.lastCommittedStyle
	for _, sl := range streamLines {
		style := tuikit.DetectLineStyleWithContext(sl, prevStyle)

		colored := tuikit.ColorizeLogLine(sl, style, m.theme)
		wrappedStyled := hardWrapDisplayLine(colored, wrapWidth)
		plainParts := normalizeWrappedPlainSegments(graphemeHardWrap(sl, wrapWidth))

		if wrappedStyled == "" {
			styledLines = append(styledLines, "")
			plainLines = append(plainLines, "")
			blockIDs = append(blockIDs, "")
		} else {
			sParts := strings.Split(wrappedStyled, "\n")
			if len(plainParts) != len(sParts) {
				plainParts = deriveViewportPlainLines(plainParts[:0], sParts)
			}
			styledLines = append(styledLines, sParts...)
			plainLines = append(plainLines, plainParts...)
			for range sParts {
				blockIDs = append(blockIDs, "")
			}
		}

		prevStyle = style
	}

	return styledLines, plainLines, blockIDs
}

func (m *Model) renderStreamViewportNarrativeRows(ctx BlockRenderContext, width int) ([]RenderedRow, bool) {
	if m == nil || strings.TrimSpace(m.streamLine) == "" {
		return nil, false
	}
	firstLine := m.streamLine
	if idx := strings.IndexByte(firstLine, '\n'); idx >= 0 {
		firstLine = firstLine[:idx]
	}
	style := tuikit.DetectLineStyleWithContext(firstLine, m.lastCommittedStyle)
	switch style {
	case tuikit.LineStyleAssistant:
		rows := RenderTextWithContext(ctx, TextRenderRequest{
			Kind:           TextAssistant,
			Mode:           RenderStream,
			MarkdownPolicy: MarkdownStableTail,
			Raw:            m.streamLine,
			Width:          width,
			LineStyle:      tuikit.LineStyleAssistant,
		}).Rows
		return rows, true
	case tuikit.LineStyleReasoning:
		rows := RenderTextWithContext(ctx, TextRenderRequest{
			Kind:           TextReasoning,
			Mode:           RenderPlain,
			MarkdownPolicy: MarkdownNone,
			Raw:            m.streamLine,
			Width:          width,
			LineStyle:      tuikit.LineStyleReasoning,
		}).Rows
		return rows, true
	default:
		return nil, false
	}
}

func renderedRowsToViewportLineSlices(rows []RenderedRow) ([]string, []string, []string) {
	if len(rows) == 0 {
		return nil, nil, nil
	}
	styledLines := make([]string, 0, len(rows))
	plainLines := make([]string, 0, len(rows))
	blockIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		styledLines = append(styledLines, row.Styled)
		plainLines = append(plainLines, row.Plain)
		blockIDs = append(blockIDs, "")
	}
	return styledLines, plainLines, blockIDs
}

type blockKeyBuilder struct {
	hash hash.Hash64
}

func newBlockKeyBuilder(kind BlockKind, ctx BlockRenderContext) *blockKeyBuilder {
	h := fnv.New64a()
	b := &blockKeyBuilder{hash: h}
	b.addString(string(kind))
	b.addInt(ctx.Width)
	b.addInt(ctx.TermWidth)
	b.addString(ctx.renderThemeKey())
	b.addString(strings.TrimSpace(ctx.Workspace))
	return b
}

func (b *blockKeyBuilder) addString(v string) {
	_, _ = b.hash.Write([]byte(v))
	_, _ = b.hash.Write([]byte{0})
}

func (b *blockKeyBuilder) addBool(v bool) {
	if v {
		b.addString("1")
		return
	}
	b.addString("0")
}

func (b *blockKeyBuilder) addInt(v int) {
	b.addString(strconv.Itoa(v))
}

func (b *blockKeyBuilder) addTime(v time.Time) {
	if v.IsZero() {
		b.addString("0")
		return
	}
	b.addString(strconv.FormatInt(v.UnixNano(), 10))
}

func (b *blockKeyBuilder) String() string {
	return fmt.Sprintf("%x", b.hash.Sum64())
}

func viewportBlockRenderKey(block Block, ctx BlockRenderContext) string {
	builder := newBlockKeyBuilder(block.Kind(), ctx)

	switch b := block.(type) {
	case *TranscriptBlock:
		builder.addString(b.Raw)
		builder.addInt(int(b.Style))
		builder.addBool(b.PreStyled)
	case *UserNarrativeBlock:
		builder.addString(b.Raw)
	case *AssistantBlock:
		builder.addString(b.Actor)
		builder.addBool(b.Streaming)
		if b.Streaming && b.activeBuffer != nil && !b.activeBuffer.Empty() {
			builder.addString(b.activeBuffer.CacheKey())
		} else {
			builder.addString(b.Raw)
		}
		builder.addString(b.LastFinal)
	case *ReasoningBlock:
		builder.addString(b.Actor)
		builder.addBool(b.Streaming)
		if b.Streaming && b.activeBuffer != nil && !b.activeBuffer.Empty() {
			builder.addString(b.activeBuffer.CacheKey())
		} else {
			builder.addString(b.Raw)
		}
	case *ParticipantTurnBlock:
		builder.addString(b.SessionID)
		builder.addString(b.Actor)
		builder.addString(b.Status)
		builder.addTime(b.StartedAt)
		builder.addTime(b.EndedAt)
		writeExpandedTools(builder, b.ExpandedTools)
		writeExpandedTools(builder, b.ExpandedToolOutput)
		writeExpandedTools(builder, b.ExpandedThought)
		writeExpandedTools(builder, b.ExpandedExplore)
		writeToolPanelScrollStates(builder, b.ToolPanelScroll)
		writeSubagentEvents(builder, b.Events, ctx)
	case *DividerBlock:
		builder.addString(b.Label)
		builder.addString(b.Text)
	case *MainACPTurnBlock:
		builder.addString(b.TurnKey)
		builder.addString(b.Status)
		builder.addTime(b.StartedAt)
		builder.addTime(b.EndedAt)
		writeExpandedTools(builder, b.ExpandedTools)
		writeExpandedTools(builder, b.ExpandedToolOutput)
		writeExpandedTools(builder, b.ExpandedThought)
		writeExpandedTools(builder, b.ExpandedExplore)
		writeToolPanelScrollStates(builder, b.ToolPanelScroll)
		writeSubagentEvents(builder, b.Events, ctx)
	case *WelcomeBlock:
		builder.addString(b.Version)
		builder.addString(b.Workspace)
		builder.addString(b.ModelName)
	}

	return builder.String()
}

func writeExpandedTools(builder *blockKeyBuilder, values map[string]bool) {
	if len(values) == 0 {
		builder.addInt(0)
		return
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	builder.addInt(len(keys))
	for _, key := range keys {
		builder.addString(key)
		builder.addBool(values[key])
	}
}

func writeToolPanelScrollStates(builder *blockKeyBuilder, values map[string]toolPanelScrollState) {
	if len(values) == 0 {
		builder.addInt(0)
		return
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	builder.addInt(len(keys))
	for _, key := range keys {
		value := values[key]
		builder.addString(key)
		builder.addInt(value.Offset)
		builder.addBool(value.FollowTail)
		builder.addTime(value.ScrollbarVisibleUntil)
	}
}

func writeRenderedRows(builder *blockKeyBuilder, rows []RenderedRow) {
	builder.addInt(len(rows))
	for _, row := range rows {
		builder.addString(row.Styled)
		builder.addString(row.Plain)
		builder.addBool(row.PreWrapped)
	}
}

func writeSubagentEvents(builder *blockKeyBuilder, events []SubagentEvent, ctx BlockRenderContext) {
	builder.addInt(len(events))
	for _, event := range events {
		builder.addInt(int(event.Kind))
		builder.addString(event.Text)
		if event.ActiveBuffer != nil && !event.ActiveBuffer.Empty() {
			builder.addString(event.ActiveBuffer.CacheKey())
		} else {
			builder.addString("")
		}
		builder.addTime(event.StartedAt)
		builder.addTime(event.EndedAt)
		builder.addString(event.CallID)
		builder.addString(event.Name)
		builder.addString(event.ToolKind)
		builder.addString(event.Args)
		builder.addString(event.StartArgs)
		builder.addString(event.FullArgs)
		if event.Kind == SEToolCall && displaypolicy.IsTerminalPanelTool(event.Name, "") {
			builder.addString(toolOutputRenderKey(event.Name, event.Output, ctx.Width))
		} else {
			builder.addString(event.Output)
		}
		builder.addString(event.TaskID)
		builder.addString(event.TaskAction)
		builder.addString(event.TaskInput)
		builder.addString(event.TaskTargetKind)
		builder.addBool(event.Done)
		builder.addBool(event.Err)
		builder.addString(event.ApprovalTool)
		builder.addString(event.ApprovalCommand)
		builder.addInt(len(event.PlanEntries))
		for _, entry := range event.PlanEntries {
			builder.addString(entry.Content)
			builder.addString(entry.Status)
		}
	}
}
