package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

type slashOutputLine struct {
	Text  string
	Style tuikit.LineStyle
	Plain bool
}

type slashOutputBlock struct {
	id    string
	lines []slashOutputLine
}

func newSlashOutputBlock(lines []slashOutputLine) *slashOutputBlock {
	out := make([]slashOutputLine, 0, len(lines))
	for _, line := range lines {
		out = append(out, slashOutputLine{
			Text:  strings.TrimRight(line.Text, " \t"),
			Style: line.Style,
			Plain: line.Plain,
		})
	}
	return &slashOutputBlock{id: nextBlockID(), lines: out}
}

func (b *slashOutputBlock) BlockID() string { return b.id }
func (b *slashOutputBlock) Kind() BlockKind { return BlockTranscript }
func (b *slashOutputBlock) Render(ctx BlockRenderContext) []RenderedRow {
	rows := make([]RenderedRow, 0, len(b.lines))
	for _, line := range b.lines {
		if strings.TrimSpace(line.Text) == "" {
			rows = append(rows, PlainRow(b.id, ""))
			continue
		}
		style := line.Style
		if style == 0 && !line.Plain {
			style = tuikit.DetectLineStyle(line.Text)
		}
		plain := line.Text
		styled := tuikit.LineExtraGutter(style) + tuikit.ColorizeLogLine(line.Text, style, ctx.Theme)
		rows = append(rows, StyledPlainRow(b.id, plain, styled))
	}
	return rows
}

func (m *Model) handleSlashCommandResultMsg(msg SlashCommandResultMsg) (tea.Model, tea.Cmd) {
	return m.appendSlashOutputLines(renderSlashCommandResultLines(msg.Result))
}

func (m *Model) handleSlashNoticeMsg(msg SlashNoticeMsg) (tea.Model, tea.Cmd) {
	return m.appendSlashOutputLines(renderSlashNoticeLines(msg))
}

func (m *Model) appendSlashOutputLines(lines []slashOutputLine) (tea.Model, tea.Cmd) {
	if len(lines) == 0 {
		return m, nil
	}
	m.finalizeAssistantBlock()
	m.finalizeReasoningBlock()
	if m.hasCommittedLine {
		m.appendSlashOutputSpacer()
	}
	block := newSlashOutputBlock(lines)
	m.doc.Append(block)
	m.appendSlashOutputSpacer()
	m.lastCommittedStyle = lastSlashOutputStyle(lines)
	m.lastCommittedRaw = lastSlashOutputText(lines)
	m.hasCommittedLine = true
	m.markViewportStructureDirty()
	m.ensureViewportLayout()
	return m, m.requestStreamViewportSync()
}

func (m *Model) appendSlashOutputSpacer() {
	if m == nil || m.doc == nil || m.doc.Len() == 0 {
		return
	}
	if last, ok := m.doc.Last().(*TranscriptBlock); ok && strings.TrimSpace(last.Raw) == "" {
		return
	}
	m.doc.Append(NewSpacerBlock())
}

func renderSlashNoticeLines(msg SlashNoticeMsg) []slashOutputLine {
	text := strings.TrimSpace(strings.ReplaceAll(msg.Text, "\r\n", "\n"))
	if text == "" {
		return nil
	}
	rawLines := strings.Split(text, "\n")
	lines := make([]slashOutputLine, 0, len(rawLines))
	for _, raw := range rawLines {
		content := strings.TrimSpace(raw)
		if content == "" {
			lines = append(lines, slashBlank())
			continue
		}
		lines = append(lines, slashOutputLine{Text: content, Plain: true})
	}
	return lines
}

func lastSlashOutputText(lines []slashOutputLine) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i].Text) != "" {
			return lines[i].Text
		}
	}
	return ""
}

func lastSlashOutputStyle(lines []slashOutputLine) tuikit.LineStyle {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i].Text) != "" {
			return lines[i].Style
		}
	}
	return tuikit.LineStyleDefault
}

func renderSlashCommandResultLines(result control.SlashCommandResult) []slashOutputLine {
	switch result.Kind {
	case control.SlashCommandResultStatus:
		return renderSlashStatusLines(result.Status)
	case control.SlashCommandResultHelp:
		return renderSlashHelpLines(result.Help)
	case control.SlashCommandResultTable:
		return renderSlashTableLines(result.Table)
	default:
		return []slashOutputLine{slashField("Slash", control.FormatSlashResult(result))}
	}
}

func renderSlashTableLines(table control.SlashTableSnapshot) []slashOutputLine {
	lines := make([]slashOutputLine, 0)
	if title := strings.TrimSpace(table.Title); title != "" {
		lines = append(lines, slashSection(title))
	}
	writtenSections := 0
	for _, section := range table.Sections {
		if len(section.Columns) == 0 && len(section.Rows) == 0 {
			continue
		}
		if writtenSections > 0 {
			lines = append(lines, slashBlank())
		}
		if title := strings.TrimSpace(section.Title); title != "" {
			lines = append(lines, slashSection(title))
		}
		rows := make([][]string, 0, len(section.Rows)+1)
		if len(section.Columns) > 0 {
			rows = append(rows, append([]string(nil), section.Columns...))
		}
		for _, row := range section.Rows {
			rows = append(rows, append([]string(nil), row...))
		}
		lines = append(lines, renderSlashPaddedRowsWithOptions(rows, len(section.Columns) > 0)...)
		writtenSections++
	}
	return lines
}

func renderSlashStatusLines(status control.StatusSnapshot) []slashOutputLine {
	view := control.StatusDisplayFromSnapshot(status)
	lines := []slashOutputLine{slashSection("Status")}
	for _, field := range view.Fields {
		lines = append(lines, slashField(field.Label, field.Value))
	}
	if len(view.Warnings) > 0 {
		lines = append(lines, slashBlank(), slashSection("Warnings"))
		for _, warning := range view.Warnings {
			lines = append(lines, slashField("Warning", warning))
		}
	}
	if !view.Usage.Empty() {
		lines = append(lines, slashBlank(), slashSection("Usage"))
		lines = append(lines, renderSlashTokenUsage(view.Usage)...)
	}
	return lines
}

func renderSlashHelpLines(help control.CommandHelpSnapshot) []slashOutputLine {
	lines := []slashOutputLine{slashSection("Commands")}
	groups := slashHelpGroups(help.Items)
	writtenGroups := 0
	for _, group := range groups {
		if len(group.items) == 0 {
			continue
		}
		if writtenGroups > 0 {
			lines = append(lines, slashBlank())
		}
		lines = append(lines, slashSection(group.title))
		table := make([][]string, 0, len(group.items))
		for _, item := range group.items {
			table = append(table, []string{strings.TrimSpace(item.Usage), strings.TrimSpace(item.Description)})
			for _, detail := range item.Details {
				if detail = strings.TrimSpace(detail); detail != "" {
					table = append(table, []string{"", detail})
				}
			}
		}
		lines = append(lines, renderSlashPaddedRows(table)...)
		writtenGroups++
	}
	return lines
}

type slashHelpGroup struct {
	title string
	items []control.CommandHelpItem
}

func slashHelpGroups(items []control.CommandHelpItem) []slashHelpGroup {
	groupOrder := []string{"Core", "Model & Session", "Agents", "Plugins & Tools", "Lifecycle"}
	groups := make(map[string][]control.CommandHelpItem, len(groupOrder))
	for _, item := range items {
		if strings.TrimSpace(item.Usage) == "" {
			continue
		}
		group := slashHelpGroupTitle(item)
		groups[group] = append(groups[group], item)
	}
	out := make([]slashHelpGroup, 0, len(groupOrder))
	for _, title := range groupOrder {
		out = append(out, slashHelpGroup{title: title, items: groups[title]})
	}
	return out
}

func slashHelpGroupTitle(item control.CommandHelpItem) string {
	if item.Dynamic {
		return "Agents"
	}
	switch strings.ToLower(strings.TrimSpace(item.Name)) {
	case "help", "status", "doctor":
		return "Core"
	case "model", "connect", "new", "resume", "compact":
		return "Model & Session"
	case "review", "breeze", "orbit", "zenith", "subagent":
		return "Agents"
	case "plugin":
		return "Plugins & Tools"
	case "exit", "quit":
		return "Lifecycle"
	default:
		return "Core"
	}
}

func renderSlashTokenUsage(usage control.TokenUsageView) []slashOutputLine {
	table := [][]string{{"Scope", "Total", "Input", "Cached", "Output"}}
	if usage.ShowReasoning {
		table[0] = append(table[0], "Reasoning")
	}
	for _, row := range usage.Rows {
		values := []string{row.Scope, row.Total, row.Input, row.Cached, row.Output}
		if usage.ShowReasoning {
			values = append(values, row.Reasoning)
		}
		table = append(table, values)
	}
	return renderSlashPaddedRowsWithOptions(table, true)
}

func renderSlashPaddedRows(table [][]string) []slashOutputLine {
	return renderSlashPaddedRowsWithOptions(table, false)
}

func renderSlashPaddedRowsWithOptions(table [][]string, hasHeader bool) []slashOutputLine {
	if len(table) == 0 {
		return nil
	}
	widths := make([]int, 0)
	for _, row := range table {
		for len(widths) < len(row) {
			widths = append(widths, 0)
		}
		for i, col := range row {
			if n := len([]rune(col)); n > widths[i] {
				widths[i] = n
			}
		}
	}
	capacity := len(table)
	if hasHeader && len(table) > 0 {
		capacity++
	}
	lines := make([]slashOutputLine, 0, capacity)
	for idx, row := range table {
		parts := make([]string, len(row))
		for i, col := range row {
			parts[i] = padRightRunes(col, widths[i])
		}
		isHeaderRow := hasHeader && idx == 0
		style := tuikit.LineStyleKeyValue
		if isHeaderRow {
			style = tuikit.LineStyleTableHeader
		}
		lines = append(lines, slashOutputLine{
			Text:  "  " + strings.TrimRight(strings.Join(parts, "  "), " "),
			Style: style,
		})
		if isHeaderRow {
			dividerParts := make([]string, len(row))
			for i := range row {
				dividerParts[i] = strings.Repeat("─", widths[i])
			}
			lines = append(lines, slashOutputLine{
				Text:  "  " + strings.Join(dividerParts, "  "),
				Style: tuikit.LineStyleTableDivider,
			})
		}
	}
	return lines
}

func slashField(label, value string) slashOutputLine {
	label = strings.TrimSpace(label)
	value = strings.TrimSpace(value)
	if label == "" {
		return slashOutputLine{Text: value, Style: tuikit.LineStyleDefault}
	}
	return slashOutputLine{Text: "  " + padRightRunes(label+":", 10) + " " + value, Style: tuikit.LineStyleKeyValue}
}

func slashSection(text string) slashOutputLine {
	return slashOutputLine{Text: strings.TrimSpace(text), Style: tuikit.LineStyleSection}
}

func slashBlank() slashOutputLine {
	return slashOutputLine{Style: tuikit.LineStyleDefault}
}
