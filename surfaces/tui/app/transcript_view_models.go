package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/surfaces/tui/displaymodel"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

type PanelViewModel struct {
	Variant tuikit.PanelShellVariant
	Width   int
	Header  tuikit.PanelHeaderModel
	Body    []string
	Footer  string
}

type ToolEventViewModel = displaymodel.ToolEventViewModel

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
	return displaymodel.BuildToolEventViewModel(displaymodel.ToolEvent{
		Name:   tuikit.SanitizeLogText(ev.Name),
		Args:   tuikit.SanitizeLogText(ev.Args),
		Output: tuikit.SanitizeLogText(ev.Output),
		Done:   ev.Done,
		Err:    ev.Err,
	})
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
	line = tuikit.SanitizeLogText(line)
	components := theme.ComponentStyles()
	switch style {
	case tuikit.LineStyleTool:
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

func renderToolEventViewModelPlain(vm ToolEventViewModel) (string, tuikit.LineStyle) {
	return displaymodel.RenderToolEventLine(vm), tuikit.LineStyleTool
}

func toolEventDisplayName(name string) string {
	return displaymodel.ToolEventDisplayName(name)
}
