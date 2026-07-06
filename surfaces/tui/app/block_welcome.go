package tuiapp

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

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
	var lines []string
	versionStr := welcomeVersionLabel(b.Version)

	if ctx.Width >= tuikit.WelcomeLogoMinWidth {
		accent := lipgloss.NewStyle().Foreground(ctx.Theme.Focus).Render
		logo := []string{
			`  ██████╗  █████╗  ███████╗██╗     ██╗███████╗`,
			` ██╔════╝ ██╔══██╗ ██╔════╝██║     ██║██╔════╝`,
			` ██║      ███████║ █████╗  ██║     ██║███████╗`,
			` ██║      ██╔══██║ ██╔══╝  ██║     ██║╚════██║`,
			` ╚██████╗ ██║  ██║ ███████╗███████╗██║███████║`,
			`  ╚═════╝ ╚═╝  ╚═╝ ╚══════╝╚══════╝╚═╝╚══════╝`,
		}
		for _, l := range logo {
			lines = append(lines, accent(l))
		}
		lines = append(lines, "")

		versionTip := fmt.Sprintf("%s  ·  type / for commands", versionStr)
		lines = append(lines, ctx.Theme.Tokens().ChromeMeta.Render(versionTip))
	} else {
		titleLine := ctx.Theme.PromptStyle().Render(">_") +
			" " + ctx.Theme.Tokens().ChromeTitle.Render("CAELIS") +
			" " + ctx.Theme.Tokens().ChromeMeta.Render("("+versionStr+")") +
			"  " + ctx.Theme.Tokens().TextMuted.Render("type / for commands")
		lines = append(lines, titleLine)
	}

	for i, line := range lines {
		lines[i] = lipgloss.PlaceHorizontal(ctx.Width, lipgloss.Center, line)
	}

	contentHeight := len(lines)
	topPadding := 0
	if ctx.Height > contentHeight {
		topPadding = maxInt(0, (ctx.Height-contentHeight)/3)
	}

	rows := make([]RenderedRow, 0, topPadding+len(lines)+1)
	for range topPadding {
		rows = append(rows, StyledRow(b.id, ""))
	}
	for _, line := range lines {
		rows = append(rows, StyledRow(b.id, line))
	}
	rows = append(rows, StyledRow(b.id, ""))
	return rows
}
