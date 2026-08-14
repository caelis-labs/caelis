package tuikit

import (
	"strings"

	"charm.land/lipgloss/v2"
)

type PanelShellVariant string

const (
	PanelShellVariantTranscript PanelShellVariant = "transcript"
	PanelShellVariantDrawer     PanelShellVariant = "drawer"
	PanelShellVariantCard       PanelShellVariant = "card"
)

type PanelHeaderModel struct {
	Expanded bool
	Kind     string
	Title    string
	State    string
	Meta     string
}

type PanelShellModel struct {
	Variant PanelShellVariant
	Width   int
	Header  string
	Body    []string
	Footer  string
}

type ToolLineModel struct {
	Prefix string
	Name   string
	Suffix string
	Style  LineStyle
}

func RenderStatusBadge(theme Theme, tone string, label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	return theme.TranscriptPillStyle(tone).Render(label)
}

func RenderPanelHeader(theme Theme, width int, model PanelHeaderModel) string {
	icon := "▾"
	if !model.Expanded {
		icon = "▸"
	}
	leftParts := []string{
		theme.TranscriptRailStyle().Bold(true).Render(icon),
	}
	if kind := strings.ToUpper(strings.TrimSpace(model.Kind)); kind != "" {
		leftParts = append(leftParts, RenderStatusBadge(theme, "accent", kind))
	}
	if title := strings.TrimSpace(model.Title); title != "" {
		leftParts = append(leftParts, theme.TitleStyle().Render(title))
	}
	if state := strings.TrimSpace(model.State); state != "" {
		leftParts = append(leftParts, RenderStatusBadge(theme, statusTone(state), statusLabel(state)))
	}
	left := strings.Join(filterNonEmptyStrings(leftParts), " ")
	right := theme.TranscriptMetaStyle().Render(strings.TrimSpace(model.Meta))
	return composeStyledFooter(width, left, right)
}

func RenderPanelShell(theme Theme, model PanelShellModel) []string {
	width := maxInt(1, model.Width)
	header := strings.TrimSpace(model.Header)
	footer := strings.TrimSpace(model.Footer)
	body := append([]string(nil), model.Body...)

	switch model.Variant {
	case PanelShellVariantDrawer, PanelShellVariantCard:
		content := make([]string, 0, len(body)+2)
		if header != "" {
			content = append(content, header)
		}
		content = append(content, body...)
		if footer != "" {
			content = append(content, footer)
		}
		boxStyle := lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(theme.PanelBorder).
			Padding(0, 1).
			Width(width)
		return strings.Split(boxStyle.Render(strings.Join(content, "\n")), "\n")
	default:
		lines := make([]string, 0, len(body)+2)
		if header != "" {
			lines = append(lines, theme.TranscriptShellStyle().Render("╭─ ")+header)
		}
		railPrefix := theme.TranscriptRailStyle().Render("│ ")
		for _, line := range body {
			if strings.TrimSpace(line) == "" {
				lines = append(lines, railPrefix)
				continue
			}
			lines = append(lines, railPrefix+line)
		}
		if footer != "" {
			lines = append(lines, theme.TranscriptShellStyle().Render("╰─ ")+footer)
		}
		return lines
	}
}

func RenderToolLine(theme Theme, model ToolLineModel) string {
	plain := strings.TrimSpace(model.Name)
	if prefix := strings.TrimSpace(model.Prefix); prefix != "" {
		if plain != "" {
			plain = prefix + " " + plain
		} else {
			plain = prefix
		}
	}
	if suffix := strings.TrimSpace(model.Suffix); suffix != "" {
		if plain != "" {
			plain += " "
		}
		plain += suffix
	}
	return ColorizeLogLine(plain, model.Style, theme)
}

func RenderCodeSurface(theme Theme, width int, lines []string) []string {
	width = maxInt(1, width)
	rendered := make([]string, 0, len(lines))
	for _, line := range lines {
		rendered = append(rendered, theme.CodeSurfaceStyle().Width(width).Render(line))
	}
	return rendered
}

func RenderTable(theme Theme, rows []string, headerRows int) []string {
	rendered := make([]string, 0, len(rows))
	for i, row := range rows {
		switch {
		case i < headerRows:
			rendered = append(rendered, theme.TableHeaderStyle().Render(row))
		case isTableBorderRow(row):
			rendered = append(rendered, theme.TableBorderStyle().Render(row))
		default:
			rendered = append(rendered, theme.TextStyle().Render(row))
		}
	}
	return rendered
}

func statusLabel(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "running", "":
		return "running"
	case "waiting_approval":
		return "approval"
	case "waiting_input":
		return "input"
	case "completed":
		return "done"
	case "failed":
		return "failed"
	case "interrupted":
		return "interrupted"
	case "timed_out":
		return "timed out"
	case "cancelled", "canceled":
		return "cancelled"
	case "terminated":
		return "terminated"
	default:
		return strings.TrimSpace(state)
	}
}

func statusTone(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "completed":
		return "success"
	case "waiting_approval", "waiting_input", "interrupted", "timed_out", "cancelled", "canceled", "terminated":
		return "warning"
	case "failed":
		return "error"
	case "running", "":
		return "accent"
	default:
		return ""
	}
}

func composeStyledFooter(width int, left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if width <= 0 {
		return ""
	}
	leftWidth := lipgloss.Width(left)
	rightWidth := lipgloss.Width(right)
	if left == "" && right == "" {
		return strings.Repeat(" ", width)
	}
	if left == "" {
		if rightWidth >= width {
			return right
		}
		return strings.Repeat(" ", width-rightWidth) + right
	}
	if right == "" {
		if leftWidth >= width {
			return left
		}
		return left + strings.Repeat(" ", width-leftWidth)
	}
	gap := maxInt(width-leftWidth-rightWidth, 1)
	return left + strings.Repeat(" ", gap) + right
}

func filterNonEmptyStrings(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item) == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func isTableBorderRow(row string) bool {
	trimmed := strings.TrimSpace(row)
	if trimmed == "" {
		return false
	}
	for _, r := range trimmed {
		switch r {
		case '|', '-', ':', '+', ' ':
		default:
			return false
		}
	}
	return true
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
