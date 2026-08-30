package tuiapp

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

const (
	welcomePanelBaseWidth    = 72
	welcomePanelMaxWidth     = 112
	welcomeStandardMinWidth  = 50
	welcomeStandardMinHeight = 11
	welcomeCompactMinWidth   = 33
	welcomeCompactMinHeight  = 11
	welcomeDefaultNotice     = "type / for commands"
	welcomeActionMarker      = "› "
	welcomeActionCommandGap  = 6
	welcomeMaxContentInset   = 16
)

const (
	welcomeActionTokenConnect = "welcome:action:connect"
	welcomeActionTokenModel   = "welcome:action:model"
	welcomeActionTokenResume  = "welcome:action:resume"
	welcomeActionTokenQuit    = "welcome:action:quit"
)

var welcomeRelayLogoASCII = []string{
	`        ▄█████████`,
	`████▄▄▄███▀▀▀▀▀▀▀▀`,
	`▀▀▀▀████▀`,
	`▄▄▄▄▄█████████████`,
	`▀▀▀▀▀█████████████`,
	`   ▄████▄`,
	`█████▀▀███▄▄▄▄▄▄▄▄`,
	`        ▀█████████`,
}

var welcomeRelayCompactASCII = []string{
	`▄▄ ▄█▀▀▀`,
	`▄███▄▄▄▄`,
	`▀███▀▀▀▀`,
	`▀▀ ▀█▄▄▄`,
}

type WelcomeBlock struct {
	id      string
	Version string
	Notice  string
}

func NewWelcomeBlock(version string) *WelcomeBlock {
	return newWelcomeBlock(version, "")
}

func newWelcomeBlock(version string, notice string) *WelcomeBlock {
	return &WelcomeBlock{
		id:      nextBlockID(),
		Version: version,
		Notice:  normalizeWelcomeNotice(notice),
	}
}

func normalizeWelcomeNotice(notice string) string {
	notice = strings.TrimSpace(notice)
	if notice == "" {
		return welcomeDefaultNotice
	}
	return notice
}

func welcomeVersionLabel(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "v0.0.0"
	}
	if !strings.HasPrefix(version, "v") {
		return "v" + version
	}
	return version
}

func (b *WelcomeBlock) BlockID() string { return b.id }
func (b *WelcomeBlock) Kind() BlockKind { return BlockWelcome }

func (b *WelcomeBlock) Render(ctx BlockRenderContext) []RenderedRow {
	panelRows := buildWelcomePanel(ctx, welcomeVersionLabel(b.Version), b.Notice)
	if len(panelRows) == 0 {
		return nil
	}

	panelWidth := displayColumns(panelRows[0].plain)
	leftPad := maxInt(0, (ctx.Width-panelWidth)/2)
	rightPad := maxInt(0, ctx.Width-panelWidth-leftPad)
	left := strings.Repeat(" ", leftPad)
	right := strings.Repeat(" ", rightPad)

	topPadding := 0
	if freeHeight := ctx.Height - len(panelRows); freeHeight > 0 {
		topPadding = freeHeight / 3
	}

	rows := make([]RenderedRow, 0, topPadding+len(panelRows))
	for range topPadding {
		rows = append(rows, StyledRow(b.id, ""))
	}
	for _, row := range panelRows {
		clickStartCol := 0
		clickEndCol := 0
		if row.token != "" {
			clickStartCol = leftPad
			clickEndCol = leftPad + panelWidth
		}
		rows = append(rows, StyledPlainBoundedClickableRow(
			b.id,
			left+row.plain+right,
			left+row.styled+right,
			row.token,
			clickStartCol,
			clickEndCol,
		))
	}
	return rows
}

type welcomePanelRow struct {
	plain  string
	styled string
	token  string
}

type welcomePanelStyles struct {
	surface      lipgloss.Style
	border       lipgloss.Style
	logo         lipgloss.Style
	brand        lipgloss.Style
	action       lipgloss.Style
	actionMarker lipgloss.Style
	meta         lipgloss.Style
	notice       lipgloss.Style
}

func newWelcomePanelStyles(tokens tuikit.Tokens) welcomePanelStyles {
	styles := welcomePanelStyles{
		surface:      tokens.Surface0,
		border:       tokens.BorderSubtle,
		logo:         tokens.Focus,
		brand:        tokens.TextPrimary.Bold(true),
		action:       tokens.TextPrimary.Bold(true),
		actionMarker: tokens.Accent.Bold(true),
		meta:         tokens.ChromeMeta,
		notice:       tokens.TextMuted,
	}
	if background := tokens.Surface0.GetBackground(); background != nil {
		styles.border = styles.border.Background(background)
		styles.logo = styles.logo.Background(background)
		styles.brand = styles.brand.Background(background)
		styles.action = styles.action.Background(background)
		styles.actionMarker = styles.actionMarker.Background(background)
		styles.meta = styles.meta.Background(background)
		styles.notice = styles.notice.Background(background)
	}
	return styles
}

func buildWelcomePanel(ctx BlockRenderContext, version string, notice string) []welcomePanelRow {
	styles := newWelcomePanelStyles(ctx.Theme.Tokens())
	notice = normalizeWelcomeNotice(notice)
	switch {
	case ctx.Width >= welcomeStandardMinWidth && ctx.Height >= welcomeStandardMinHeight:
		return buildStandardWelcomePanel(ctx.Width, ctx.Height, version, notice, styles)
	case ctx.Width >= welcomeCompactMinWidth && ctx.Height >= welcomeCompactMinHeight:
		return buildCompactWelcomePanel(ctx.Width, ctx.Height, version, notice, styles)
	default:
		return buildFallbackWelcomePanel(ctx.Width, ctx.Height, notice, styles)
	}
}

func buildStandardWelcomePanel(
	width int,
	height int,
	version string,
	notice string,
	styles welcomePanelStyles,
) []welcomePanelRow {
	panelWidth := welcomeStandardPanelWidth(width)
	innerWidth := maxInt(0, panelWidth-2)
	gap := 2
	switch {
	case panelWidth >= 96:
		gap = 8
	case panelWidth >= welcomePanelBaseWidth:
		gap = 4
	}
	const (
		logoWidth                = 18
		minimumHorizontalPadding = 2
	)
	maxRightWidth := maxInt(0, innerWidth-logoWidth-gap-minimumHorizontalPadding*2)
	rightWidth := minInt(welcomePreferredDetailsWidth(notice), maxRightWidth)
	remainingWidth := maxInt(0, innerWidth-logoWidth-gap-rightWidth)
	leftPadding := minInt(remainingWidth/2, welcomeMaxContentInset)
	rightPadding := remainingWidth - leftPadding
	rightRows := buildWelcomeDetails(rightWidth, version, notice, styles)
	contentRows := welcomeColumnLayoutRows(
		welcomeRelayLogoASCII,
		logoWidth,
		rightRows,
		rightWidth,
		leftPadding,
		rightPadding,
		gap,
		styles,
	)
	return frameWelcomePanel(panelWidth, height, contentRows, styles)
}

func buildCompactWelcomePanel(
	width int,
	height int,
	version string,
	notice string,
	styles welcomePanelStyles,
) []welcomePanelRow {
	panelWidth := minInt(40, maxInt(1, width))
	innerWidth := maxInt(0, panelWidth-2)
	const (
		leftPadding  = 2
		rightPadding = 1
		logoWidth    = 8
		gap          = 2
	)
	rightWidth := maxInt(0, innerWidth-leftPadding-rightPadding-logoWidth-gap)
	rightRows := buildWelcomeDetails(rightWidth, version, notice, styles)
	contentRows := welcomeColumnLayoutRows(
		welcomeRelayCompactASCII,
		logoWidth,
		rightRows,
		rightWidth,
		leftPadding,
		rightPadding,
		gap,
		styles,
	)
	return frameWelcomePanel(panelWidth, height, contentRows, styles)
}

func buildFallbackWelcomePanel(width int, height int, notice string, styles welcomePanelStyles) []welcomePanelRow {
	width = maxInt(1, width)
	if height < len(welcomeActions)+2 {
		return buildWelcomeActionRows(width, styles)
	}
	rows := welcomeNoticeCells(notice, width, 1, styles)
	divider := strings.Repeat("─", width)
	rows = append(rows, welcomePanelRow{plain: divider, styled: styles.border.Render(divider)})
	return append(rows, buildWelcomeActionRows(width, styles)...)
}

func buildWelcomeDetails(width int, version string, notice string, styles welcomePanelStyles) []welcomePanelRow {
	width = maxInt(0, width)
	showCommands := width >= welcomeActionLabelsWidth()+1+welcomeCommandColumnWidth()
	rows := []welcomePanelRow{
		welcomeBrandVersionCell("CAELIS", version, width, styles),
		welcomeTextCell("", width, styles.surface),
	}
	rows = append(rows, welcomeNoticeCells(notice, width, 2, styles)...)
	rows = append(rows, welcomeTextCell("", width, styles.surface))
	for _, action := range welcomeActions {
		rows = append(rows, welcomeActionCell(action, width, showCommands, styles))
	}
	return rows
}

func welcomePanelSupportsNotice(ctx BlockRenderContext) bool {
	if ctx.Width >= welcomeCompactMinWidth && ctx.Height >= welcomeCompactMinHeight {
		return true
	}
	return ctx.Height >= len(welcomeActions)+2
}

func welcomeNoticeCells(notice string, width int, maxRows int, styles welcomePanelStyles) []welcomePanelRow {
	if width <= 0 {
		return nil
	}
	lines := graphemeWordWrap(strings.TrimSpace(notice), width)
	if len(lines) > maxRows {
		lines = lines[:maxRows]
	}
	rows := make([]welcomePanelRow, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, welcomeTextCell(line, width, styles.notice))
	}
	return rows
}

func welcomeColumnLayoutRows(
	leftLines []string,
	leftWidth int,
	rightRows []welcomePanelRow,
	rightWidth int,
	leftPadding int,
	rightPadding int,
	gap int,
	styles welcomePanelStyles,
) []welcomePanelRow {
	rowCount := maxInt(len(leftLines), len(rightRows))
	rows := make([]welcomePanelRow, 0, rowCount)
	leftInset := strings.Repeat(" ", maxInt(0, leftPadding))
	gapText := strings.Repeat(" ", maxInt(0, gap))
	rightInset := strings.Repeat(" ", maxInt(0, rightPadding))
	for i := range rowCount {
		leftLine := ""
		if i < len(leftLines) {
			leftLine = leftLines[i]
		}
		leftPlain, leftStyled := welcomeCell(leftLine, leftWidth, styles.logo.Render)
		right := welcomeTextCell("", rightWidth, styles.surface)
		if i < len(rightRows) {
			right = rightRows[i]
		}
		row := welcomePanelRow{
			plain: leftInset + leftPlain + gapText + right.plain + rightInset,
			styled: styles.surface.Render(leftInset) + leftStyled + styles.surface.Render(gapText) +
				right.styled + styles.surface.Render(rightInset),
			token: right.token,
		}
		rows = append(rows, row)
	}
	return rows
}

func welcomeStandardPanelWidth(width int) int {
	width = maxInt(1, width)
	target := maxInt(welcomePanelBaseWidth, width*3/4)
	return minInt(welcomePanelMaxWidth, minInt(width, target))
}

func welcomePanelVerticalPadding(height int, contentRows int) int {
	padding := 0
	switch {
	case height >= 36:
		padding = 4
	case height >= 19:
		padding = 2
	case height >= 12:
		padding = 1
	}
	maxPadding := maxInt(0, (height-contentRows-2)/2)
	return minInt(padding, maxPadding)
}

func frameWelcomePanel(panelWidth int, height int, contentRows []welcomePanelRow, styles welcomePanelStyles) []welcomePanelRow {
	innerWidth := maxInt(0, panelWidth-2)
	verticalPadding := welcomePanelVerticalPadding(height, len(contentRows))
	rows := make([]welcomePanelRow, 0, 2+verticalPadding*2+len(contentRows))
	rows = append(rows, welcomeBorderRow(panelWidth, true, styles))
	for range verticalPadding {
		rows = append(rows, welcomeFramedRow(welcomeTextCell("", innerWidth, styles.surface), styles))
	}
	for _, row := range contentRows {
		rows = append(rows, welcomeFramedRow(row, styles))
	}
	for range verticalPadding {
		rows = append(rows, welcomeFramedRow(welcomeTextCell("", innerWidth, styles.surface), styles))
	}
	return append(rows, welcomeBorderRow(panelWidth, false, styles))
}

func welcomeFramedRow(row welcomePanelRow, styles welcomePanelStyles) welcomePanelRow {
	return welcomePanelRow{
		plain:  "│" + row.plain + "│",
		styled: styles.border.Render("│") + row.styled + styles.border.Render("│"),
		token:  row.token,
	}
}

func welcomeBorderRow(width int, top bool, styles welcomePanelStyles) welcomePanelRow {
	if width <= 1 {
		text := fitWelcomeText("─", width)
		return welcomePanelRow{plain: text, styled: styles.border.Render(text)}
	}
	left, right := "┌", "┐"
	if !top {
		left, right = "└", "┘"
	}
	plain := left + strings.Repeat("─", maxInt(0, width-2)) + right
	return welcomePanelRow{plain: plain, styled: styles.border.Render(plain)}
}

func buildWelcomeActionRows(width int, styles welcomePanelStyles) []welcomePanelRow {
	width = maxInt(1, width)
	commandWidth := welcomeCommandColumnWidth()
	showCommands := width >= welcomeActionLabelsWidth()+1+commandWidth
	rows := make([]welcomePanelRow, 0, len(welcomeActions))
	for _, action := range welcomeActions {
		rows = append(rows, welcomeActionCell(action, width, showCommands, styles))
	}
	return rows
}

func welcomeActionLabelsWidth() int {
	width := 0
	for _, action := range welcomeActions {
		width = maxInt(width, displayColumns(welcomeActionMarker)+displayColumns(action.label))
	}
	return width
}

func welcomeCommandColumnWidth() int {
	width := 0
	for _, action := range welcomeActions {
		width = maxInt(width, displayColumns(action.command))
	}
	return width
}

func welcomePreferredDetailsWidth(notice string) int {
	actionWidth := welcomeActionLabelsWidth() + welcomeActionCommandGap + welcomeCommandColumnWidth()
	return maxInt(actionWidth, displayColumns(strings.TrimSpace(notice)))
}

func welcomeBrandVersionCell(brand string, version string, width int, styles welcomePanelStyles) welcomePanelRow {
	width = maxInt(0, width)
	brand = fitWelcomeText(strings.TrimSpace(brand), width)
	brandWidth := displayColumns(brand)
	versionGap := ""
	if brandWidth < width {
		versionGap = strings.Repeat(" ", minInt(2, width-brandWidth))
	}
	versionWidth := maxInt(0, width-brandWidth-displayColumns(versionGap))
	version = fitWelcomeText(strings.TrimSpace(version), versionWidth)
	rightSpace := strings.Repeat(" ", maxInt(0, width-brandWidth-displayColumns(versionGap)-displayColumns(version)))
	return welcomePanelRow{
		plain: brand + versionGap + version + rightSpace,
		styled: styles.brand.Render(brand) + styles.surface.Render(versionGap) +
			styles.meta.Render(version) + styles.surface.Render(rightSpace),
	}
}

func welcomeTextCell(text string, width int, style lipgloss.Style) welcomePanelRow {
	plain, styled := welcomeCell(text, width, style.Render)
	return welcomePanelRow{plain: plain, styled: styled}
}

func welcomeActionCell(action welcomeAction, width int, showCommand bool, styles welcomePanelStyles) welcomePanelRow {
	width = maxInt(1, width)
	commandWidth := 0
	if showCommand {
		commandWidth = welcomeCommandColumnWidth()
	}
	labelWidth := width
	if showCommand {
		labelWidth = minInt(
			maxInt(0, width-commandWidth),
			welcomeActionLabelsWidth()+welcomeActionCommandGap,
		)
	}
	label := welcomeActionMarker + action.label
	if labelWidth < displayColumns(welcomeActionMarker) {
		label = action.label
	}
	label = fitWelcomeText(label, labelWidth)
	labelSpace := strings.Repeat(" ", maxInt(0, labelWidth-displayColumns(label)))

	labelStyled := styles.action.Render(label)
	if strings.HasPrefix(label, welcomeActionMarker) {
		labelStyled = styles.actionMarker.Render(welcomeActionMarker) +
			styles.action.Render(strings.TrimPrefix(label, welcomeActionMarker))
	}
	plain := label + labelSpace
	styled := labelStyled + styles.surface.Render(labelSpace)
	if showCommand {
		command := fitWelcomeText(action.command, commandWidth)
		commandSpace := strings.Repeat(" ", maxInt(0, commandWidth-displayColumns(command)))
		plain += command + commandSpace
		styled += styles.meta.Render(command) + styles.surface.Render(commandSpace)
	}
	trailingSpace := strings.Repeat(" ", maxInt(0, width-displayColumns(plain)))
	plain += trailingSpace
	styled += styles.surface.Render(trailingSpace)
	return welcomePanelRow{
		plain:  plain,
		styled: styled,
		token:  action.token,
	}
}

func welcomeCell(text string, width int, render func(...string) string) (string, string) {
	width = maxInt(0, width)
	text = fitWelcomeText(text, width)
	textWidth := displayColumns(text)
	rightSpace := strings.Repeat(" ", maxInt(0, width-textWidth))
	return text + rightSpace, render(text) + rightSpace
}

func fitWelcomeText(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if displayColumns(text) <= width {
		return text
	}
	return sliceByDisplayColumns(text, 0, width)
}
