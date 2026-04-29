package tuiapp

import (
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/OnslaughtSnail/caelis/tui/tuikit"
	"github.com/charmbracelet/x/ansi"
)

// ---------------------------------------------------------------------------
// Layout helpers
// ---------------------------------------------------------------------------

// computeLayout returns (viewportHeight, bottomHeight).
func (m *Model) computeLayout() (int, int) {
	bottomHeight := m.bottomSectionHeight()
	vpHeight := maxInt(1, m.height-bottomHeight)
	return vpHeight, bottomHeight
}

// bottomSectionHeight calculates how many lines the fixed bottom area needs.
func (m *Model) bottomSectionHeight() int {
	lines := 0

	// Spacer + optional plan + optional pending queue + hint row + hint/header
	// gap + workspace/model row + composer section label.
	lines += m.preComposerFixedHeight()
	lines += m.promptModalReservedHeight()

	// Composer top padding between workspace/model row and input.
	lines += tuikit.ComposerPadTop

	// Input bar (with minimum height).
	inputH := maxInt(tuikit.ComposerMinHeight, m.textarea.Height())
	lines += inputH

	// Composer bottom padding.
	lines += tuikit.ComposerPadBottom

	// Lower separator + status footer.
	lines += 2

	// Status bar bottom padding.
	lines += tuikit.StatusBarPadBottom

	return lines
}

func (m *Model) promptModalReservedHeight() int {
	if m == nil || m.activePrompt == nil || m.width <= 0 || m.height <= 0 {
		return 0
	}
	modal := ansi.Strip(m.renderPromptModal())
	if strings.TrimSpace(modal) == "" {
		return 0
	}
	return strings.Count(modal, "\n") + 1
}

// renderedStyledLines returns the unwrapped styled lines from all document
// blocks. This replaces the old historyLines cache with an on-demand
// computation directly from the document model.
func (m *Model) renderedStyledLines() []string {
	ctx := m.blockRenderContext(maxInt(1, m.viewport.Width()))
	var lines []string
	for _, block := range m.doc.Blocks() {
		for _, row := range block.Render(ctx) {
			lines = append(lines, row.Styled)
		}
	}
	streamStyled, _, _ := m.renderStreamViewportLines(ctx)
	lines = append(lines, streamStyled...)
	return lines
}

// syncViewportContent rebuilds the viewport content from the document model
// plus any in-progress streaming content, then sets it on the viewport.
// Both styled and plain text are wrapped independently from RenderedRow,
// making RenderedRow the single layout truth.
func (m *Model) syncViewportContent() {
	if m.viewportSyncDepth > 0 {
		m.viewportDirty = true
		return
	}
	m.viewportSyncPending = false
	m.offscreenViewportDirty = false
	m.offscreenViewportTickScheduled = false
	m.offscreenViewportSyncAt = time.Time{}
	wrapWidth := maxInt(1, m.viewport.Width())
	ctx := m.blockRenderContext(wrapWidth)
	contextKey := viewportRenderContextKey(ctx)
	activeTailOnly := m.dirtyViewportBlocksOnlyActiveNarrative()
	incremental := false
	if len(m.dirtyViewportBlocks) == 0 &&
		!m.viewportStructureDirty &&
		m.lastViewportRenderContextKey == contextKey &&
		m.viewportRenderCacheMatchesDocument(ctx) {
		if m.streamLine == m.lastViewportStreamLine {
			return
		}
		m.rebuildViewportLineCaches(ctx)
		incremental = true
	} else {
		incremental = m.syncDirtyViewportRenderEntries(ctx)
	}
	if !incremental {
		m.rebuildViewportRenderCache(ctx)
		m.rebuildViewportLineCaches(ctx)
		m.viewportStructureDirty = false
		m.diag.ViewportFullSyncs++
	} else {
		m.diag.ViewportIncrementalSyncs++
	}
	syncReason := "incremental_sync"
	if !incremental {
		syncReason = "full_sync"
	}
	m.lastViewportRenderContextKey = contextKey
	clear(m.dirtyViewportBlocks)
	m.viewportContentVersion++
	m.lastViewportStreamLine = m.streamLine

	m.renderViewportContent(syncReason, activeTailOnly)
}

func (m *Model) dirtyViewportBlocksOnlyActiveNarrative() bool {
	if m == nil || len(m.dirtyViewportBlocks) == 0 || m.viewportStructureDirty {
		return false
	}
	if m.streamLine != m.lastViewportStreamLine {
		return false
	}
	for blockID := range m.dirtyViewportBlocks {
		switch block := m.doc.Find(blockID).(type) {
		case *AssistantBlock:
			if !block.Streaming || block.activeBuffer == nil || block.activeBuffer.Empty() {
				return false
			}
		case *ReasoningBlock:
			if !block.Streaming || block.activeBuffer == nil || block.activeBuffer.Empty() {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (m *Model) beginDeferredViewportSync() {
	if m == nil {
		return
	}
	m.viewportSyncDepth++
}

func (m *Model) endDeferredViewportSync() {
	if m == nil || m.viewportSyncDepth == 0 {
		return
	}
	m.viewportSyncDepth--
	if m.viewportSyncDepth == 0 && m.viewportDirty {
		m.viewportDirty = false
		m.syncViewportContent()
	}
}

func (m *Model) renderedRowWrapMode(blockID string) BlockKind {
	if blockID == "" {
		return ""
	}
	block := m.doc.Find(blockID)
	if block == nil {
		return ""
	}
	return block.Kind()
}

func (m *Model) wrapNarrativeRowStyled(row RenderedRow, width int) string {
	if width <= 0 {
		return row.Styled
	}
	plain := ansi.Strip(row.Styled)
	// If the line already fits, preserve the original styled text (which may
	// include inline markdown formatting from block.Render).
	if graphemeWidth(plain) <= width {
		return row.Styled
	}
	// Word-wrap plain text, then re-apply inline styling per segment.
	segments := graphemeWordWrap(plain, width)
	if len(segments) == 0 {
		return ""
	}
	roleStyle := tuikit.LineStyleAssistant
	if m.renderedRowWrapMode(row.BlockID) == BlockReasoning {
		roleStyle = tuikit.LineStyleReasoning
	}
	baseStyle := narrativeBodyStyle(roleStyle, m.theme)
	styled := make([]string, 0, len(segments))
	for _, segment := range segments {
		styled = append(styled, m.renderInlineMarkdown(segment, baseStyle))
	}
	return strings.Join(styled, "\n")
}

func (m *Model) wrapNarrativeRowPlain(row RenderedRow, width int) []string {
	plain := strings.TrimRight(row.Plain, " ")
	if plain == "" {
		plain = strings.TrimRight(ansi.Strip(row.Styled), " ")
	}
	if width <= 0 {
		return []string{plain}
	}
	if graphemeWidth(plain) <= width {
		return []string{plain}
	}
	segments := graphemeWordWrap(plain, width)
	if len(segments) == 0 {
		return []string{""}
	}
	return normalizeWrappedPlainSegments(segments)
}

func normalizeWrappedPlainSegments(segments []string) []string {
	if len(segments) == 0 {
		return nil
	}
	out := make([]string, len(segments))
	for i, seg := range segments {
		out[i] = strings.TrimRight(seg, " ")
	}
	return out
}

func (m *Model) adaptHistoryLineForViewport(line string, wrapWidth int) string {
	plain := strings.TrimSpace(ansi.Strip(line))
	prefix := ""
	switch {
	case strings.HasPrefix(plain, "▸ SPAWN "):
		prefix = "▸ SPAWN "
	default:
		return line
	}
	taskText := strings.TrimSpace(strings.TrimPrefix(plain, prefix))
	if taskText == "" {
		return line
	}
	style := tuikit.LineStyleTool
	gutter := tuikit.LineExtraGutter(style)
	available := max(wrapWidth-displayColumns(gutter)-displayColumns(prefix), 16)
	targetWidth := minInt(available, maxInt(24, wrapWidth*2/3))
	adapted := prefix + truncateMiddleDisplay(taskText, targetWidth)
	colored := tuikit.ColorizeLogLine(adapted, style, m.theme)
	return gutter + colored
}

// deriveViewportPlainLines strips ANSI from styled lines to produce plain text.
// This is the rendered-text-first approach: what the user sees on screen
// (minus colors) is what they get when copying.
func deriveViewportPlainLines(buf []string, styledLines []string) []string {
	if cap(buf) < len(styledLines) {
		buf = make([]string, 0, len(styledLines))
	}
	for _, sl := range styledLines {
		buf = append(buf, strings.TrimRight(ansi.Strip(sl), " "))
	}
	return buf
}

func viewportLinesFingerprint(lines []string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strconv.Itoa(len(lines))))
	_, _ = h.Write([]byte{0})
	for _, line := range lines {
		_, _ = h.Write([]byte(line))
		_, _ = h.Write([]byte{0})
	}
	return strconv.FormatUint(h.Sum64(), 16)
}

func truncateMiddleDisplay(text string, width int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" || width <= 0 || displayColumns(text) <= width {
		return text
	}
	ellipsis := "......"
	ellipsisWidth := displayColumns(ellipsis)
	if width <= ellipsisWidth {
		return sliceByDisplayColumns(text, 0, width)
	}
	head := (width - ellipsisWidth) * 2 / 3
	tail := (width - ellipsisWidth) - head
	if head <= 0 {
		head = 1
	}
	if tail <= 0 {
		tail = 1
	}
	total := displayColumns(text)
	prefix := sliceByDisplayColumns(text, 0, head)
	suffix := sliceByDisplayColumns(text, total-tail, total)
	return prefix + ellipsis + suffix
}

func (m *Model) renderViewportContent(reason string, activeTailOnly bool) {
	start := time.Now()
	lines := m.viewportStyledLines
	if m.lastViewportContentVersion != m.viewportContentVersion {
		fingerprint := viewportLinesFingerprint(lines)
		if fingerprint != m.lastViewportContent {
			if activeTailOnly && m.isViewportFollowTail() {
				m.viewportContentStale = true
			} else {
				m.observeViewportSetContent(lines, reason)
				m.viewport.SetContentLines(append([]string(nil), lines...))
				m.lastViewportContent = fingerprint
				m.viewportContentStale = false
			}
			m.lastViewportViewKey = ""
		}
		m.lastViewportContentVersion = m.viewportContentVersion
	}

	// Auto-scroll: decide based on current state AFTER SetContent so
	// that scroll decisions use the up-to-date content length. The
	// previous approach sampled AtBottom() before SetContent, which
	// could produce the wrong decision when content/height changed.
	if m.isViewportFollowTail() {
		if !m.viewportContentStale {
			m.viewport.GotoBottom()
		}
	}
	m.streamPlayback.LastFrameRenderCost = time.Since(start)
}

func (m *Model) materializeViewportContentIfStale() {
	if m == nil || !m.viewportContentStale {
		return
	}
	offset := m.viewportVisibleOffset()
	lines := append([]string(nil), m.viewportStyledLines...)
	m.observeViewportSetContent(lines, "stale_materialize")
	m.viewport.SetContentLines(lines)
	if maxOffset := m.viewportMaxOffset(); offset > maxOffset {
		offset = maxOffset
	}
	m.viewport.SetYOffset(offset)
	m.lastViewportContent = viewportLinesFingerprint(lines)
	m.viewportContentStale = false
	m.lastViewportViewKey = ""
}

func (m *Model) offscreenViewportSyncInterval() time.Duration {
	interval := m.streamTickInterval() * 5
	if interval < offscreenViewportSyncIntervalFloor {
		interval = offscreenViewportSyncIntervalFloor
	}
	if interval > offscreenViewportSyncIntervalMax {
		interval = offscreenViewportSyncIntervalMax
	}
	return interval
}

func (m *Model) shouldDeferStreamViewportSync() bool {
	if m == nil {
		return false
	}
	if m.selecting || m.inputSelecting || m.fixedSelecting {
		return true
	}
	if m.hasSelectionRange() {
		return true
	}
	return !m.isViewportFollowTail()
}

func (m *Model) ensureViewportSyncTick() tea.Cmd {
	if m == nil || !m.viewportSyncPending || m.viewportSyncTickScheduled {
		return nil
	}
	m.viewportSyncTickScheduled = true
	m.diag.ViewportQueuedSyncs++
	return frameTickCmd(frameTickViewportSync, m.streamTickInterval())
}

func (m *Model) ensureOffscreenViewportTick() tea.Cmd {
	if m == nil || !m.offscreenViewportDirty || m.offscreenViewportTickScheduled {
		return nil
	}
	delay := time.Until(m.offscreenViewportSyncAt)
	if delay <= 0 {
		delay = time.Millisecond
	}
	m.offscreenViewportTickScheduled = true
	return frameTickCmd(frameTickOffscreen, delay)
}

func (m *Model) requestStreamViewportSync() tea.Cmd {
	if m == nil {
		return nil
	}
	if !m.shouldDeferStreamViewportSync() {
		m.viewportSyncPending = true
		return m.ensureViewportSyncTick()
	}
	m.offscreenViewportDirty = true
	if m.offscreenViewportSyncAt.IsZero() {
		m.offscreenViewportSyncAt = time.Now().Add(m.offscreenViewportSyncInterval())
	}
	return m.ensureOffscreenViewportTick()
}

func (m *Model) flushPendingViewportSync() tea.Cmd {
	if m == nil || !m.viewportSyncPending {
		return nil
	}
	m.viewportSyncPending = false
	if m.shouldDeferStreamViewportSync() {
		m.offscreenViewportDirty = true
		if m.offscreenViewportSyncAt.IsZero() {
			m.offscreenViewportSyncAt = time.Now().Add(m.offscreenViewportSyncInterval())
		}
		return m.ensureOffscreenViewportTick()
	}
	m.syncViewportContent()
	return nil
}

func (m *Model) flushPendingOffscreenViewportSync(now time.Time) tea.Cmd {
	if m == nil || !m.offscreenViewportDirty {
		m.offscreenViewportSyncAt = time.Time{}
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	if m.shouldDeferStreamViewportSync() {
		m.flushDeferredMainStreamSmoothing()
		m.offscreenViewportSyncAt = now.Add(m.offscreenViewportSyncInterval())
		m.diag.ViewportSkippedSyncs++
		return m.ensureOffscreenViewportTick()
	}
	m.flushDeferredMainStreamSmoothing()
	m.syncViewportContent()
	return nil
}

// ensureViewportLayout reconciles the viewport height with the current
// bottom-section layout. Call this from Update() after any state change
// that may affect bottomSectionHeight (textarea resize, drawer toggle,
// pending queue change, etc.). Moving this out of View() avoids
// mutating viewport state during rendering, which can cause the scroll
// offset and visible content to desynchronize for one or more frames —
// producing the "invisible but selectable text" artefact.
func (m *Model) ensureViewportLayout() {
	vpHeight, _ := m.computeLayout()
	if m.viewport.Height() != vpHeight {
		m.viewport.SetHeight(vpHeight)
		m.syncViewportContent()
	}
}

func (m *Model) clearSelection() {
	changed := m.selecting || m.selectionStart.line >= 0 || m.selectionEnd.line >= 0
	m.selecting = false
	m.selectionStart = textSelectionPoint{line: -1, col: -1}
	m.selectionEnd = textSelectionPoint{line: -1, col: -1}
	if m.viewportFollowState == viewportSelecting {
		m.leaveViewportSelecting()
	}
	if changed {
		m.bumpViewportSelectionVersion()
	}
}

func (m *Model) bumpViewportSelectionVersion() {
	if m == nil {
		return
	}
	m.viewportSelectionVersion++
	m.lastViewportViewKey = ""
}

func (m *Model) markViewportBlockDirty(blockID string) {
	blockID = strings.TrimSpace(blockID)
	if m == nil || blockID == "" {
		return
	}
	if m.dirtyViewportBlocks == nil {
		m.dirtyViewportBlocks = make(map[string]struct{})
	}
	m.dirtyViewportBlocks[blockID] = struct{}{}
}

func (m *Model) markViewportStructureDirty() {
	if m == nil {
		return
	}
	m.viewportStructureDirty = true
}

func (m *Model) clearInputSelection() {
	m.inputSelecting = false
	m.inputSelectionStart = textSelectionPoint{line: -1, col: -1}
	m.inputSelectionEnd = textSelectionPoint{line: -1, col: -1}
}

func (m *Model) clearFixedSelection() {
	m.fixedSelecting = false
	m.fixedSelectionArea = fixedSelectionNone
	m.fixedSelectionStart = textSelectionPoint{line: -1, col: -1}
	m.fixedSelectionEnd = textSelectionPoint{line: -1, col: -1}
}

func (m *Model) hasSelectionRange() bool {
	start, end, ok := normalizedSelectionRange(m.selectionStart, m.selectionEnd, len(m.viewportPlainLines))
	if !ok {
		return false
	}
	return start.line != end.line || start.col != end.col
}

func (m *Model) mousePointToContentPoint(x int, y int, clamp bool) (textSelectionPoint, bool) {
	y = m.screenYToFrameY(y)
	if len(m.viewportPlainLines) == 0 || m.viewport.Height() <= 0 {
		return textSelectionPoint{}, false
	}
	if !clamp {
		columnStart := m.mainColumnX()
		columnEnd := columnStart + m.mainColumnWidth()
		if x < columnStart || x >= columnEnd {
			return textSelectionPoint{}, false
		}
	}
	vy := y
	if clamp {
		if vy < 0 {
			vy = 0
		}
		if vy >= m.viewport.Height() {
			vy = m.viewport.Height() - 1
		}
	} else if vy < 0 || vy >= m.viewport.Height() {
		return textSelectionPoint{}, false
	}

	line := max(m.viewportVisibleOffset()+vy, 0)
	if line >= len(m.viewportPlainLines) {
		line = len(m.viewportPlainLines) - 1
	}

	col := max(x-m.mainColumnX()-tuikit.GutterNarrative, 0)
	width := displayColumns(m.viewportPlainLines[line])
	if col > width {
		col = width
	}
	return textSelectionPoint{line: line, col: col}, true
}

func (m *Model) inputAreaBounds() (startY int, height int, ok bool) {
	y := m.viewport.Height()
	y += m.preComposerFixedHeight()
	// composer top padding
	y += tuikit.ComposerPadTop
	h := maxInt(tuikit.ComposerMinHeight, m.textarea.Height())
	return y, h, true
}

func (m *Model) mousePointToInputPoint(x int, y int, clamp bool, lines []string) (textSelectionPoint, bool) {
	y = m.screenYToFrameY(y)
	startY, height, ok := m.inputAreaBounds()
	if !ok || len(lines) == 0 {
		return textSelectionPoint{}, false
	}
	ry := y - startY
	if clamp {
		if ry < 0 {
			ry = 0
		}
		if ry >= height {
			ry = height - 1
		}
	} else if ry < 0 || ry >= height {
		return textSelectionPoint{}, false
	}
	if ry >= len(lines) {
		ry = len(lines) - 1
	}
	col := max(x-m.mainColumnX()-inputHorizontalInset, 0)
	width := displayColumns(lines[ry])
	if col > width {
		col = width
	}
	return textSelectionPoint{line: ry, col: col}, true
}

func (m *Model) selectionText() string {
	start, end, ok := normalizedSelectionRange(m.selectionStart, m.selectionEnd, len(m.viewportPlainLines))
	if !ok {
		return ""
	}
	return selectionTextFromLines(m.viewportPlainLines, start, end)
}

func (m *Model) renderSelectionLines() []string {
	start, end, ok := normalizedSelectionRange(m.selectionStart, m.selectionEnd, len(m.viewportPlainLines))
	if !ok {
		return append([]string(nil), m.viewportStyledLines...)
	}
	// Rendered-text-first selection: non-selected lines keep styled output,
	// selected lines show plain text with reverse highlight.
	return renderSelectionOnStyledLines(m.viewportStyledLines, m.viewportPlainLines, start, end)
}

type fixedTextRegion struct {
	area fixedSelectionArea
	y    int
	text string
}

func (m *Model) fixedTextRegions() []fixedTextRegion {
	layout := m.fixedRowLayout()
	return []fixedTextRegion{
		{area: fixedSelectionHint, y: layout.hintY, text: m.hintRowText()},
		{area: fixedSelectionHeader, y: layout.headerY, text: m.headerRowText()},
		{area: fixedSelectionFooter, y: layout.footerY, text: m.footerRowText()},
	}
}

type fixedRowLayout struct {
	hintY   int
	headerY int
	footerY int
}

func (m *Model) fixedRowLayout() fixedRowLayout {
	y := m.viewport.Height()
	layout := fixedRowLayout{
		hintY:   y + 1 + m.primaryDrawerOffsetHeight() + m.pendingQueueSectionHeight(),
		headerY: y + 3 + m.primaryDrawerOffsetHeight() + m.pendingQueueSectionHeight(),
	}
	y += m.preComposerFixedHeight()
	y += tuikit.ComposerPadTop
	y += maxInt(tuikit.ComposerMinHeight, m.textarea.Height())
	y += tuikit.ComposerPadBottom // composer bottom padding
	y++                           // lower separator
	layout.footerY = y
	return layout
}

func (m *Model) preComposerFixedHeight() int {
	return 5 + m.primaryDrawerOffsetHeight() + m.pendingQueueSectionHeight()
}

func (m *Model) primaryDrawerOffsetHeight() int {
	height := m.primaryDrawerHeight()
	if height <= 0 {
		return 0
	}
	return height + 1
}

func (m *Model) pendingQueueSectionHeight() int {
	if m.pendingQueue == nil || m.width <= 0 {
		return 0
	}
	return 3
}

func (m *Model) fixedRegionAt(y int) (fixedTextRegion, bool) {
	y = m.screenYToFrameY(y)
	for _, region := range m.fixedTextRegions() {
		if region.y == y {
			return region, true
		}
	}
	return fixedTextRegion{}, false
}

func (m *Model) screenYToFrameY(y int) int {
	if y < 0 {
		return y
	}
	return y + maxInt(0, m.frameTopTrim)
}

func (m *Model) fixedRowPoint(region fixedTextRegion, x int, clamp bool) (textSelectionPoint, bool) {
	contentWidth := m.fixedRowContentWidth()
	col := x - m.mainColumnX() - tuikit.StatusInset // account for status-row horizontal padding
	if clamp {
		if col < 0 {
			col = 0
		}
		if col > contentWidth {
			col = contentWidth
		}
	} else if col < 0 || col > contentWidth {
		return textSelectionPoint{}, false
	}
	lineWidth := displayColumns(region.text)
	if col > lineWidth {
		col = lineWidth
	}
	return textSelectionPoint{line: 0, col: col}, true
}

func (m *Model) fixedSelectionText() string {
	if m.fixedSelectionArea == fixedSelectionNone {
		return ""
	}
	start, end, ok := normalizedSelectionRange(m.fixedSelectionStart, m.fixedSelectionEnd, 1)
	if !ok {
		return ""
	}
	for _, region := range m.fixedTextRegions() {
		if region.area == m.fixedSelectionArea {
			return selectionTextFromLines([]string{region.text}, start, end)
		}
	}
	return ""
}

func (m *Model) renderFixedRow(area fixedSelectionArea, plain string, rendered string, style lipgloss.Style) string {
	line := plain
	if m.fixedSelectionArea == area {
		start, end, ok := normalizedSelectionRange(m.fixedSelectionStart, m.fixedSelectionEnd, 1)
		if ok && (start.line != end.line || start.col != end.col) {
			line = renderSelectionOnLines([]string{plain}, start, end)[0]
			return style.Render(line)
		}
	}
	if rendered == "" {
		rendered = line
	}
	return style.Render(rendered)
}

// indentBlock adds a fixed left margin to every line of a multi-line block.
func indentBlock(block string, indent int) string {
	if indent <= 0 || block == "" {
		return block
	}
	pad := strings.Repeat(" ", indent)
	lines := strings.Split(block, "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}
