package tuiapp

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/aymanbagabas/go-udiff/lcs"
	"github.com/charmbracelet/colorprofile"
	"github.com/rivo/uniseg"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

type diffTextSpan struct{ start, end int }

func markDiffTextChanges(old, next *diffPanelLine) {
	oldParts, oldOffsets := diffGraphemes(old.Text)
	newParts, newOffsets := diffGraphemes(next.Text)
	for _, edit := range lcs.DiffLines(oldParts, newParts) {
		if edit.Start < edit.End {
			old.Changed = append(old.Changed, diffTextSpan{oldOffsets[edit.Start], oldOffsets[edit.End]})
		}
		if edit.ReplStart < edit.ReplEnd {
			next.Changed = append(next.Changed, diffTextSpan{newOffsets[edit.ReplStart], newOffsets[edit.ReplEnd]})
		}
	}
}

func diffGraphemes(text string) ([]string, []int) {
	var parts []string
	var offsets []int
	graphemes := uniseg.NewGraphemes(text)
	for graphemes.Next() {
		start, _ := graphemes.Positions()
		parts = append(parts, graphemes.Str())
		offsets = append(offsets, start)
	}
	return parts, append(offsets, len(text))
}

func highlightDiffPanelLines(lines []diffPanelLine, ctx BlockRenderContext) {
	// Lex each side of a hunk as a unit so multiline strings and comments retain
	// their state. Omitted context between hunks is never invented.
	for start := 0; start < len(lines); {
		if lines[start].Kind == diffPanelLineMeta {
			start++
			continue
		}
		end := start
		var old, next []*diffPanelLine
		for end < len(lines) && lines[end].Kind != diffPanelLineMeta {
			line := &lines[end]
			if line.Kind != diffPanelLineAdd {
				old = append(old, line)
			}
			if line.Kind != diffPanelLineRemove {
				next = append(next, line)
			}
			end++
		}
		highlightDiffSide(old, ctx)
		highlightDiffSide(next, ctx)
		start = end
	}
}

func highlightDiffSide(lines []*diffPanelLine, ctx BlockRenderContext) {
	if len(lines) == 0 {
		return
	}
	sourceLines := make([]string, len(lines))
	for i, line := range lines {
		sourceLines[i] = line.Text
	}
	source := strings.Join(sourceLines, "\n") + "\n"
	lexer := lexers.Match(lines[0].Path)
	if lexer == nil && len(source) < 256*1024 {
		lexer = lexers.Analyse(source)
	}
	if lexer == nil || ctx.Theme.NoColor {
		for _, line := range lines {
			line.StyledText = renderDiffToken(line.Text, 0, ctx.Theme.TextStyle(), line, ctx)
		}
		return
	}
	iterator, err := chroma.Coalesce(lexer).Tokenise(nil, source)
	if err != nil {
		return
	}
	tokens := iterator.Tokens()
	var reconstructed strings.Builder
	for _, token := range tokens {
		reconstructed.WriteString(token.Value)
	}
	// Some lexers normalize whitespace. Source text is authoritative for copy and
	// line alignment, so highlighting must never alter it.
	if reconstructed.String() != source {
		return
	}
	theme := styles.Get(tuikit.SyntaxPaletteForTheme(ctx.Theme).ChromaTheme)
	for _, line := range lines {
		line.StyledText = ""
	}
	row, offset := 0, 0
	for _, token := range tokens {
		entry := theme.Get(token.Type)
		style := ctx.Theme.TextStyle()
		if entry.Colour.IsSet() {
			profile := ctx.Theme.Profile
			if profile == colorprofile.Unknown {
				profile = colorprofile.TrueColor
			}
			style = style.Foreground(profile.Convert(lipgloss.Color(entry.Colour.String())))
		}
		style = style.Bold(entry.Bold == chroma.Yes).Italic(entry.Italic == chroma.Yes).Underline(entry.Underline == chroma.Yes)
		parts := strings.Split(token.Value, "\n")
		for i, part := range parts {
			if row < len(lines) {
				lines[row].StyledText += renderDiffToken(part, offset, style, lines[row], ctx)
				offset += len(part)
			}
			if i < len(parts)-1 {
				row++
				offset = 0
			}
		}
	}
}

func renderDiffToken(text string, offset int, style lipgloss.Style, line *diffPanelLine, ctx BlockRenderContext) string {
	// Syntax palettes are authored on the base surface, not the red/green
	// overlays. Keep their roles while checking the background actually painted.
	foreground := style.GetForeground()
	background, _ := diffPanelLineStyle(line.Kind, ctx)
	style = style.Foreground(ctx.Theme.ReadableTextColor(foreground, background.GetBackground()))
	var result strings.Builder
	cursor, end := offset, offset+len(text)
	for _, span := range line.Changed {
		start, stop := max(cursor, span.start), min(end, span.end)
		if start >= stop {
			continue
		}
		result.WriteString(style.Render(text[cursor-offset : start-offset]))
		changed := style.Bold(true)
		if ctx.Theme.NoColor || ctx.Theme.Profile < colorprofile.ANSI {
			changed = changed.Underline(true)
		} else if line.Kind == diffPanelLineAdd {
			changed = changed.Background(ctx.Theme.DiffAddStrongBg).
				Foreground(ctx.Theme.ReadableTextColor(foreground, ctx.Theme.DiffAddStrongBg))
		} else {
			changed = changed.Background(ctx.Theme.DiffRemoveStrongBg).
				Foreground(ctx.Theme.ReadableTextColor(foreground, ctx.Theme.DiffRemoveStrongBg))
		}
		result.WriteString(changed.Render(text[start-offset : stop-offset]))
		cursor = stop
	}
	result.WriteString(style.Render(text[cursor-offset:]))
	return result.String()
}

// Match related replacement lines before pairing columns. Inserting a comment
// above a changed return statement must not pair that comment with the old return.
func alignDiffChanges(old, next []*diffPanelLine) []diffPanelPair {
	pairs := make([]diffPanelPair, 0, max(len(old), len(next)))
	const maxAlignmentCells = 4096
	if len(old) > 0 && len(next) > maxAlignmentCells/len(old) {
		for i := range max(len(old), len(next)) {
			pair := diffPanelPair{}
			if i < len(old) {
				pair.old = old[i]
			}
			if i < len(next) {
				pair.new = next[i]
			}
			pairs = append(pairs, pair)
		}
		return pairs
	}
	cols := len(next) + 1
	scores := make([]float64, (len(old)+1)*cols)
	similarity := make([]float64, len(old)*len(next))
	for i := len(old) - 1; i >= 0; i-- {
		for j := len(next) - 1; j >= 0; j-- {
			score := diffLineSimilarity(old[i].Text, next[j].Text)
			similarity[i*len(next)+j] = score
			best := max(scores[(i+1)*cols+j], scores[i*cols+j+1])
			if score >= .5 {
				best = max(best, score+scores[(i+1)*cols+j+1])
			}
			scores[i*cols+j] = best
		}
	}
	for i, j := 0, 0; i < len(old) || j < len(next); {
		switch {
		case i < len(old) && j < len(next) && similarity[i*len(next)+j] >= .5 && scores[i*cols+j] == similarity[i*len(next)+j]+scores[(i+1)*cols+j+1]:
			pairs = append(pairs, diffPanelPair{old: old[i], new: next[j]})
			i++
			j++
		case i < len(old) && (j == len(next) || scores[(i+1)*cols+j] >= scores[i*cols+j+1]):
			pairs = append(pairs, diffPanelPair{old: old[i]})
			i++
		default:
			pairs = append(pairs, diffPanelPair{new: next[j]})
			j++
		}
	}
	return pairs
}

func diffLineSimilarity(old, next string) float64 {
	old = strings.Join(strings.Fields(old), " ")
	next = strings.Join(strings.Fields(next), " ")
	if old == next {
		return 1
	}
	a, _ := diffGraphemes(old)
	b, _ := diffGraphemes(next)
	// Similarity is only a display heuristic; cap very long source lines without
	// truncating their actual rendered text or their intraline diff.
	a = a[:min(len(a), 256)]
	b = b[:min(len(b), 256)]
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	common := len(a)
	for _, edit := range lcs.DiffLines(a, b) {
		common -= edit.End - edit.Start
	}
	return 2 * float64(common) / float64(len(a)+len(b))
}
