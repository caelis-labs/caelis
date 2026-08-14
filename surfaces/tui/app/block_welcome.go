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
}

func NewWelcomeBlock(version string) *WelcomeBlock {
	return &WelcomeBlock{
		id:      nextBlockID(),
		Version: version,
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
	panelRows := buildWelcomePanel(ctx, welcomeVersionLabel(b.Version))
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
		if row.clickEndCol > row.clickStartCol {
			clickStartCol = leftPad + row.clickStartCol
			clickEndCol = leftPad + row.clickEndCol
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
	plain         string
	styled        string
	token         string
	clickStartCol int
	clickEndCol   int
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

func buildWelcomePanel(ctx BlockRenderContext, version string) []welcomePanelRow {
	styles := newWelcomePanelStyles(ctx.Theme.Tokens())
	switch {
	case ctx.Width >= welcomeStandardMinWidth && ctx.Height >= welcomeStandardMinHeight:
		return buildStandardWelcomePanel(ctx.Width, ctx.Height, version, styles)
	case ctx.Width >= welcomeCompactMinWidth && ctx.Height >= welcomeCompactMinHeight:
		return buildCompactWelcomePanel(ctx.Width, ctx.Height, version, styles)
	default:
		return buildWelcomeActionRows(ctx.Width, styles)
	}
}

func buildStandardWelcomePanel(width int, height int, version string, styles welcomePanelStyles) []welcomePanelRow {
	panelWidth := welcomeStandardPanelWidth(width)
	innerWidth := panelWidth - 2
	horizontalPadding := 1
	gap := 2
	switch {
	case panelWidth >= 96:
		horizontalPadding = 4
		gap = 8
	case panelWidth >= welcomePanelBaseWidth:
		horizontalPadding = 2
		gap = 4
	}
	const logoWidth = 18
	rightWidth := maxInt(0, innerWidth-horizontalPadding*2-logoWidth-gap)
	rightRows := buildWelcomeDetails(rightWidth, version, styles)
	contentRows := welcomeSideBySideRows(
		welcomeRelayLogoASCII,
		logoWidth,
		rightRows,
		rightWidth,
		horizontalPadding,
		gap,
		styles,
	)
	return frameWelcomePanel(panelWidth, height, contentRows, styles)
}

func buildCompactWelcomePanel(width int, height int, version string, styles welcomePanelStyles) []welcomePanelRow {
	panelWidth := minInt(40, maxInt(1, width))
	innerWidth := panelWidth - 2
	const (
		logoWidth = 8
		gap       = 1
	)
	rightWidth := maxInt(0, innerWidth-logoWidth-gap)
	headerRows := []welcomePanelRow{
		welcomeBrandVersionCell("CAELIS", version, rightWidth, styles),
		welcomeTextCell("", rightWidth, styles.surface),
		welcomeTextCell(welcomeDefaultNotice, rightWidth, styles.notice),
		welcomeTextCell("", rightWidth, styles.surface),
	}
	contentRows := welcomeSideBySideRows(welcomeRelayCompactASCII, logoWidth, headerRows, rightWidth, 0, gap, styles)
	for _, action := range welcomeActions {
		actionRow := welcomeActionCell(action, maxInt(0, innerWidth-1), true, styles)
		contentRows = append(contentRows, padWelcomePanelRow(actionRow, 1, 0, styles))
	}
	contentRows = append(contentRows, welcomeTextCell("", innerWidth, styles.surface))
	return frameWelcomePanel(panelWidth, height, contentRows, styles)
}

func buildWelcomeDetails(width int, version string, styles welcomePanelStyles) []welcomePanelRow {
	rows := []welcomePanelRow{
		welcomeBrandVersionCell("CAELIS", version, width, styles),
		welcomeTextCell("", width, styles.surface),
		welcomeTextCell(welcomeDefaultNotice, width, styles.notice),
		welcomeTextCell("", width, styles.surface),
	}
	for _, action := range welcomeActions {
		rows = append(rows, welcomeActionCell(action, width, true, styles))
	}
	return append(rows, welcomeTextCell("", width, styles.surface))
}

func buildWelcomeActionRows(width int, styles welcomePanelStyles) []welcomePanelRow {
	width = maxInt(1, width)
	rows := make([]welcomePanelRow, 0, len(welcomeActions))
	for _, action := range welcomeActions {
		rows = append(rows, welcomeActionCell(action, width, false, styles))
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

func welcomeSideBySideRows(
	leftLines []string,
	leftWidth int,
	rightRows []welcomePanelRow,
	rightWidth int,
	horizontalPadding int,
	gap int,
	styles welcomePanelStyles,
) []welcomePanelRow {
	rowCount := maxInt(len(leftLines), len(rightRows))
	rows := make([]welcomePanelRow, 0, rowCount)
	leftInset := strings.Repeat(" ", maxInt(0, horizontalPadding))
	gapText := strings.Repeat(" ", maxInt(0, gap))
	rightInset := leftInset
	rightOffset := displayColumns(leftInset) + maxInt(0, leftWidth) + displayColumns(gapText)
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
			styled: styles.surface.Render(leftInset) + leftStyled +
				styles.surface.Render(gapText) + right.styled + styles.surface.Render(rightInset),
			token: right.token,
		}
		if right.clickEndCol > right.clickStartCol {
			row.clickStartCol = rightOffset + right.clickStartCol
			row.clickEndCol = rightOffset + right.clickEndCol + displayColumns(rightInset)
		}
		rows = append(rows, row)
	}
	return rows
}

func padWelcomePanelRow(row welcomePanelRow, left int, right int, styles welcomePanelStyles) welcomePanelRow {
	leftSpace := strings.Repeat(" ", maxInt(0, left))
	rightSpace := strings.Repeat(" ", maxInt(0, right))
	padded := welcomePanelRow{
		plain:  leftSpace + row.plain + rightSpace,
		styled: styles.surface.Render(leftSpace) + row.styled + styles.surface.Render(rightSpace),
		token:  row.token,
	}
	if row.clickEndCol > row.clickStartCol {
		padded.clickStartCol = displayColumns(leftSpace) + row.clickStartCol
		padded.clickEndCol = displayColumns(leftSpace) + row.clickEndCol + displayColumns(rightSpace)
	}
	return padded
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
	framed := welcomePanelRow{
		plain:  "│" + row.plain + "│",
		styled: styles.border.Render("│") + row.styled + styles.border.Render("│"),
		token:  row.token,
	}
	if row.clickEndCol > row.clickStartCol {
		framed.clickStartCol = 1 + row.clickStartCol
		framed.clickEndCol = 1 + row.clickEndCol
	}
	return framed
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

func welcomeBrandVersionCell(brand string, version string, width int, styles welcomePanelStyles) welcomePanelRow {
	width = maxInt(0, width)
	brand = fitWelcomeText(brand, width)
	brandWidth := displayColumns(brand)
	versionGap := ""
	if brandWidth < width {
		versionGap = strings.Repeat(" ", minInt(2, width-brandWidth))
	}
	versionWidth := maxInt(0, width-brandWidth-displayColumns(versionGap))
	version = fitWelcomeText(version, versionWidth)
	rightSpace := strings.Repeat(" ", maxInt(0, width-brandWidth-displayColumns(versionGap)-displayColumns(version)))
	return welcomePanelRow{
		plain: brand + versionGap + version + rightSpace,
		styled: styles.brand.Render(brand) +
			styles.surface.Render(versionGap) +
			styles.meta.Render(version) +
			styles.surface.Render(rightSpace),
	}
}

func welcomeTextCell(text string, width int, style lipgloss.Style) welcomePanelRow {
	plain, styled := welcomeCell(text, width, style.Render)
	return welcomePanelRow{plain: plain, styled: styled}
}

func welcomeActionCell(action welcomeAction, width int, showMarker bool, styles welcomePanelStyles) welcomePanelRow {
	label := action.label
	if showMarker && width >= displayColumns(welcomeActionMarker)+displayColumns(label) {
		label = welcomeActionMarker + label
	}
	plain, styled := welcomeCell(label, width, func(text ...string) string {
		value := strings.Join(text, "")
		if !strings.HasPrefix(value, welcomeActionMarker) {
			return styles.action.Render(value)
		}
		return styles.actionMarker.Render(welcomeActionMarker) +
			styles.action.Render(strings.TrimPrefix(value, welcomeActionMarker))
	})
	return welcomePanelRow{
		plain:         plain,
		styled:        styled,
		token:         action.token,
		clickStartCol: 0,
		clickEndCol:   width,
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
