package tuiapp

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

const subagentOutputOverlayTokenPrefix = "subagent_output_overlay:"

type subagentOutputStatus uint8

const (
	subagentOutputRunning subagentOutputStatus = iota
	subagentOutputSucceeded
	subagentOutputFailed
)

type subagentOutputOverlayGeometry struct {
	x            int
	y            int
	width        int
	height       int
	headerY      int
	footerY      int
	contentX     int
	contentY     int
	contentWidth int
	rowTokens    []string
	totalRows    int
}

type subagentOutputOverlayState struct {
	attachments        []inputAttachment
	historyAttachments [][]inputAttachment
	editorSelecting    bool
	editorSelectStart  textSelectionPoint
	editorSelectEnd    textSelectionPoint
	history            []string
	historyIndex       int
	editor             textarea.Model
	editorReady        bool
	editorOffset       int
	editorCursor       *tea.Cursor
	editorY            int
	editorHeight       int
	inputStatus        string
	receipts           []string
	receiptPoll        *paneReceiptPoll
	menu               string
	menuIndex          int
	menuOffset         int
	menuInset          int
	menuItems          []paneMenuItem
	menuRect           paneRect
	headerActions      []paneHeaderAction
	hoveredHeader      string
	footerHideX        int
	footerHideWidth    int
	footerHideHovered  bool
	callID             string
	offset             int
	followTail         bool
	layout             subagentOutputOverlayLayout
	composition        centeredOverlayCache
	splitFrame         splitWorkspaceFrameCache
	geometry           subagentOutputOverlayGeometry
	pressedItem        string
	selecting          bool
	selectStart        textSelectionPoint
	selectEnd          textSelectionPoint
}

type subagentOutputOverlayLayout struct {
	termWidth     int
	termHeight    int
	themeKey      string
	useBorder     bool
	frameWidth    int
	frameHeight   int
	innerWidth    int
	contentRows   int
	startX        int
	startY        int
	borderInset   int
	contentInset  int
	blank         string
	paintedMargin string
	separator     string
	topBorder     string
	bottomBorder  string
	leftBorder    string
	rightBorder   string
}

func subagentOutputOverlayClickToken(callID string) string {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return ""
	}
	return subagentOutputOverlayTokenPrefix + callID
}

func (m *Model) openSubagentOutputOverlay(blockID, callID string) bool {
	if m == nil {
		return false
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return false
	}
	owner, ok := m.subagentOutputOwner(blockID, callID)
	if ok {
		view := m.ensureSubagentOutputView(callID)
		if view == nil {
			return false
		}
		// Opening a presentation surface must not advance child lifecycle. The
		// Spawn invocation is already Done once it returns a handle, while the
		// child Task represented by that handle may still be running. Only hydrate
		// the stable identity here; terminal Task observations arrive separately.
		view.observeOwnerIdentity(owner)
		return m.openSubagentOutputOverlayView(callID, view)
	}
	// SourceCallID links from later Turns may not share the Spawn block.
	// Open the retained workspace only when this call is already a known owner.
	return m.openSubagentOutputOverlayView(callID, m.subagentOutputViews[callID])
}

// openSubagentOutputOverlayView opens a retained child workspace without
// requiring its original Spawn row to still be visible in the transcript.
// The Spawn call ID remains the presentation owner and Task identity is not
// inferred from the public handle.
func (m *Model) openSubagentOutputOverlayView(callID string, view *subagentOutputView) bool {
	if m == nil || view == nil {
		return false
	}
	callID = strings.TrimSpace(callID)
	if callID == "" || m.subagentOutputViews[callID] != view {
		return false
	}
	view.touch(true)
	if m.subagentOutputOverlay != nil && m.subagentOutputOverlay.callID == callID {
		m.workspace.childFocused = true
		return true
	}
	if m.subagentOutputOverlay != nil {
		m.closeSubagentOutputOverlay()
	}
	view.lastViewed = time.Now()
	if view.document == nil {
		view.resetForReplacement()
	}
	m.retainChildDisplayViews(callID)
	m.clearInputOverlays()
	m.showPalette = false
	m.subagentOverlay = nil
	m.subagentRosterPressed = false
	newPane := view.pane == nil
	if newPane {
		view.pane = &subagentOutputOverlayState{callID: callID, followTail: true, selectStart: textSelectionPoint{line: -1, col: -1}, selectEnd: textSelectionPoint{line: -1, col: -1}}
	}
	m.subagentOutputOverlay = view.pane
	m.workspace.childFocused = true
	m.workspace.lastCallID = callID
	m.ensureSubagentEditor(view.pane)
	if newPane {
		m.restoreChildSessionDraft(view.pane)
	}
	m.resizeWorkspace()

	view.prepareVisibleRender()
	m.reconcileTaskStreamOwner(callID, view.taskHandle)
	return true
}

func (m *Model) openSubagentOutputOverlayForMessage(blockID, messageCallID string) bool {
	if m == nil || m.doc == nil {
		return false
	}
	target := agentMessageTargetFromBlock(m.doc.Find(strings.TrimSpace(blockID)), messageCallID)
	callID := m.subagentOutputCallIDForHandle(target)
	if callID == "" {
		return false
	}
	for _, block := range m.doc.Blocks() {
		if _, ok := m.subagentOutputOwner(block.BlockID(), callID); ok {
			return m.openSubagentOutputOverlay(block.BlockID(), callID)
		}
	}
	return m.openSubagentOutputOverlay(blockID, callID)
}

func agentMessageTargetFromBlock(block Block, messageCallID string) string {
	messageCallID = strings.TrimSpace(messageCallID)
	var events []SubagentEvent
	switch typed := block.(type) {
	case *MainACPTurnBlock:
		events = typed.Events
	case *ParticipantTurnBlock:
		events = typed.Events
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Kind == SEToolCall && strings.TrimSpace(event.CallID) == messageCallID &&
			event.Name == surfaceToolSendMessage {
			return event.MessageTarget
		}
	}
	return ""
}

func (m *Model) subagentOutputCallIDForHandle(handle string) string {
	handle = normalizeTaskStreamHandle(handle)
	if m == nil || handle == "" || handle == "parent" {
		return ""
	}
	for taskID, candidate := range m.taskStreamHandlesByID {
		if normalizeTaskStreamHandle(candidate) == handle {
			if callID := strings.TrimSpace(m.taskStreamCallIDsByID[taskID]); callID != "" {
				return callID
			}
		}
	}
	for callID, view := range m.subagentOutputViews {
		if view != nil && normalizeTaskStreamHandle(view.taskHandle) == handle {
			return strings.TrimSpace(callID)
		}
	}
	return ""
}

func (m *Model) closeSubagentOutputOverlay() {
	if m == nil || m.subagentOutputOverlay == nil {
		return
	}
	callID := strings.TrimSpace(m.subagentOutputOverlay.callID)
	view := m.subagentOutputViews[callID]
	m.cancelSelectionAutoScroll()
	m.clearSubagentOutputSelection()
	m.clearPaneChromeMouse()
	m.subagentOutputOverlay.menu = ""
	m.subagentOutputOverlay = nil
	m.workspace.childFocused = false
	m.workspace.dragging = false
	m.cancelPaneResize()
	m.resizeWorkspace()
	if view != nil {
		m.reconcileTaskStreamOwner(callID, view.taskHandle)
		// The live activity fence belongs to the visible observation lifetime.
		// A later cold reopen must be allowed to hydrate the directory's current
		// terminal activity even when no new live frame was observed locally.
		view.liveActivityID = ""
	}
}

func (m *Model) renderSubagentOutputOverlay() string {
	if m == nil || m.subagentOutputOverlay == nil {
		return ""
	}
	state := m.subagentOutputOverlay
	view := m.subagentOutputViews[state.callID]
	layout := m.subagentOutputLayout(state)
	surface := m.subagentPaneSurface()
	rows := m.subagentOutputRows(view, layout.innerWidth, layout.contentRows)
	fixedRows := subagentOutputFixedRows(view, rows, layout.innerWidth)
	if view != nil {
		// Row wrapping is independent of the pane surface. Painted ANSI rows are
		// reusable in both layouts only while their background remains the same.
		surfaceKey := themeColorCacheKey(surface.GetBackground())
		if view.renderCache.paintedSurfaceKey != surfaceKey {
			clear(view.renderCache.paintedRows)
			view.renderCache.paintedSurfaceKey = surfaceKey
		}
	}
	maxOffset := maxInt(0, len(rows)-layout.contentRows)
	if state.followTail {
		state.offset = maxOffset
	} else {
		state.offset = clampInt(state.offset, 0, maxOffset)
	}
	end := minInt(len(rows), state.offset+layout.contentRows)
	visible := rows[state.offset:end]
	visibleFixedRows := m.renderSubagentOutputSelection(state, rows, fixedRows, end, layout.innerWidth)

	rowTokens := reuseSubagentOutputGeometryRows(state.geometry.rowTokens, layout.contentRows)
	var frame strings.Builder
	frame.Grow(layout.frameWidth * layout.frameHeight)
	appendSubagentOutputFrameLine(&frame, layout.topBorder)
	appendSubagentOutputContentLine(
		&frame,
		layout,
		renderSubagentOutputContentLine(
			surface,
			layout.innerWidth,
			normalizeFullscreenFrameLine(m.renderPaneTitle(view, layout.innerWidth), layout.innerWidth),
		),
	)
	appendSubagentOutputContentLine(&frame, layout, layout.separator)
	for index := 0; index < layout.contentRows; index++ {
		content := layout.blank
		if index < len(visible) {
			row := visible[index]
			rowTokens[index] = row.ClickToken
			if index < len(visibleFixedRows) {
				content = m.paintSubagentOutputRow(
					view,
					state.offset+index,
					visibleFixedRows[index],
					layout.innerWidth,
					surface,
					!state.selecting,
				)
			}
		}
		appendSubagentOutputContentLine(&frame, layout, content)
	}
	appendSubagentOutputContentLine(&frame, layout, layout.blank)
	appendSubagentOutputContentLine(&frame, layout, renderSubagentOutputContentLine(surface, layout.innerWidth, m.renderPaneHint(view, state, layout.innerWidth)))
	appendSubagentOutputContentLine(&frame, layout, layout.blank)
	state.editorY = layout.startY + layout.borderInset + 2 + layout.contentRows + 3
	editorLines := strings.Split(m.renderPaneEditor(state, layout.innerWidth), "\n")
	state.editorHeight = len(editorLines)
	for _, line := range editorLines {
		appendSubagentOutputContentLine(&frame, layout, tuikit.PaintLineBackground(normalizeFullscreenFrameLine(line, layout.innerWidth), layout.innerWidth, surface.GetBackground()))
	}
	appendSubagentOutputContentLine(&frame, layout, renderSubagentOutputContentLine(surface, layout.innerWidth, m.renderPaneFooter(state, layout.innerWidth)))

	appendSubagentOutputFrameLine(&frame, layout.bottomBorder)
	state.geometry = subagentOutputOverlayGeometry{
		x:            layout.startX,
		y:            layout.startY,
		width:        layout.frameWidth,
		height:       layout.frameHeight,
		headerY:      layout.startY + layout.borderInset,
		footerY:      state.editorY + state.editorHeight,
		contentX:     layout.startX + layout.contentInset,
		contentY:     layout.startY + layout.borderInset + 2,
		contentWidth: layout.innerWidth,
		rowTokens:    rowTokens,
		totalRows:    len(rows),
	}
	return frame.String()
}

func (m *Model) renderSubagentOutputSelection(
	state *subagentOutputOverlayState,
	rows []RenderedRow,
	fixedRows []string,
	end int,
	width int,
) []string {
	if state == nil || state.offset < 0 || state.offset >= end || end > len(rows) || end > len(fixedRows) {
		return nil
	}
	visibleFixedRows := fixedRows[state.offset:end]
	if !state.selecting {
		return visibleFixedRows
	}
	start, finish, ok := normalizedSelectionRange(state.selectStart, state.selectEnd, len(rows))
	if !ok || finish.line < state.offset || start.line >= end {
		return visibleFixedRows
	}

	styled := append([]string(nil), visibleFixedRows...)
	plain := make([]string, len(styled))
	indents := make([]int, len(styled))
	for index := range plain {
		row := rows[state.offset+index]
		plain[index] = row.Plain
		indents[index] = row.selectionIndent
	}
	localStart := textSelectionPoint{line: maxInt(start.line, state.offset) - state.offset, col: start.col}
	localFinish := textSelectionPoint{line: minInt(finish.line, end-1) - state.offset, col: finish.col}
	if start.line < state.offset {
		localStart.col = 0
	}
	if finish.line >= end {
		localFinish.col = displayColumns(plain[len(plain)-1])
	}
	styled = renderSelectionOnStyledLinesWithIndents(
		styled,
		plain,
		indents,
		localStart,
		localFinish,
		m.theme.InputSelectionStyle(),
	)
	for index := range styled {
		styled[index] = normalizeFullscreenFrameLine(styled[index], width)
		globalLine := state.offset + index
		if globalLine >= start.line && globalLine <= finish.line {
			styled[index] = protectWideCellRepaintLine(styled[index], width)
		}
	}
	return styled
}

func (m *Model) subagentOutputLayout(state *subagentOutputOverlayState) subagentOutputOverlayLayout {
	if m == nil || state == nil {
		return subagentOutputOverlayLayout{}
	}
	themeKey := m.cachedThemeRenderKey()
	workspace := m.workspaceLayout()
	rect := workspace.child
	frameWidth, targetHeight := rect.width, rect.height
	useBorder := !workspace.split && m.overlayUsesBorder() && frameWidth >= 8 && targetHeight >= 8
	borderInset, contentInset, borderHeight := 0, 0, 0
	if workspace.split {
		contentInset = 1
	}
	if useBorder {
		borderInset, contentInset, borderHeight = 1, 2, 2
	}
	innerWidth := maxInt(1, frameWidth-contentInset*2)
	editor := m.paneEditorLayout(state, innerWidth)
	editorHeight := editor.rowEnd - editor.rowOffset + m.composerChrome().verticalRows()
	contentRows := maxInt(1, targetHeight-borderHeight-6-editorHeight)
	frameHeight := contentRows + 6 + editorHeight + borderHeight
	if layout := state.layout; layout.termWidth == m.width && layout.termHeight == m.height && layout.themeKey == themeKey && layout.frameWidth == frameWidth && layout.frameHeight == frameHeight && layout.startX == rect.x && layout.startY == rect.y && layout.contentRows == contentRows && layout.useBorder == useBorder {
		return layout
	}
	surface := m.subagentPaneSurface()
	blank := strings.Repeat(" ", innerWidth)
	separator := normalizeFullscreenFrameLine(m.theme.SeparatorStyle().Render(strings.Repeat("─", innerWidth)), innerWidth)
	layout := subagentOutputOverlayLayout{
		termWidth:     m.width,
		termHeight:    m.height,
		themeKey:      themeKey,
		useBorder:     useBorder,
		frameWidth:    frameWidth,
		frameHeight:   frameHeight,
		innerWidth:    innerWidth,
		contentRows:   contentRows,
		startX:        rect.x,
		startY:        rect.y,
		borderInset:   borderInset,
		contentInset:  contentInset,
		blank:         renderSubagentOutputContentLine(surface, innerWidth, blank),
		paintedMargin: tuikit.PaintLineBackground(surface.Render(" "), 1, surface.GetBackground()),
		separator:     renderSubagentOutputContentLine(surface, innerWidth, separator),
	}
	if useBorder {
		borderStyle := m.theme.Tokens().OverlayBorder.Background(surface.GetBackground())
		layout.topBorder = tuikit.PaintLineBackground(
			borderStyle.Render("╭"+strings.Repeat("─", frameWidth-2)+"╮"),
			frameWidth,
			surface.GetBackground(),
		)
		layout.bottomBorder = tuikit.PaintLineBackground(
			borderStyle.Render("╰"+strings.Repeat("─", frameWidth-2)+"╯"),
			frameWidth,
			surface.GetBackground(),
		)
		layout.leftBorder = tuikit.PaintLineBackground(
			borderStyle.Render("│"),
			1,
			surface.GetBackground(),
		)
		layout.rightBorder = layout.leftBorder
	}
	state.layout = layout
	return layout
}

func appendSubagentOutputFrameLine(frame *strings.Builder, line string) {
	if frame == nil || line == "" {
		return
	}
	if frame.Len() > 0 {
		frame.WriteByte('\n')
	}
	frame.WriteString(line)
}

func appendSubagentOutputContentLine(
	frame *strings.Builder,
	layout subagentOutputOverlayLayout,
	content string,
) {
	if frame == nil {
		return
	}
	if frame.Len() > 0 {
		frame.WriteByte('\n')
	}
	var line strings.Builder
	line.Grow(layout.frameWidth)
	line.WriteString(layout.leftBorder)
	for range layout.contentInset - layout.borderInset {
		line.WriteString(layout.paintedMargin)
	}
	line.WriteString(content)
	for range layout.contentInset - layout.borderInset {
		line.WriteString(layout.paintedMargin)
	}
	line.WriteString(layout.rightBorder)
	frame.WriteString(line.String())
}

func renderSubagentOutputContentLine(surface lipgloss.Style, width int, line string) string {
	content := surface.Width(width).Render(strings.TrimRight(line, " "))
	return tuikit.PaintLineBackground(content, width, surface.GetBackground())
}

func (m *Model) paintSubagentOutputRow(
	view *subagentOutputView,
	index int,
	line string,
	width int,
	surface lipgloss.Style,
	cacheable bool,
) string {
	if cacheable && view != nil && index >= 0 && index < len(view.renderCache.paintedRows) &&
		index < len(view.renderCache.fixedRows) && view.renderCache.fixedRows[index] == line {
		if painted := view.renderCache.paintedRows[index]; painted != "" {
			return painted
		}
	}
	painted := renderSubagentOutputContentLine(surface, width, line)
	if cacheable && view != nil && index >= 0 && index < len(view.renderCache.paintedRows) &&
		index < len(view.renderCache.fixedRows) && view.renderCache.fixedRows[index] == line {
		view.renderCache.paintedRows[index] = painted
	}
	return painted
}

func reuseSubagentOutputGeometryRows(rows []string, size int) []string {
	if cap(rows) < size {
		return make([]string, size)
	}
	rows = rows[:size]
	clear(rows)
	return rows
}

func subagentOutputFixedRows(view *subagentOutputView, rows []RenderedRow, width int) []string {
	if view == nil {
		return fixedSubagentOutputRows(rows, width)
	}
	if len(view.renderCache.fixedRows) == len(rows) {
		return view.renderCache.fixedRows
	}
	view.renderCache.fixedRows = fixedSubagentOutputRows(rows, width)
	view.renderCache.paintedRows = make([]string, len(rows))
	return view.renderCache.fixedRows
}

func fixedSubagentOutputRows(rows []RenderedRow, width int) []string {
	fixed := make([]string, len(rows))
	for index, row := range rows {
		fixed[index] = normalizeFullscreenFrameLine(row.Styled, width)
	}
	return fixed
}

func (m *Model) subagentOutputRows(view *subagentOutputView, width, height int) []RenderedRow {
	if rows, ok := m.cachedSubagentOutputRows(view, width, height); ok {
		return rows
	}
	var rows []RenderedRow
	var fixedRows []string
	var entries []subagentOutputRenderEntry
	ctx := m.blockRenderContext(width)
	ctx.Width = width
	ctx.Height = height
	ctx.TermWidth = m.width
	if view != nil && subagentOutputViewHasTranscript(view) && view.document != nil && view.document.Len() > 0 {
		var previous []subagentOutputRenderEntry
		if cache := view.renderCache; cache.width == width && cache.height == height &&
			cache.termWidth == m.width && cache.themeKey == ctx.renderThemeKey() &&
			cache.workspace == strings.TrimSpace(ctx.Workspace) {
			previous = cache.entries
		}
		rows, fixedRows, entries = m.renderSubagentOutputDocument(view, ctx, previous)
	}
	if len(rows) == 0 {
		label := "Waiting for participant output…"
		switch {
		case view == nil:
			label = "Participant transcript is unavailable."
		case m.subagentOutputCurrentStatus(view) != subagentOutputRunning && m.subagentOutputHistoryPending(view):
			label = "Loading participant history…"
		case m.subagentOutputCurrentStatus(view) != subagentOutputRunning:
			label = ""
		}
		rows = []RenderedRow{StyledPlainRow(
			"subagent-output",
			label,
			m.theme.HelpHintTextStyle().Render(label),
		)}
	}
	if view != nil {
		previous := view.renderCache
		if len(fixedRows) != len(rows) {
			fixedRows = fixedSubagentOutputRows(rows, width)
		}
		paintedRows := make([]string, len(fixedRows))
		if previous.width == width && previous.termWidth == m.width &&
			previous.themeKey == ctx.renderThemeKey() && previous.workspace == strings.TrimSpace(ctx.Workspace) {
			for index := 0; index < len(fixedRows) && index < len(previous.fixedRows) && index < len(previous.paintedRows); index++ {
				if fixedRows[index] != previous.fixedRows[index] {
					break
				}
				paintedRows[index] = previous.paintedRows[index]
			}
		}
		view.renderCache = subagentOutputRenderCache{
			revision:          view.revision,
			entries:           entries,
			width:             width,
			height:            height,
			termWidth:         m.width,
			themeKey:          ctx.renderThemeKey(),
			workspace:         strings.TrimSpace(ctx.Workspace),
			rows:              rows,
			fixedRows:         fixedRows,
			paintedRows:       paintedRows,
			paintedSurfaceKey: previous.paintedSurfaceKey,
			renders:           previous.renders + 1,
		}
		view.renderReady = false
	}
	return rows
}

func (m *Model) subagentOutputHistoryPending(view *subagentOutputView) bool {
	if m == nil || view == nil || view.historyResolved {
		return false
	}
	callID := strings.TrimSpace(view.callID)
	if callID == "" {
		return false
	}
	if taskID := strings.TrimSpace(m.taskStreamIDsByCallID[callID]); taskID != "" {
		return m.taskStreamWanted[taskID] && (m.taskStreamTokens[taskID] != 0 || m.taskStreamSubscriptions[taskID] != nil)
	}
	return m.taskStreamResolveTokens[callID] != 0
}

func subagentOutputViewHasTranscript(view *subagentOutputView) bool {
	if view == nil || view.document == nil {
		return false
	}
	for _, block := range view.document.Blocks() {
		if typed, ok := block.(*ParticipantTurnBlock); ok && len(typed.Events) > 0 {
			return true
		}
	}
	return false
}

func (m *Model) subagentOutputCurrentStatus(view *subagentOutputView) subagentOutputStatus {
	if view == nil {
		return subagentOutputRunning
	}
	status, _, _ := m.subagentRosterViewState(view.callID, view)
	return status
}

func (m *Model) cachedSubagentOutputRows(view *subagentOutputView, width, height int) ([]RenderedRow, bool) {
	if m == nil || view == nil || len(view.renderCache.rows) == 0 {
		return nil, false
	}
	ctx := m.blockRenderContext(width)
	cache := &view.renderCache
	if cache.width != width ||
		cache.height != height ||
		cache.termWidth != m.width ||
		cache.themeKey != ctx.renderThemeKey() ||
		cache.workspace != strings.TrimSpace(ctx.Workspace) {
		return nil, false
	}
	if cache.revision == view.revision || !view.renderReady {
		return cache.rows, true
	}
	return nil, false
}
