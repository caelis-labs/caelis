package tuiapp

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

const (
	welcomePanelBaseWidth    = 72
	welcomePanelMaxWidth     = 80
	welcomeStandardMinWidth  = 50
	welcomeStandardMinHeight = 11
	welcomeCompactMinWidth   = 33
	welcomeCompactMinHeight  = 11
	welcomeDefaultNotice     = "type / for commands"
	welcomeActionMarker      = "› "
	welcomeDetailsMinWidth   = 28
	welcomeLogoLeftShift     = 3
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
	id           string
	Version      string
	announcement welcomeAnnouncement
}

type welcomeAnnouncement struct {
	text     string
	emphasis string
}

func newWelcomeAnnouncement(text string) welcomeAnnouncement {
	text = strings.TrimSpace(text)
	if text == "" {
		text = welcomeDefaultNotice
	}
	return welcomeAnnouncement{text: text}
}

func newWelcomeAnnouncementWithEmphasis(text string, emphasis string) welcomeAnnouncement {
	announcement := newWelcomeAnnouncement(text)
	emphasis = strings.TrimSpace(emphasis)
	if emphasis != "" && strings.HasPrefix(announcement.text, emphasis) {
		announcement.emphasis = emphasis
	}
	return announcement
}

func (a welcomeAnnouncement) plainText() string { return a.text }

func NewWelcomeBlock(version string) *WelcomeBlock {
	return newWelcomeBlock(version, "")
}

func newWelcomeBlock(version string, notice string) *WelcomeBlock {
	return &WelcomeBlock{
		id:           nextBlockID(),
		Version:      version,
		announcement: newWelcomeAnnouncement(notice),
	}
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
	panelRows := buildWelcomePanel(ctx, welcomeVersionLabel(b.Version), b.announcement)
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
	surface        lipgloss.Style
	border         lipgloss.Style
	logo           lipgloss.Style
	brand          lipgloss.Style
	action         lipgloss.Style
	actionMarker   lipgloss.Style
	meta           lipgloss.Style
	notice         lipgloss.Style
	noticeEmphasis lipgloss.Style
}

func newWelcomePanelStyles(tokens tuikit.Tokens) welcomePanelStyles {
	styles := welcomePanelStyles{
		surface:        tokens.Surface0,
		border:         tokens.BorderSubtle,
		logo:           tokens.Focus,
		brand:          tokens.TextPrimary.Bold(true),
		action:         tokens.TextPrimary.Bold(true),
		actionMarker:   tokens.Accent.Bold(true),
		meta:           tokens.ChromeMeta,
		notice:         tokens.TextMuted,
		noticeEmphasis: tokens.Warning,
	}
	if background := tokens.Surface0.GetBackground(); background != nil {
		styles.border = styles.border.Background(background)
		styles.logo = styles.logo.Background(background)
		styles.brand = styles.brand.Background(background)
		styles.action = styles.action.Background(background)
		styles.actionMarker = styles.actionMarker.Background(background)
		styles.meta = styles.meta.Background(background)
		styles.notice = styles.notice.Background(background)
		styles.noticeEmphasis = styles.noticeEmphasis.Background(background)
	}
	return styles
}

func buildWelcomePanel(ctx BlockRenderContext, version string, announcement welcomeAnnouncement) []welcomePanelRow {
	styles := newWelcomePanelStyles(ctx.Theme.Tokens())
	if strings.TrimSpace(announcement.plainText()) == "" {
		announcement = newWelcomeAnnouncement("")
	}
	switch {
	case ctx.Width >= welcomeStandardMinWidth && ctx.Height >= welcomeStandardMinHeight:
		return buildStandardWelcomePanel(ctx.Width, ctx.Height, version, announcement, styles)
	case ctx.Width >= welcomeCompactMinWidth && ctx.Height >= welcomeCompactMinHeight:
		return buildCompactWelcomePanel(ctx.Width, ctx.Height, version, announcement, styles)
	default:
		return buildFallbackWelcomePanel(ctx.Width, ctx.Height, announcement, styles)
	}
}

func buildStandardWelcomePanel(
	width int,
	height int,
	version string,
	announcement welcomeAnnouncement,
	styles welcomePanelStyles,
) []welcomePanelRow {
	panelWidth := welcomeStandardPanelWidth(width)
	innerWidth := maxInt(0, panelWidth-2)
	baseGap := 2
	if panelWidth >= welcomePanelBaseWidth {
		baseGap = 4
	}
	const (
		logoWidth                = 18
		minimumHorizontalPadding = 2
	)
	maxRightWidth := maxInt(0, innerWidth-logoWidth-baseGap-minimumHorizontalPadding*2)
	rightWidth := minInt(welcomePreferredDetailsWidth(announcement), maxRightWidth)
	remainingWidth := maxInt(0, innerWidth-logoWidth-baseGap-rightWidth)
	centeredLeftPadding := remainingWidth / 2
	leftPadding := maxInt(minimumHorizontalPadding, centeredLeftPadding-welcomeLogoLeftShift)
	// Match the logo inset and details gap so the two-column layout has one
	// consistent unit of horizontal whitespace.
	gap := leftPadding
	rightPadding := maxInt(0, innerWidth-leftPadding-logoWidth-gap-rightWidth)
	rightRows := buildWelcomeDetails(rightWidth, version, announcement, styles)
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
	announcement welcomeAnnouncement,
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
	rightRows := buildWelcomeDetails(rightWidth, version, announcement, styles)
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

func buildFallbackWelcomePanel(width int, height int, announcement welcomeAnnouncement, styles welcomePanelStyles) []welcomePanelRow {
	width = maxInt(1, width)
	if height < len(welcomeActions)+2 {
		return buildWelcomeActionRows(width, styles)
	}
	rows := welcomeNoticeCells(announcement, width, 1, styles)
	divider := strings.Repeat("─", width)
	rows = append(rows, welcomePanelRow{plain: divider, styled: styles.border.Render(divider)})
	return append(rows, buildWelcomeActionRows(width, styles)...)
}

func buildWelcomeDetails(width int, version string, announcement welcomeAnnouncement, styles welcomePanelStyles) []welcomePanelRow {
	width = maxInt(0, width)
	rows := []welcomePanelRow{
		welcomeBrandVersionCell("CAELIS", version, width, styles),
		welcomeTextCell("", width, styles.surface),
	}
	noticeRows := welcomeNoticeCells(announcement, width, 3, styles)
	rows = append(rows, noticeRows...)
	if len(noticeRows) < 3 {
		rows = append(rows, welcomeTextCell("", width, styles.surface))
	}
	for _, action := range welcomeActions {
		rows = append(rows, welcomeActionCell(action, width, styles))
	}
	return rows
}

func welcomePanelSupportsAnnouncement(ctx BlockRenderContext, announcement welcomeAnnouncement) bool {
	if ctx.Width >= welcomeCompactMinWidth && ctx.Height >= welcomeCompactMinHeight {
		return true
	}
	if ctx.Height < len(welcomeActions)+2 {
		return false
	}
	return len(graphemeWordWrap(strings.TrimSpace(announcement.plainText()), maxInt(1, ctx.Width))) <= 1
}

func welcomeNoticeCells(announcement welcomeAnnouncement, width int, maxRows int, styles welcomePanelStyles) []welcomePanelRow {
	if width <= 0 {
		return nil
	}
	lines := graphemeWordWrap(strings.TrimSpace(announcement.plainText()), width)
	if len(lines) > maxRows {
		lines = lines[:maxRows]
	}
	emphasisRemaining := announcement.emphasis
	rows := make([]welcomePanelRow, 0, len(lines))
	for _, line := range lines {
		emphasisRemaining = strings.TrimLeft(emphasisRemaining, " \t")
		emphasized := ""
		switch {
		case emphasisRemaining == "":
		case strings.HasPrefix(emphasisRemaining, line):
			emphasized = line
			emphasisRemaining = strings.TrimPrefix(emphasisRemaining, line)
		case strings.HasPrefix(line, emphasisRemaining):
			emphasized = emphasisRemaining
			emphasisRemaining = ""
		}
		rows = append(rows, welcomeNoticeCell(line, emphasized, width, styles))
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
	left, right := "╭", "╮"
	if !top {
		left, right = "╰", "╯"
	}
	plain := left + strings.Repeat("─", maxInt(0, width-2)) + right
	return welcomePanelRow{plain: plain, styled: styles.border.Render(plain)}
}

func buildWelcomeActionRows(width int, styles welcomePanelStyles) []welcomePanelRow {
	width = maxInt(1, width)
	rows := make([]welcomePanelRow, 0, len(welcomeActions))
	for _, action := range welcomeActions {
		rows = append(rows, welcomeActionCell(action, width, styles))
	}
	return rows
}

func welcomePreferredDetailsWidth(announcement welcomeAnnouncement) int {
	return maxInt(welcomeDetailsMinWidth, displayColumns(strings.TrimSpace(announcement.plainText())))
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

func welcomeNoticeCell(text string, emphasized string, width int, styles welcomePanelStyles) welcomePanelRow {
	text = fitWelcomeText(text, maxInt(0, width))
	if !strings.HasPrefix(text, emphasized) {
		emphasized = ""
	}
	styled := styles.notice.Render(text)
	if emphasized != "" {
		styled = styles.noticeEmphasis.Render(emphasized) + styles.notice.Render(strings.TrimPrefix(text, emphasized))
	}
	rightSpace := strings.Repeat(" ", maxInt(0, width-displayColumns(text)))
	return welcomePanelRow{
		plain:  text + rightSpace,
		styled: styled + styles.surface.Render(rightSpace),
	}
}

func welcomeActionCell(action welcomeAction, width int, styles welcomePanelStyles) welcomePanelRow {
	width = maxInt(1, width)
	labelWidth := width
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
