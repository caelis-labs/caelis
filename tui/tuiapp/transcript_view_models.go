package tuiapp

import (
	"cmp"
	"os"
	"strings"

	"github.com/OnslaughtSnail/caelis/tui/tuikit"
)

type PanelViewModel struct {
	Variant tuikit.PanelShellVariant
	Width   int
	Header  tuikit.PanelHeaderModel
	Body    []string
	Footer  string
}

type ToolEventViewModel struct {
	Name       string
	Args       string
	Output     string
	Done       bool
	Err        bool
	Expandable bool
	Expanded   bool
	ClickToken string
}

type WelcomeViewModel struct {
	VersionLabel string
	Workspace    string
	ModelAlias   string
}

type renderedSegment struct {
	Plain  string
	Styled string
}

func renderPanelViewModel(theme tuikit.Theme, vm PanelViewModel) []string {
	width := maxInt(1, vm.Width)
	headerWidth := width
	switch vm.Variant {
	case tuikit.PanelShellVariantDrawer, tuikit.PanelShellVariantCard:
		headerWidth = maxInt(1, width-4)
	}
	header := ""
	if strings.TrimSpace(vm.Header.Kind) != "" || strings.TrimSpace(vm.Header.Title) != "" || strings.TrimSpace(vm.Header.State) != "" || strings.TrimSpace(vm.Header.Meta) != "" {
		header = tuikit.RenderPanelHeader(theme, headerWidth, vm.Header)
	}
	return tuikit.RenderPanelShell(theme, tuikit.PanelShellModel{
		Variant: vm.Variant,
		Width:   width,
		Header:  header,
		Body:    vm.Body,
		Footer:  strings.TrimSpace(vm.Footer),
	})
}

func buildWelcomeViewModel(version, workspace, modelName string) WelcomeViewModel {
	versionText := cmp.Or(strings.TrimSpace(version), "unknown")
	versionLabel := versionText
	if !strings.HasPrefix(strings.ToLower(versionText), "v") {
		versionLabel = "v" + versionText
	}

	workspace = cmp.Or(strings.TrimSpace(workspace), ".")
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		workspace = strings.Replace(workspace, home, "~", 1)
	}

	return WelcomeViewModel{
		VersionLabel: versionLabel,
		Workspace:    workspace,
		ModelAlias:   cmp.Or(strings.TrimSpace(modelName), "not configured (/connect)"),
	}
}

func buildWelcomePanelViewModel(w WelcomeViewModel, width int, theme tuikit.Theme) PanelViewModel {
	tok := theme.Tokens()
	contentWidth := maxInt(1, width-4)
	valueWidth := maxInt(8, contentWidth-11)
	titleLine := theme.PromptStyle().Render(">_") +
		" " + tok.ChromeTitle.Render("CAELIS") +
		" " + tok.ChromeMeta.Render("("+w.VersionLabel+")")
	renderField := func(label string, value string, style func(...string) string) string {
		labelText := tok.ComposerLabel.Render(label + ":")
		padding := maxInt(0, 11-displayColumns(label+":"))
		if label == "workspace" {
			value = truncateWorkspaceStatusDisplay(value, valueWidth)
		} else {
			value = truncateTailDisplay(value, valueWidth)
		}
		return labelText + strings.Repeat(" ", padding) + style(value)
	}
	body := []string{
		titleLine,
		"",
		renderField("model", w.ModelAlias, tok.TextPrimary.Render),
		renderField("workspace", w.Workspace, tok.TextSecondary.Render),
		renderField("tip", "type / for command list", tok.TextMuted.Render),
	}
	return PanelViewModel{
		Variant: tuikit.PanelShellVariantCard,
		Width:   width,
		Body:    body,
	}
}

func truncateTailDisplay(text string, width int) string {
	text = strings.TrimSpace(text)
	if text == "" || width <= 0 || displayColumns(text) <= width {
		return text
	}
	if width <= 3 {
		return sliceByDisplayColumns(text, 0, width)
	}
	return sliceByDisplayColumns(text, 0, width-3) + "..."
}

func buildToolEventViewModel(ev SubagentEvent) ToolEventViewModel {
	return ToolEventViewModel{
		Name:   strings.TrimSpace(ev.Name),
		Args:   strings.TrimSpace(ev.Args),
		Output: strings.TrimSpace(ev.Output),
		Done:   ev.Done,
		Err:    ev.Err,
	}
}

func renderToolEventViewModelLines(blockID string, vm ToolEventViewModel, width int, theme tuikit.Theme) []RenderedRow {
	segments := renderToolEventViewModelSegments(vm, width, theme)
	rows := make([]RenderedRow, 0, len(segments))
	for _, segment := range segments {
		rows = append(rows, StyledPlainClickableRow(blockID, segment.Plain, segment.Styled, vm.ClickToken))
	}
	return rows
}

func renderToolEventViewModelSegments(vm ToolEventViewModel, width int, theme tuikit.Theme) []renderedSegment {
	text, style := renderToolEventViewModelPlain(vm)
	lines := wrapToolOutputText(text, maxInt(1, width))
	if len(lines) == 0 {
		lines = []string{text}
	}
	segments := make([]renderedSegment, 0, len(lines))
	for i, line := range lines {
		if i > 0 {
			line = "  " + line
		}
		segments = append(segments, renderedSegment{
			Plain:  line,
			Styled: styleToolEventLine(theme, line, style),
		})
	}
	return segments
}

func styleToolEventLine(theme tuikit.Theme, line string, style tuikit.LineStyle) string {
	components := theme.ComponentStyles()
	switch style {
	case tuikit.LineStyleTool:
		if styled, ok := styleRequestPermissionsToolLine(theme, line); ok {
			return styled
		}
		parts := strings.SplitN(strings.TrimSpace(line), " ", 3)
		model := tuikit.ToolLineModel{Style: tuikit.LineStyleTool}
		if len(parts) > 0 {
			model.Prefix = parts[0]
		}
		if len(parts) > 1 {
			model.Name = parts[1]
		}
		if len(parts) > 2 {
			model.Suffix = parts[2]
		}
		styled := tuikit.RenderToolLine(theme, model)
		if strings.HasPrefix(line, "  ") {
			styled = "  " + styled
		}
		return styled
	case tuikit.LineStyleError:
		return components.Tool.Error.Render(line)
	default:
		return components.Tool.Normal.Render(line)
	}
}

func styleRequestPermissionsToolLine(theme tuikit.Theme, line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	prefix, rest, ok := splitToolLifecycleHeader(trimmed)
	if !ok {
		return "", false
	}
	const name = "Request permissions"
	if !strings.HasPrefix(rest, name) {
		return "", false
	}
	suffix := strings.TrimSpace(strings.TrimPrefix(rest, name))
	prefixStyle := theme.ToolStyle()
	nameStyle := theme.ToolNameStyle()
	suffixStyle := theme.ToolArgsStyle()
	switch prefix {
	case "✓":
		prefixStyle = theme.AssistantStyle()
		suffixStyle = theme.ToolResultStyle()
	case "✗":
		prefixStyle = theme.ToolErrorStyle()
		nameStyle = theme.ToolErrorStyle()
		suffixStyle = theme.ToolErrorStyle()
	}
	styled := prefixStyle.Render(prefix+" ") + nameStyle.Render(name)
	if suffix != "" {
		styled += " " + suffixStyle.Render(tuikit.LinkifyText(suffix, theme.LinkStyle()))
	}
	if strings.HasPrefix(line, "  ") {
		styled = "  " + styled
	}
	return styled, true
}

func splitToolLifecycleHeader(line string) (prefix string, rest string, ok bool) {
	line = strings.TrimSpace(line)
	for _, candidate := range []string{"▸", "▾", "✓", "✗"} {
		if strings.HasPrefix(line, candidate+" ") {
			return candidate, strings.TrimSpace(strings.TrimPrefix(line, candidate+" ")), true
		}
	}
	return "", "", false
}

func renderToolEventViewModelPlain(vm ToolEventViewModel) (string, tuikit.LineStyle) {
	name := toolEventDisplayName(vm.Name)
	if !vm.Done {
		prefix := "▸"
		if vm.Expandable && vm.Expanded {
			prefix = "▾"
		}
		line := prefix + " " + name
		if vm.Args != "" {
			line += " " + vm.Args
		}
		return line, tuikit.LineStyleTool
	}
	if vm.Err {
		line := "✗ " + name
		if vm.Output != "" {
			line += " " + vm.Output
		}
		return line, tuikit.LineStyleTool
	}
	line := "✓ " + name
	if vm.Output != "" {
		line += " " + vm.Output
	} else {
		line += " completed"
	}
	return line, tuikit.LineStyleTool
}

func toolEventDisplayName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "TOOL"
	}
	switch strings.ToUpper(strings.ReplaceAll(name, " ", "_")) {
	case "REQUEST_PERMISSIONS":
		return "Request permissions"
	default:
		return cmp.Or(strings.TrimSpace(name), "TOOL")
	}
}
