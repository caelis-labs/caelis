package tuiapp

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/surfaces/internal/promptview"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

type slashOutputLine struct {
	Text  string
	Style tuikit.LineStyle
	Plain bool
}

type slashOutputBlock struct {
	id        string
	lines     []slashOutputLine
	alignBody bool
}

func newSlashOutputBlock(lines []slashOutputLine, alignBody bool) *slashOutputBlock {
	out := make([]slashOutputLine, 0, len(lines))
	for _, line := range lines {
		out = append(out, slashOutputLine{
			Text:  strings.TrimRight(line.Text, " \t"),
			Style: line.Style,
			Plain: line.Plain,
		})
	}
	return &slashOutputBlock{id: nextBlockID(), lines: out, alignBody: alignBody}
}

func (b *slashOutputBlock) BlockID() string { return b.id }
func (b *slashOutputBlock) Kind() BlockKind { return BlockTranscript }
func (b *slashOutputBlock) Render(ctx BlockRenderContext) []RenderedRow {
	rows := make([]RenderedRow, 0, len(b.lines))
	var inset int
	if b.alignBody {
		_, bodyIndent := narrativeLinePrefixes(tuikit.LineStyleAssistant)
		inset = min(displayColumns(bodyIndent), max(0, ctx.Width-2))
	}
	pad := strings.Repeat(" ", inset)
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
		if inset > 0 {
			for _, segment := range splitStyledPhysicalLines(hardWrapDisplayLine(styled, ctx.Width-inset)) {
				row := StyledRow(b.id, pad+segment)
				row.PreWrapped = true
				row.selectionIndent = inset
				rows = append(rows, row)
			}
			continue
		}
		rows = append(rows, StyledPlainRow(b.id, plain, styled))
	}
	return rows
}

func (m *Model) handleSlashCommandResultMsg(msg SlashCommandResultMsg) (tea.Model, tea.Cmd) {
	return m.appendSlashOutputLines(renderSlashCommandResultLines(msg.Result), false)
}

func (m *Model) appendSlashOutputLines(lines []slashOutputLine, alignBody bool) (tea.Model, tea.Cmd) {
	if len(lines) == 0 {
		return m, nil
	}
	if m.hasCommittedLine {
		m.appendSlashOutputSpacer()
	}
	block := newSlashOutputBlock(lines, alignBody)
	m.appendMainTranscriptBlock(block)
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

func renderSlashCommandResultLines(result controlprompt.SlashCommandResult) []slashOutputLine {
	switch result.Kind {
	case controlprompt.SlashCommandResultStatus:
		return renderSlashStatusLines(result.Status)
	case controlprompt.SlashCommandResultHelp:
		return renderSlashHelpLines(result.Help)
	case controlprompt.SlashCommandResultTable:
		return renderSlashTableLines(result.Table)
	case controlprompt.SlashCommandResultDoctor:
		return renderSlashNoticeLines(SlashNoticeMsg{Text: promptview.FormatDoctorSnapshot(result.Status)})
	default:
		return []slashOutputLine{slashField("Slash", promptview.FormatSlashResult(result))}
	}
}

func renderSlashTableLines(table controlprompt.SlashTableSnapshot) []slashOutputLine {
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

func renderSlashStatusLines(status controlstatus.StatusSnapshot) []slashOutputLine {
	view := promptview.StatusDisplayFromSnapshot(status)
	lines := []slashOutputLine{slashSection("Status")}
	lines = append(lines, renderSlashFields(view.Fields)...)
	if len(view.Warnings) > 0 {
		lines = append(lines, slashBlank(), slashSection("Warnings"))
		for _, warning := range view.Warnings {
			lines = append(lines, slashField("Warning", warning))
		}
	}
	if !view.RateLimits.Empty() {
		lines = append(lines, slashBlank(), slashSection("Limits"))
		fields := make([]promptview.DisplayField, 0, len(view.RateLimits.Rows)+1)
		if view.RateLimits.Plan != "" {
			fields = append(fields, promptview.DisplayField{Label: "Plan", Value: view.RateLimits.Plan})
		}
		fields = append(fields, view.RateLimits.Rows...)
		lines = append(lines, renderSlashFields(fields)...)
	}
	if !view.Usage.Empty() {
		lines = append(lines, slashBlank(), slashSection("Usage"))
		lines = append(lines, renderSlashTokenUsage(view.Usage)...)
	}
	return lines
}

func renderSlashHelpLines(help controlprompt.CommandHelpSnapshot) []slashOutputLine {
	lines := []slashOutputLine{slashSection("Find commands and skills")}
	lines = append(lines, renderSlashPaddedRows([][]string{
		{"/", "Filter commands, agents, and skills by name"},
		{"↑/↓", "Move selection. Enter or click to apply"},
	})...)

	lines = append(lines, slashBlank(), slashSection("Work in a session"))
	session := [][]string{{"Ctrl+O", "Find and reopen a recent session"}}
	for _, example := range [][2]string{
		{"connect", "Add a model or agent before chatting"},
		{"model", "Use ←/→ when reasoning effort is available"},
	} {
		if slices.ContainsFunc(help.Items, func(item controlprompt.CommandHelpItem) bool {
			return item.Known && item.Name == example[0]
		}) {
			session = append(session, []string{"/" + example[0], example[1]})
		}
	}
	lines = append(lines, renderSlashPaddedRows(session)...)

	lines = append(lines, slashBlank(), slashSection("Compose and interrupt"))
	return append(lines, renderSlashPaddedRows([][]string{
		{"Enter", "Send the prompt"},
		{"Ctrl+J", "Insert a newline in your prompt"},
		{"Esc", "Close a picker or interrupt a turn"},
	})...)
}

func renderSlashTokenUsage(usage promptview.TokenUsageView) []slashOutputLine {
	table := [][]string{{"Scope", "Total", "Input", "Cache read", "Output"}}
	if usage.ShowCacheWrite {
		table[0] = append(table[0], "Cache write")
	}
	if usage.ShowReasoning {
		table[0] = append(table[0], "Reasoning")
	}
	for _, row := range usage.Rows {
		values := []string{row.Scope, row.Total, row.Input, row.Cached, row.Output}
		if usage.ShowCacheWrite {
			values = append(values, row.CacheWrite)
		}
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

func renderSlashFields(fields []promptview.DisplayField) []slashOutputLine {
	width := 10
	for _, field := range fields {
		width = max(width, len([]rune(strings.TrimSpace(field.Label)+":")))
	}
	lines := make([]slashOutputLine, 0, len(fields))
	for _, field := range fields {
		lines = append(lines, slashFieldWithWidth(field.Label, field.Value, width))
	}
	return lines
}

func slashField(label, value string) slashOutputLine {
	return slashFieldWithWidth(label, value, 10)
}

func slashFieldWithWidth(label, value string, width int) slashOutputLine {
	label = strings.TrimSpace(label)
	value = strings.TrimSpace(value)
	if label == "" {
		return slashOutputLine{Text: value, Style: tuikit.LineStyleDefault}
	}
	return slashOutputLine{Text: "  " + padRightRunes(label+":", width) + " " + value, Style: tuikit.LineStyleKeyValue}
}

func slashSection(text string) slashOutputLine {
	return slashOutputLine{Text: strings.TrimSpace(text), Style: tuikit.LineStyleSection}
}

func slashBlank() slashOutputLine {
	return slashOutputLine{Style: tuikit.LineStyleDefault}
}
