package tuiapp

import (
	"fmt"
	"strings"

	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
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
	closeX       int
	closeY       int
	contentX     int
	contentY     int
	contentWidth int
	rowTokens    []string
	totalRows    int
}

type subagentOutputOverlayState struct {
	callID      string
	offset      int
	followTail  bool
	layout      subagentOutputOverlayLayout
	geometry    subagentOutputOverlayGeometry
	pressedItem string
	selecting   bool
	selectStart textSelectionPoint
	selectEnd   textSelectionPoint
}

type subagentOutputOverlayLayout struct {
	termWidth    int
	termHeight   int
	themeKey     string
	useBorder    bool
	frameWidth   int
	frameHeight  int
	innerWidth   int
	contentRows  int
	startX       int
	startY       int
	borderInset  int
	contentInset int
	blank        string
	separator    string
	topBorder    string
	bottomBorder string
	leftBorder   string
	rightBorder  string
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
	owner, ok := m.subagentOutputOwner(blockID, callID)
	if !ok {
		return false
	}
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
	if m.subagentOutputOverlay != nil {
		m.closeSubagentOutputOverlay()
	}
	m.clearInputOverlays()
	m.showPalette = false
	m.subagentOverlay = nil
	m.subagentRosterOverlay = nil
	m.subagentRosterPressed = false
	m.subagentOutputOverlay = &subagentOutputOverlayState{
		callID:      callID,
		followTail:  true,
		selectStart: textSelectionPoint{line: -1, col: -1},
		selectEnd:   textSelectionPoint{line: -1, col: -1},
	}
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
	return false
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
			names.CanonicalOrSelf(toolSemanticName(event.Name, event.ToolKind)) == names.SendMessage {
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
	m.subagentOutputOverlay = nil
	if view != nil {
		m.reconcileTaskStreamOwner(callID, view.taskHandle)
	}
}

func (m *Model) renderSubagentOutputOverlay() string {
	if m == nil || m.subagentOutputOverlay == nil {
		return ""
	}
	state := m.subagentOutputOverlay
	view := m.subagentOutputViews[state.callID]
	layout := m.subagentOutputLayout(state)
	rows := m.subagentOutputRows(view, layout.innerWidth, layout.contentRows)
	fixedRows := subagentOutputFixedRows(view, rows, layout.innerWidth)
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
		normalizeFullscreenFrameLine(m.renderSubagentOutputTitle(view, layout.innerWidth), layout.innerWidth),
	)
	appendSubagentOutputContentLine(&frame, layout, layout.separator)
	for index := 0; index < layout.contentRows; index++ {
		line := layout.blank
		if index < len(visible) {
			row := visible[index]
			rowTokens[index] = row.ClickToken
			if index < len(visibleFixedRows) {
				line = visibleFixedRows[index]
			}
		}
		appendSubagentOutputContentLine(&frame, layout, line)
	}
	appendSubagentOutputContentLine(&frame, layout, layout.separator)
	appendSubagentOutputContentLine(
		&frame,
		layout,
		normalizeFullscreenFrameLine(
			m.renderSubagentOutputFooter(state.offset, end, len(rows), layout.innerWidth),
			layout.innerWidth,
		),
	)
	appendSubagentOutputFrameLine(&frame, layout.bottomBorder)
	state.geometry = subagentOutputOverlayGeometry{
		x:            layout.startX,
		y:            layout.startY,
		width:        layout.frameWidth,
		height:       layout.frameHeight,
		closeX:       layout.startX + layout.contentInset + maxInt(0, layout.innerWidth-1),
		closeY:       layout.startY + layout.borderInset,
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
	if layout := state.layout; layout.termWidth == m.width &&
		layout.termHeight == m.height &&
		layout.themeKey == themeKey &&
		layout.useBorder == m.overlayUsesBorder() {
		return layout
	}

	frameWidth := minInt(maxInt(64, m.fixedRowWidth()-6), maxInt(20, m.width-4))
	if m.width < tuikit.OverlayBorderMinWidth {
		frameWidth = maxInt(20, m.width)
	}
	useBorder := m.overlayUsesBorder()
	innerWidth := maxInt(16, frameWidth-m.overlayBorderChromeWidth())
	borderHeight := 0
	targetHeight := maxInt(6, m.height-4)
	if useBorder {
		borderHeight = 2
	} else {
		targetHeight = maxInt(6, m.height)
	}
	contentRows := maxInt(2, targetHeight-borderHeight-4)
	frameHeight := contentRows + 4 + borderHeight
	borderInset := 0
	contentInset := 0
	if useBorder {
		borderInset = 1
		contentInset = 2
	}
	layout := subagentOutputOverlayLayout{
		termWidth:    m.width,
		termHeight:   m.height,
		themeKey:     themeKey,
		useBorder:    useBorder,
		frameWidth:   frameWidth,
		frameHeight:  frameHeight,
		innerWidth:   innerWidth,
		contentRows:  contentRows,
		startX:       maxInt(0, (m.width-frameWidth)/2),
		startY:       maxInt(0, (m.height-frameHeight)/2),
		borderInset:  borderInset,
		contentInset: contentInset,
		blank:        strings.Repeat(" ", innerWidth),
		separator:    normalizeFullscreenFrameLine(m.theme.SeparatorStyle().Render(strings.Repeat("─", innerWidth)), innerWidth),
	}
	if useBorder {
		borderStyle := m.theme.Tokens().OverlayBorder
		layout.topBorder = borderStyle.Render("╭" + strings.Repeat("─", frameWidth-2) + "╮")
		layout.bottomBorder = borderStyle.Render("╰" + strings.Repeat("─", frameWidth-2) + "╯")
		layout.leftBorder = borderStyle.Render("│")
		layout.rightBorder = borderStyle.Render("│")
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

func appendSubagentOutputContentLine(frame *strings.Builder, layout subagentOutputOverlayLayout, line string) {
	if frame == nil {
		return
	}
	if frame.Len() > 0 {
		frame.WriteByte('\n')
	}
	if layout.useBorder {
		frame.WriteString(layout.leftBorder)
		frame.WriteByte(' ')
	}
	frame.WriteString(line)
	if layout.useBorder {
		frame.WriteByte(' ')
		frame.WriteString(layout.rightBorder)
	}
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
	ctx := m.blockRenderContext(width)
	ctx.Width = width
	ctx.Height = height
	ctx.TermWidth = m.width
	if view != nil && subagentOutputViewHasTranscript(view) && view.document != nil && view.document.Len() > 0 {
		rows = view.document.RenderAll(ctx)
	}
	if len(rows) == 0 {
		label := "Waiting for subagent output…"
		switch {
		case view == nil:
			label = "Subagent transcript is unavailable."
		case m.subagentOutputCurrentStatus(view) != subagentOutputRunning && m.subagentOutputHistoryPending(view):
			label = "Loading subagent history…"
		case m.subagentOutputCurrentStatus(view) != subagentOutputRunning:
			label = "No retained assistant messages for this subagent."
		}
		rows = []RenderedRow{StyledPlainRow(
			"subagent-output",
			label,
			m.theme.HelpHintTextStyle().Render(label),
		)}
	}
	if view != nil {
		view.renderCache = subagentOutputRenderCache{
			revision:  view.revision,
			width:     width,
			height:    height,
			termWidth: m.width,
			themeKey:  ctx.renderThemeKey(),
			workspace: strings.TrimSpace(ctx.Workspace),
			rows:      rows,
			fixedRows: fixedSubagentOutputRows(rows, width),
			renders:   view.renderCache.renders + 1,
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
		return m.taskStreamWanted[taskID] &&
			(m.taskStreamTokens[taskID] != 0 || m.taskStreamSubscriptions[taskID] != nil)
	}
	return m.taskStreamResolveTokens[callID] != 0
}

func subagentOutputViewHasTranscript(view *subagentOutputView) bool {
	if view == nil || view.document == nil {
		return false
	}
	for _, block := range view.document.Blocks() {
		switch typed := block.(type) {
		case *ParticipantTurnBlock:
			if len(typed.Events) > 0 {
				return true
			}
		case *UserNarrativeBlock:
			if strings.TrimSpace(typed.Raw) != "" {
				return true
			}
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

func (m *Model) renderSubagentOutputTitle(view *subagentOutputView, width int) string {
	actor := ""
	status := subagentOutputRunning
	if view != nil {
		actor = firstNonEmpty(strings.TrimSpace(view.actor), strings.TrimSpace(view.taskHandle))
		status = m.subagentOutputCurrentStatus(view)
	}
	title := "Subagent"
	if actor != "" {
		title += " · " + actor
	}
	closeText := "×"
	titleBudget := maxInt(8, width-displayColumns("•  "+closeText)-2)
	title = truncateTailDisplay(title, titleBudget)
	ctx := m.blockRenderContext(width)
	left := renderSubagentOutputStatusMark(ctx, status) + " " + m.theme.TitleStyle().Render(title)
	right := m.theme.HelpHintTextStyle().Render(closeText)
	gap := maxInt(1, width-displayColumns(left)-displayColumns(right))
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) renderSubagentOutputFooter(start, end, total, width int) string {
	position := "0 / 0"
	if total > 0 {
		position = fmt.Sprintf("%d–%d / %d", start+1, end, total)
	}
	help := "↑/↓ scroll  pgup/pgdn page  esc close"
	gap := maxInt(1, width-displayColumns(position)-displayColumns(help))
	if displayColumns(position)+displayColumns(help)+gap > width {
		return m.theme.HelpHintTextStyle().Render(truncateTailDisplay(help, width))
	}
	return m.theme.TranscriptMetaStyle().Render(position) +
		strings.Repeat(" ", gap) +
		m.theme.HelpHintTextStyle().Render(help)
}
