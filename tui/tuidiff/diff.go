package tuidiff

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"

	"github.com/OnslaughtSnail/caelis/tui/tuikit"
)

const (
	// SplitViewMinWidth is the minimum viewport width for side-by-side rendering.
	SplitViewMinWidth = 120
	// FoldContextLines is the number of unchanged lines to retain around each hunk.
	FoldContextLines = 2
)

// Payload is the source data used to build a structured diff model.
type Payload struct {
	Tool      string
	Path      string
	Created   bool
	Hunk      string
	Old       string
	New       string
	Preview   string
	Truncated bool
}

// RowKind represents one semantic diff row.
type RowKind int

const (
	RowContext RowKind = iota
	RowAdd
	RowRemove
	RowModified
	RowFold
)

// InlineKind represents one semantic inline span.
type InlineKind int

const (
	InlineUnchanged InlineKind = iota
	InlineAdd
	InlineRemove
)

// InlineSpan is one inline text segment with semantic kind.
type InlineSpan struct {
	Text string
	Kind InlineKind
}

// Row stores one aligned diff row.
type Row struct {
	Kind RowKind

	OldLineNo  int
	NewLineNo  int
	OldLineEnd int
	NewLineEnd int

	OldText string
	NewText string

	OldSpans []InlineSpan
	NewSpans []InlineSpan
}

// Model is the structured diff model used by renderers.
type Model struct {
	Tool      string
	Path      string
	Created   bool
	Hunk      string
	Preview   string
	Truncated bool

	Rows []Row

	OldLineCount int
	NewLineCount int
}

// BuildModel converts a payload into a line-aligned diff model.
func BuildModel(payload Payload) Model {
	oldLines := splitContentLines(payload.Old)
	newLines := splitContentLines(payload.New)
	rows := make([]Row, 0, minInt(len(oldLines)+len(newLines), richDiffMaxRowsHint(oldLines, newLines)))

	prefix, suffix := commonLineAffixCounts(oldLines, newLines)
	if len(oldLines) == len(newLines) && prefix == len(oldLines) {
		return Model{
			Tool:         payload.Tool,
			Path:         payload.Path,
			Created:      payload.Created,
			Hunk:         strings.TrimSpace(payload.Hunk),
			Preview:      payload.Preview,
			Truncated:    payload.Truncated,
			Rows:         nil,
			OldLineCount: len(oldLines),
			NewLineCount: len(newLines),
		}
	}

	rows = append(rows, buildSharedPrefixRows(oldLines, newLines, prefix)...)

	oldCoreEnd := len(oldLines) - suffix
	newCoreEnd := len(newLines) - suffix
	coreRows, _, _ := buildDiffRows(oldLines[prefix:oldCoreEnd], newLines[prefix:newCoreEnd], prefix, prefix)
	rows = append(rows, foldRows(coreRows, FoldContextLines)...)
	rows = append(rows, buildSharedSuffixRows(oldLines, newLines, oldCoreEnd, newCoreEnd, suffix)...)

	return Model{
		Tool:         payload.Tool,
		Path:         payload.Path,
		Created:      payload.Created,
		Hunk:         strings.TrimSpace(payload.Hunk),
		Preview:      payload.Preview,
		Truncated:    payload.Truncated,
		Rows:         rows,
		OldLineCount: len(oldLines),
		NewLineCount: len(newLines),
	}
}

func richDiffMaxRowsHint(oldLines, newLines []string) int {
	const extraRows = 16
	return (FoldContextLines * 4) + len(oldLines) + len(newLines) + extraRows
}

func commonLineAffixCounts(oldLines, newLines []string) (int, int) {
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}

	suffix := 0
	for prefix+suffix < len(oldLines) &&
		prefix+suffix < len(newLines) &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	return prefix, suffix
}

func buildSharedPrefixRows(oldLines, newLines []string, prefix int) []Row {
	if prefix <= 0 {
		return nil
	}
	rows := make([]Row, 0, minInt(prefix, FoldContextLines)+1)
	if prefix > FoldContextLines {
		rows = append(rows, Row{
			Kind:       RowFold,
			OldLineNo:  1,
			OldLineEnd: prefix - FoldContextLines,
			NewLineNo:  1,
			NewLineEnd: prefix - FoldContextLines,
		})
	}
	start := maxInt(0, prefix-FoldContextLines)
	for idx := start; idx < prefix; idx++ {
		rows = append(rows, sharedContextRow(oldLines[idx], newLines[idx], idx+1, idx+1))
	}
	return rows
}

func buildSharedSuffixRows(oldLines, newLines []string, oldStart, newStart, suffix int) []Row {
	if suffix <= 0 {
		return nil
	}
	show := minInt(FoldContextLines, suffix)
	rows := make([]Row, 0, show+1)
	for offset := 0; offset < show; offset++ {
		oldIdx := oldStart + offset
		newIdx := newStart + offset
		rows = append(rows, sharedContextRow(oldLines[oldIdx], newLines[newIdx], oldIdx+1, newIdx+1))
	}
	if suffix > show {
		rows = append(rows, Row{
			Kind:       RowFold,
			OldLineNo:  oldStart + show + 1,
			OldLineEnd: len(oldLines),
			NewLineNo:  newStart + show + 1,
			NewLineEnd: len(newLines),
		})
	}
	return rows
}

func sharedContextRow(oldText, newText string, oldLineNo, newLineNo int) Row {
	return Row{
		Kind:      RowContext,
		OldLineNo: oldLineNo,
		NewLineNo: newLineNo,
		OldText:   oldText,
		NewText:   newText,
		OldSpans:  []InlineSpan{{Text: oldText, Kind: InlineUnchanged}},
		NewSpans:  []InlineSpan{{Text: newText, Kind: InlineUnchanged}},
	}
}

func buildDiffRows(oldLines, newLines []string, oldBase, newBase int) ([]Row, int, int) {
	rows := make([]Row, 0, len(oldLines)+len(newLines)+4)
	pairs := lcsLinePairs(oldLines, newLines)
	oldCursor := 0
	newCursor := 0
	oldNo := oldBase
	newNo := newBase

	for _, one := range pairs {
		segOld := oldLines[oldCursor:one.a]
		segNew := newLines[newCursor:one.b]
		changed, nextOldNo, nextNewNo := buildChangedRows(segOld, segNew, oldNo, newNo)
		rows = append(rows, changed...)
		oldNo = nextOldNo
		newNo = nextNewNo

		oldNo++
		newNo++
		rows = append(rows, sharedContextRow(oldLines[one.a], newLines[one.b], oldNo, newNo))

		oldCursor = one.a + 1
		newCursor = one.b + 1
	}

	tailRows, tailOldNo, tailNewNo := buildChangedRows(oldLines[oldCursor:], newLines[newCursor:], oldNo, newNo)
	rows = append(rows, tailRows...)
	return rows, tailOldNo, tailNewNo
}

// Render renders a diff model to TUI lines with adaptive layout.
func Render(model Model, width int, theme tuikit.Theme) []string {
	if width < 40 {
		width = 40
	}
	out := make([]string, 0, len(model.Rows)+8)
	out = append(out, renderHeader(model, theme))
	if model.Hunk != "" {
		out = append(out, "  "+theme.DiffHunkStyle().Render(model.Hunk))
	}
	if len(model.Rows) == 0 {
		out = append(out, "  "+theme.NoteStyle().Render("(no diff lines)"))
		return out
	}
	out = append(out, theme.DiffPanelBorderStyle().Render(strings.Repeat("─", width)))
	if width >= SplitViewMinWidth && !isAddOnlyModel(model) {
		out = append(out, renderSplit(model.Rows, width, theme)...)
	} else {
		out = append(out, renderUnified(model.Rows, width, theme)...)
	}
	return out
}

func renderHeader(model Model, theme tuikit.Theme) string {
	tool := strings.ToUpper(strings.TrimSpace(model.Tool))
	if tool == "" {
		tool = "PATCH"
	}
	target := strings.TrimSpace(model.Path)
	if target == "" {
		target = "(unknown file)"
	}
	action := "edited"
	if model.Created {
		action = "created"
	}
	return theme.ToolStyle().Render("✓ ") + theme.ToolNameStyle().Render(tool) + " " + action + " " + tuikit.LinkifyText(target, theme.LinkStyle())
}

func isAddOnlyModel(model Model) bool {
	if len(model.Rows) == 0 {
		return false
	}
	for _, row := range model.Rows {
		if row.Kind != RowAdd {
			return false
		}
	}
	return true
}

func renderUnified(rows []Row, width int, theme tuikit.Theme) []string {
	lineNoWidth := maxLineNoWidth(rows)
	out := make([]string, 0, len(rows)+4)
	for _, row := range rows {
		switch row.Kind {
		case RowFold:
			out = append(out, renderFoldRow(row, width, theme))
		case RowContext:
			no := row.NewLineNo
			if no <= 0 {
				no = row.OldLineNo
			}
			spans := row.NewSpans
			if len(spans) == 0 {
				spans = row.OldSpans
			}
			out = append(out, renderUnifiedRow(no, ' ', spans, RowContext, lineNoWidth, width, theme))
		case RowRemove:
			out = append(out, renderUnifiedRow(row.OldLineNo, '-', row.OldSpans, RowRemove, lineNoWidth, width, theme))
		case RowAdd:
			out = append(out, renderUnifiedRow(row.NewLineNo, '+', row.NewSpans, RowAdd, lineNoWidth, width, theme))
		case RowModified:
			out = append(out,
				renderUnifiedRow(row.OldLineNo, '-', row.OldSpans, RowRemove, lineNoWidth, width, theme),
				renderUnifiedRow(row.NewLineNo, '+', row.NewSpans, RowAdd, lineNoWidth, width, theme),
			)
		}
	}
	return out
}

func renderUnifiedRow(lineNo int, marker rune, spans []InlineSpan, kind RowKind, lineNoWidth, totalWidth int, theme tuikit.Theme) string {
	lineNoText := ""
	if lineNo > 0 {
		lineNoText = strconv.Itoa(lineNo)
	}
	plainPrefix := fmt.Sprintf(" %*s %c ", lineNoWidth, lineNoText, marker)
	prefixWidth := displayColumns(plainPrefix)
	contentWidth := maxInt(0, totalWidth-prefixWidth)
	styledPrefix := theme.DiffLineNoStyle().Render(fmt.Sprintf(" %*s ", lineNoWidth, lineNoText)) +
		theme.DiffGutterStyle().Render(fmt.Sprintf("%c ", marker))
	return styledPrefix + renderContent(spans, kind, contentWidth, theme)
}

func renderSplit(rows []Row, width int, theme tuikit.Theme) []string {
	lineNoWidth := maxLineNoWidth(rows)
	if width < SplitViewMinWidth {
		return renderUnified(rows, width, theme)
	}
	sepPlain := " │ "
	sepStyled := " " + theme.DiffPanelBorderStyle().Render("│") + " "
	sepWidth := displayColumns(sepPlain)
	leftWidth := maxInt(20, (width-sepWidth)/2)
	rightWidth := maxInt(20, width-sepWidth-leftWidth)

	out := make([]string, 0, len(rows)+4)
	for _, row := range rows {
		switch row.Kind {
		case RowFold:
			out = append(out, renderFoldRow(row, width, theme))
		case RowContext:
			left := renderSplitCell(row.OldLineNo, ' ', row.OldSpans, RowContext, lineNoWidth, leftWidth, theme)
			right := renderSplitCell(row.NewLineNo, ' ', row.NewSpans, RowContext, lineNoWidth, rightWidth, theme)
			out = append(out, left+sepStyled+right)
		case RowRemove:
			left := renderSplitCell(row.OldLineNo, '-', row.OldSpans, RowRemove, lineNoWidth, leftWidth, theme)
			right := renderSplitCell(0, ' ', nil, RowContext, lineNoWidth, rightWidth, theme)
			out = append(out, left+sepStyled+right)
		case RowAdd:
			left := renderSplitCell(0, ' ', nil, RowContext, lineNoWidth, leftWidth, theme)
			right := renderSplitCell(row.NewLineNo, '+', row.NewSpans, RowAdd, lineNoWidth, rightWidth, theme)
			out = append(out, left+sepStyled+right)
		case RowModified:
			left := renderSplitCell(row.OldLineNo, '-', row.OldSpans, RowRemove, lineNoWidth, leftWidth, theme)
			right := renderSplitCell(row.NewLineNo, '+', row.NewSpans, RowAdd, lineNoWidth, rightWidth, theme)
			out = append(out, left+sepStyled+right)
		}
	}
	return out
}

func renderFoldRow(row Row, width int, theme tuikit.Theme) string {
	text := strings.TrimSpace(foldNote(row))
	if text == "" {
		text = "..."
	}
	if width <= 0 {
		return theme.DiffHunkStyle().Render(text)
	}
	if displayColumns(text) > width {
		text = sliceByDisplayColumns(text, 0, maxInt(1, width-1)) + "…"
	}
	return theme.DiffHunkStyle().Render(text)
}

func renderSplitCell(lineNo int, marker rune, spans []InlineSpan, kind RowKind, lineNoWidth, cellWidth int, theme tuikit.Theme) string {
	lineNoText := ""
	if lineNo > 0 {
		lineNoText = strconv.Itoa(lineNo)
	}
	plainPrefix := fmt.Sprintf("%*s %c ", lineNoWidth, lineNoText, marker)
	prefixWidth := displayColumns(plainPrefix)
	contentWidth := maxInt(0, cellWidth-prefixWidth)
	styledPrefix := theme.DiffLineNoStyle().Render(fmt.Sprintf("%*s ", lineNoWidth, lineNoText)) +
		theme.DiffGutterStyle().Render(fmt.Sprintf("%c ", marker))
	return styledPrefix + renderContent(spans, kind, contentWidth, theme)
}

func renderContent(spans []InlineSpan, kind RowKind, width int, theme tuikit.Theme) string {
	if width <= 0 {
		return ""
	}
	clipped, used := clipSpans(spans, width)
	pad := width - used
	if pad < 0 {
		pad = 0
	}
	if kind == RowContext {
		plain := spansText(clipped)
		if pad > 0 {
			plain += strings.Repeat(" ", pad)
		}
		return plain
	}

	base, strong := diffStylesForKind(kind, theme)
	if len(clipped) == 0 {
		return base.Render(strings.Repeat(" ", width))
	}

	var b strings.Builder
	for _, span := range clipped {
		if span.Text == "" {
			continue
		}
		s := base
		switch kind {
		case RowAdd:
			if span.Kind == InlineAdd {
				s = strong
			}
		case RowRemove:
			if span.Kind == InlineRemove {
				s = strong
			}
		}
		b.WriteString(s.Render(span.Text))
	}
	if pad > 0 {
		b.WriteString(base.Render(strings.Repeat(" ", pad)))
	}
	return b.String()
}

func diffStylesForKind(kind RowKind, theme tuikit.Theme) (lipgloss.Style, lipgloss.Style) {
	switch kind {
	case RowAdd:
		base := lipgloss.NewStyle().Foreground(theme.TextPrimary).Background(theme.DiffAddBg)
		strong := lipgloss.NewStyle().Foreground(theme.TextPrimary).Background(theme.DiffAddStrongBg)
		return base, strong
	case RowRemove:
		base := lipgloss.NewStyle().Foreground(theme.TextPrimary).Background(theme.DiffRemoveBg)
		strong := lipgloss.NewStyle().Foreground(theme.TextPrimary).Background(theme.DiffRemoveStrongBg)
		return base, strong
	default:
		empty := lipgloss.NewStyle()
		return empty, empty
	}
}

func spansText(spans []InlineSpan) string {
	if len(spans) == 0 {
		return ""
	}
	var b strings.Builder
	for _, one := range spans {
		b.WriteString(one.Text)
	}
	return b.String()
}

func clipSpans(spans []InlineSpan, width int) ([]InlineSpan, int) {
	if width <= 0 || len(spans) == 0 {
		return nil, 0
	}
	out := make([]InlineSpan, 0, len(spans))
	used := 0
	for _, span := range spans {
		if used >= width {
			break
		}
		text := strings.ReplaceAll(span.Text, "\t", "    ")
		if text == "" {
			continue
		}
		var sb strings.Builder
		for _, r := range text {
			rw := runeDisplayWidth(r)
			if used+rw > width {
				break
			}
			sb.WriteRune(r)
			used += rw
		}
		if sb.Len() > 0 {
			appendInlineSpan(&out, InlineSpan{Text: sb.String(), Kind: span.Kind})
		}
	}
	return out, used
}

func maxLineNoWidth(rows []Row) int {
	maxNo := 1
	for _, row := range rows {
		if row.OldLineNo > maxNo {
			maxNo = row.OldLineNo
		}
		if row.NewLineNo > maxNo {
			maxNo = row.NewLineNo
		}
	}
	return len(strconv.Itoa(maxNo))
}

func splitContentLines(text string) []string {
	if text == "" {
		return nil
	}
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	normalized = strings.TrimSuffix(normalized, "\n")
	return strings.Split(normalized, "\n")
}

func foldRows(rows []Row, contextLines int) []Row {
	if len(rows) == 0 {
		return nil
	}
	if contextLines < 0 {
		contextLines = 0
	}
	keep := make([]bool, len(rows))
	for i, row := range rows {
		if row.Kind == RowContext {
			continue
		}
		start := maxInt(0, i-contextLines)
		end := minInt(len(rows)-1, i+contextLines)
		for j := start; j <= end; j++ {
			keep[j] = true
		}
	}
	out := make([]Row, 0, len(rows))
	for i := 0; i < len(rows); {
		if keep[i] {
			out = append(out, rows[i])
			i++
			continue
		}
		start := i
		for i < len(rows) && !keep[i] {
			i++
		}
		out = append(out, Row{
			Kind:       RowFold,
			OldLineNo:  firstNonZeroOldLine(rows[start:i]),
			NewLineNo:  firstNonZeroNewLine(rows[start:i]),
			OldLineEnd: lastNonZeroOldLine(rows[start:i]),
			NewLineEnd: lastNonZeroNewLine(rows[start:i]),
		})
	}
	return out
}

func foldNote(row Row) string {
	oldCount := foldRangeCount(row.OldLineNo, row.OldLineEnd)
	newCount := foldRangeCount(row.NewLineNo, row.NewLineEnd)
	omitted := maxInt(oldCount, newCount)
	if omitted <= 0 {
		return "@@ ..."
	}
	unchangedLabel := fmt.Sprintf("%d unchanged lines", omitted)
	if omitted == 1 {
		unchangedLabel = "1 unchanged line"
	}
	return fmt.Sprintf(
		"@@ -%s +%s @@ ... %s ...",
		formatFoldRange(row.OldLineNo, row.OldLineEnd),
		formatFoldRange(row.NewLineNo, row.NewLineEnd),
		unchangedLabel,
	)
}

func formatFoldRange(start, end int) string {
	count := foldRangeCount(start, end)
	if count <= 0 {
		return "0,0"
	}
	return fmt.Sprintf("%d,%d", start, count)
}

func foldRangeCount(start, end int) int {
	if start <= 0 || end < start {
		return 0
	}
	return end - start + 1
}

func firstNonZeroOldLine(rows []Row) int {
	for _, row := range rows {
		if row.OldLineNo > 0 {
			return row.OldLineNo
		}
	}
	return 0
}

func lastNonZeroOldLine(rows []Row) int {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].OldLineNo > 0 {
			return rows[i].OldLineNo
		}
	}
	return 0
}

func firstNonZeroNewLine(rows []Row) int {
	for _, row := range rows {
		if row.NewLineNo > 0 {
			return row.NewLineNo
		}
	}
	return 0
}

func lastNonZeroNewLine(rows []Row) int {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].NewLineNo > 0 {
			return rows[i].NewLineNo
		}
	}
	return 0
}

func buildChangedRows(oldLines, newLines []string, oldNo, newNo int) ([]Row, int, int) {
	out := make([]Row, 0, len(oldLines)+len(newLines)+2)
	pairCount := minInt(len(oldLines), len(newLines))
	for i := 0; i < pairCount; i++ {
		oldNo++
		newNo++
		oldSpans, newSpans := diffInlineSpans(oldLines[i], newLines[i])
		out = append(out, Row{
			Kind:      RowModified,
			OldLineNo: oldNo,
			NewLineNo: newNo,
			OldText:   oldLines[i],
			NewText:   newLines[i],
			OldSpans:  oldSpans,
			NewSpans:  newSpans,
		})
	}
	for i := pairCount; i < len(oldLines); i++ {
		oldNo++
		out = append(out, Row{
			Kind:      RowRemove,
			OldLineNo: oldNo,
			OldText:   oldLines[i],
			OldSpans:  []InlineSpan{{Text: oldLines[i], Kind: InlineRemove}},
		})
	}
	for i := pairCount; i < len(newLines); i++ {
		newNo++
		out = append(out, Row{
			Kind:      RowAdd,
			NewLineNo: newNo,
			NewText:   newLines[i],
			NewSpans:  []InlineSpan{{Text: newLines[i], Kind: InlineAdd}},
		})
	}
	return out, oldNo, newNo
}

func diffInlineSpans(oldText, newText string) ([]InlineSpan, []InlineSpan) {
	if oldText == newText {
		return []InlineSpan{{Text: oldText, Kind: InlineUnchanged}}, []InlineSpan{{Text: newText, Kind: InlineUnchanged}}
	}
	oldRunes := []rune(oldText)
	newRunes := []rune(newText)
	pairs := lcsRunePairs(oldRunes, newRunes)
	oldSpans := buildOldSpans(oldRunes, pairs)
	newSpans := buildNewSpans(newRunes, pairs)
	if len(oldSpans) == 0 {
		oldSpans = []InlineSpan{{Text: oldText, Kind: InlineRemove}}
	}
	if len(newSpans) == 0 {
		newSpans = []InlineSpan{{Text: newText, Kind: InlineAdd}}
	}
	return oldSpans, newSpans
}

func buildOldSpans(runes []rune, pairs []indexPair) []InlineSpan {
	out := make([]InlineSpan, 0, len(runes))
	cursor := 0
	for _, one := range pairs {
		if one.a > cursor {
			appendInlineSpan(&out, InlineSpan{Text: string(runes[cursor:one.a]), Kind: InlineRemove})
		}
		appendInlineSpan(&out, InlineSpan{Text: string(runes[one.a : one.a+1]), Kind: InlineUnchanged})
		cursor = one.a + 1
	}
	if cursor < len(runes) {
		appendInlineSpan(&out, InlineSpan{Text: string(runes[cursor:]), Kind: InlineRemove})
	}
	return out
}

func buildNewSpans(runes []rune, pairs []indexPair) []InlineSpan {
	out := make([]InlineSpan, 0, len(runes))
	cursor := 0
	for _, one := range pairs {
		if one.b > cursor {
			appendInlineSpan(&out, InlineSpan{Text: string(runes[cursor:one.b]), Kind: InlineAdd})
		}
		appendInlineSpan(&out, InlineSpan{Text: string(runes[one.b : one.b+1]), Kind: InlineUnchanged})
		cursor = one.b + 1
	}
	if cursor < len(runes) {
		appendInlineSpan(&out, InlineSpan{Text: string(runes[cursor:]), Kind: InlineAdd})
	}
	return out
}

type indexPair struct {
	a int
	b int
}

func lcsLinePairs(oldLines, newLines []string) []indexPair {
	n := len(oldLines)
	m := len(newLines)
	if n == 0 || m == 0 {
		return nil
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = maxInt(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	pairs := make([]indexPair, 0, dp[0][0])
	i, j := 0, 0
	for i < n && j < m {
		if oldLines[i] == newLines[j] {
			pairs = append(pairs, indexPair{a: i, b: j})
			i++
			j++
			continue
		}
		if dp[i+1][j] >= dp[i][j+1] {
			i++
		} else {
			j++
		}
	}
	return pairs
}

func lcsRunePairs(oldRunes, newRunes []rune) []indexPair {
	n := len(oldRunes)
	m := len(newRunes)
	if n == 0 || m == 0 {
		return nil
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldRunes[i] == newRunes[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = maxInt(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	pairs := make([]indexPair, 0, dp[0][0])
	i, j := 0, 0
	for i < n && j < m {
		if oldRunes[i] == newRunes[j] {
			pairs = append(pairs, indexPair{a: i, b: j})
			i++
			j++
			continue
		}
		if dp[i+1][j] >= dp[i][j+1] {
			i++
		} else {
			j++
		}
	}
	return pairs
}

func appendInlineSpan(dst *[]InlineSpan, span InlineSpan) {
	if span.Text == "" {
		return
	}
	if len(*dst) == 0 {
		*dst = append(*dst, span)
		return
	}
	last := &(*dst)[len(*dst)-1]
	if last.Kind == span.Kind {
		last.Text += span.Text
		return
	}
	*dst = append(*dst, span)
}

func displayColumns(text string) int {
	return runewidth.StringWidth(text)
}

func sliceByDisplayColumns(s string, start int, end int) string {
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if s == "" || start == end {
		return ""
	}
	var b strings.Builder
	col := 0
	prevIncluded := false
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if w < 0 {
			w = 0
		}
		if w == 0 {
			if prevIncluded {
				b.WriteRune(r)
			}
			continue
		}
		if col >= end {
			break
		}
		include := col >= start && col < end
		if include {
			b.WriteRune(r)
		}
		prevIncluded = include
		col += w
	}
	return b.String()
}

func runeDisplayWidth(r rune) int {
	w := runewidth.RuneWidth(r)
	if w < 0 {
		return 0
	}
	return w
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
