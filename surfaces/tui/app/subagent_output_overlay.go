package tuiapp

import (
	"fmt"
	"strings"

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
	view.observeOwnerEvent(owner)
	if m.subagentOutputOverlay != nil {
		m.closeSubagentOutputOverlay()
	}
	m.clearInputOverlays()
	m.showPalette = false
	m.subagentOverlay = nil
	m.subagentOutputOverlay = &subagentOutputOverlayState{
		callID:     callID,
		followTail: true,
	}
	view.prepareVisibleRender()
	return true
}

func (m *Model) closeSubagentOutputOverlay() {
	if m == nil || m.subagentOutputOverlay == nil {
		return
	}
	m.subagentOutputOverlay = nil
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
			fixedIndex := state.offset + index
			if fixedIndex >= 0 && fixedIndex < len(fixedRows) {
				line = fixedRows[fixedIndex]
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
		contentY:     layout.startY + layout.borderInset + 2,
		contentWidth: layout.innerWidth,
		rowTokens:    rowTokens,
		totalRows:    len(rows),
	}
	return frame.String()
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
	if view != nil && view.block != nil && len(view.block.Events) > 0 {
		rows = view.block.Render(ctx)
	}
	if view != nil && strings.TrimSpace(view.finalResponse) != "" &&
		!subagentOutputBlockEndsWithAssistantText(view.block, view.finalResponse) {
		rows = appendSubagentOutputAssistantRows(
			rows,
			ctx,
			"subagent-output-"+view.callID,
			view.finalResponse,
			width,
		)
	}
	if view != nil && strings.TrimSpace(view.terminalFailure) != "" &&
		!subagentOutputBlockEndsWithText(view.block, view.terminalFailure) {
		rows = appendSubagentOutputFailureRows(
			rows,
			ctx,
			"subagent-output-"+view.callID,
			view.terminalFailure,
			width,
		)
	}
	if len(rows) == 0 {
		label := "Waiting for subagent transcript…"
		if view == nil {
			label = "Subagent transcript is unavailable."
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

func appendSubagentOutputAssistantRows(
	rows []RenderedRow,
	ctx BlockRenderContext,
	blockID string,
	text string,
	width int,
) []RenderedRow {
	if len(rows) > 0 {
		rows = append(rows, PlainRow(blockID, ""))
	}
	return append(rows, renderParticipantTurnNarrativeRowsWithBuffer(
		blockID,
		text,
		nil,
		tuikit.LineStyleAssistant,
		width,
		ctx,
		false,
	)...)
}

func appendSubagentOutputFailureRows(
	rows []RenderedRow,
	ctx BlockRenderContext,
	blockID string,
	text string,
	width int,
) []RenderedRow {
	if len(rows) > 0 {
		rows = append(rows, PlainRow(blockID, ""))
	}
	rows = append(rows, StyledPlainRow(blockID, "Error", ctx.Theme.ErrorStyle().Bold(true).Render("Error")))
	rendered := RenderTextWithContext(ctx, TextRenderRequest{
		Kind:           TextAssistant,
		Mode:           RenderFinal,
		MarkdownPolicy: MarkdownFull,
		Raw:            text,
		Width:          width,
		BlockID:        blockID,
		LineStyle:      tuikit.LineStyleError,
	}).Rows
	return append(rows, rendered...)
}

func subagentOutputBlockEndsWithAssistantText(block *ParticipantTurnBlock, text string) bool {
	if block == nil {
		return false
	}
	text = strings.TrimSpace(text)
	for index := len(block.Events) - 1; index >= 0; index-- {
		event := block.Events[index]
		if event.Kind == SESemanticBoundary {
			continue
		}
		return event.Kind == SEAssistant && strings.TrimSpace(event.Text) == text
	}
	return false
}

func subagentOutputBlockEndsWithText(block *ParticipantTurnBlock, text string) bool {
	if block == nil {
		return false
	}
	text = strings.TrimSpace(text)
	for index := len(block.Events) - 1; index >= 0; index-- {
		event := block.Events[index]
		switch event.Kind {
		case SESemanticBoundary:
			continue
		case SEAssistant, SEReasoning, SENotice:
			return strings.TrimSpace(event.Text) == text
		default:
			return false
		}
	}
	return false
}

func (m *Model) renderSubagentOutputTitle(view *subagentOutputView, width int) string {
	actor := ""
	status := subagentOutputRunning
	if view != nil {
		actor = firstNonEmpty(strings.TrimSpace(view.actor), strings.TrimSpace(view.taskHandle))
		if view.block != nil {
			status = subagentOutputStatusFromState(view.block.Status)
		}
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
