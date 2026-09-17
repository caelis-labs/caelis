package tuiapp

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

const (
	welcomePanelMaxWidth = 64
	welcomeActionMarker  = "› "
	welcomeDefaultNotice = "type / for commands"
)

const (
	welcomeActionTokenConnect = "welcome:action:connect"
	welcomeActionTokenModel   = "welcome:action:model"
	welcomeActionTokenResume  = "welcome:action:resume"
	welcomeActionTokenTeam    = "welcome:action:team"
)

var welcomeWordmarkASCII = []string{
	`█▀▀ ▄▀▄ █▀▀ █   ▀█▀ █▀▀`,
	`█   █▀█ █▀  █    █  ▀▀█`,
	`▀▀▀ ▀ ▀ ▀▀▀ ▀▀▀ ▀▀▀ ▀▀▀`,
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
			clickStartCol = leftPad + row.clickStart
			clickEndCol = leftPad + row.clickEnd
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
	plain      string
	styled     string
	token      string
	clickStart int
	clickEnd   int
}

type welcomePanelStyles struct {
	surface        lipgloss.Style
	border         lipgloss.Style
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
		brand:          tokens.TextPrimary.Bold(true),
		action:         tokens.TextPrimary,
		actionMarker:   tokens.Accent.Bold(true),
		meta:           tokens.ChromeMeta,
		notice:         tokens.TextMuted,
		noticeEmphasis: tokens.Warning,
	}
	if background := tokens.Surface0.GetBackground(); background != nil {
		styles.border = styles.border.Background(background)
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
	width := minInt(welcomePanelMaxWidth, maxInt(1, ctx.Width))
	actions := buildWelcomeActionRows(width, styles)
	headerHeight, noticeBudget := welcomeLayoutBudget(width, ctx.Height, announcement)
	rows := make([]welcomePanelRow, 0, maxInt(ctx.Height, 0))
	if headerHeight == 3 {
		for i, line := range welcomeWordmarkASCII {
			if i == 2 {
				rows = append(rows, welcomeBrandVersionCell(line, version, width, styles))
			} else {
				rows = append(rows, welcomeTextCell(line, width, styles.brand))
			}
		}
	} else {
		// Even short terminals keep the installed version visible.
		rows = append(rows, welcomeBrandVersionCell("CAELIS", version, width, styles))
	}
	if notice := welcomeNoticeCells(announcement, width, noticeBudget, styles); len(notice) > 0 {
		rows = append(rows, welcomeTextCell("", width, styles.surface))
		rows = append(rows, notice...)
	}
	if ctx.Height >= len(rows)+len(actions)+3 {
		rows = append(rows, welcomeTextCell("", width, styles.surface),
			welcomeTextCell(strings.Repeat("─", width), width, styles.border),
			welcomeTextCell("", width, styles.surface))
	} else if ctx.Height >= len(rows)+len(actions)+1 {
		rows = append(rows, welcomeTextCell(strings.Repeat("─", width), width, styles.border))
	}
	return append(rows, actions...)
}

// Content determines the height. Reserve command rows first, then let the
// announcement grow to six lines; short terminals use a one-line identity.
func welcomeLayoutBudget(width, height int, announcement welcomeAnnouncement) (header, notice int) {
	actionRows := len(welcomeActions)
	wanted := 0
	if announcement.plainText() != "" {
		wanted = minInt(6, len(graphemeWordWrap(announcement.plainText(), width)))
	}
	header = 3
	required := header + actionRows + 3
	if wanted > 0 {
		required += 1 + wanted
	}
	if width < displayColumns(welcomeWordmarkASCII[0])+10 || height < required {
		header = 1
	}
	notice = maxInt(0, height-header-actionRows-4)
	if notice < wanted {
		notice = maxInt(0, height-header-actionRows-2)
	}
	return header, minInt(6, notice)
}

func welcomePanelSupportsAnnouncement(ctx BlockRenderContext, announcement welcomeAnnouncement) bool {
	width := minInt(welcomePanelMaxWidth, maxInt(1, ctx.Width))
	_, budget := welcomeLayoutBudget(width, ctx.Height, announcement)
	return len(graphemeWordWrap(strings.TrimSpace(announcement.plainText()), width)) <= budget
}

func welcomeNoticeCells(announcement welcomeAnnouncement, width int, maxRows int, styles welcomePanelStyles) []welcomePanelRow {
	if width <= 0 || maxRows <= 0 || announcement.plainText() == "" {
		return nil
	}
	lines := graphemeWordWrap(strings.TrimSpace(announcement.plainText()), width)
	if len(lines) > maxRows {
		lines = lines[:maxRows]
		lines[maxRows-1] = ansi.Truncate(lines[maxRows-1], width-1, "") + "…"
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

func buildWelcomeActionRows(width int, styles welcomePanelStyles) []welcomePanelRow {
	width = maxInt(1, width)
	rows := make([]welcomePanelRow, 0, len(welcomeActions))
	for _, action := range welcomeActions {
		rows = append(rows, welcomeActionCell(action, width, styles))
	}
	return rows
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
		plain:    plain,
		styled:   styled,
		token:    action.token,
		clickEnd: displayColumns(label),
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
	return ansi.Truncate(text, width, "")
}
