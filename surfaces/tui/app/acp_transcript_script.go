package tuiapp

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func styleScriptSourcePreview(ctx BlockRenderContext, source string, budget, finalBudget int) string {
	source = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(source, "\r\n", "\n"), "\r", "\n"))
	lines := strings.Split(source, "\n")
	// Retain complete-source token context while applying the command preview's
	// whitespace normalization. Cutting a preview before lexing can turn a
	// truncated comment or string into a syntax error.
	budget = maxInt(compactSingleLineMinBudget, budget)
	styled := styleJavaScriptSource(ctx, source, true)
	if len(lines) > 1 {
		suffix := fmt.Sprintf(" ... +%d lines", len(lines)-1)
		styled = compactStyledScriptPreview(ctx, styled, maxInt(compactSingleLineMinBudget, budget-displayColumns(suffix))) + ctx.Theme.ToolOutputMetaStyle().Render(suffix)
	} else {
		styled = compactStyledScriptPreview(ctx, styled, budget)
	}
	return compactStyledScriptPreview(ctx, styled, finalBudget)
}

func compactStyledScriptPreview(ctx BlockRenderContext, styled string, budget int) string {
	plain := ansi.Strip(styled)
	preview := compactSingleLineHeader(plain, budget)
	if preview == plain {
		return styled
	}
	if budget <= 3 {
		return ansi.Cut(styled, 0, displayColumns(preview))
	}
	leftBudget := maxInt(1, (budget-3+1)/2)
	left := strings.TrimSpace(truncateDisplayCells(plain, leftBudget))
	right := strings.TrimSpace(truncateDisplayCellsFromEnd(plain, maxInt(1, budget-3-leftBudget)))
	result := ansi.Cut(styled, 0, displayColumns(left)) + ctx.Theme.ToolOutputMetaStyle().Render("...") + ansi.Cut(styled, displayColumns(plain)-displayColumns(right), displayColumns(plain))
	return ansi.Cut(result, 0, displayColumns(truncateDisplayCells(ansi.Strip(result), budget)))
}

func styleJavaScriptText(ctx BlockRenderContext, source string) string {
	return styleJavaScriptSource(ctx, source, false)
}

func styleJavaScriptSource(ctx BlockRenderContext, source string, inline bool) string {
	plain := source
	if inline {
		plain = strings.Join(strings.Fields(source), " ")
	}
	if ctx.Theme.NoColor {
		return plain
	}
	iterator, err := chroma.Coalesce(lexers.Get("javascript")).Tokenise(nil, source)
	if err != nil {
		return plain
	}
	theme := styles.Get(tuikit.SyntaxPaletteForTheme(ctx.Theme).ChromaTheme)
	var styled strings.Builder
	space := false
	for _, token := range iterator.Tokens() {
		style := chromaTokenStyle(ctx, theme.Get(token.Type))
		if inline {
			text := token.Value
			for text != "" {
				end := strings.IndexFunc(text, unicode.IsSpace)
				if end < 0 {
					end = len(text)
				}
				if end > 0 {
					if space {
						styled.WriteByte(' ')
					}
					styled.WriteString(style.Render(text[:end]))
					space = false
				}
				text = text[end:]
				if text != "" {
					space = styled.Len() > 0
					text = strings.TrimLeftFunc(text, unicode.IsSpace)
				}
			}
			continue
		}
		for i, line := range strings.Split(token.Value, "\n") {
			if i > 0 {
				styled.WriteByte('\n')
			}
			styled.WriteString(style.Render(line))
		}
	}
	// Lexers may normalize source. Presentation must retain the displayed bytes.
	if ansi.Strip(styled.String()) != plain {
		return plain
	}
	return styled.String()
}

func wrapACPScriptHeaderForViewport(prefix, source string, width int, ctx BlockRenderContext, tone acpHeaderMarkTone, dim bool) ([]string, []string, bool) {
	indent := displayColumns(prefix)
	if width <= indent+1 {
		styled := styleACPTranscriptHeaderWithMark(ctx, prefix+source, tone, dim)
		segments := strings.Split(hardWrapDisplayLine(styled, width), "\n")
		return deriveViewportPlainLines(nil, segments), segments, true
	}
	// Tokenize before wrapping so multiline comments and strings retain context.
	segments := wrapStyledACPTranscriptNarrativeBody(styleJavaScriptText(ctx, source), width-indent)
	plain := make([]string, 0, len(segments))
	styled := make([]string, 0, len(segments))
	for i, segment := range segments {
		linePrefix := strings.Repeat(" ", indent)
		styledPrefix := linePrefix
		if i == 0 {
			linePrefix = prefix
			styledPrefix = styleACPTranscriptHeaderWithMark(ctx, strings.TrimRight(prefix, " "), tone, dim) + " "
		}
		plain = append(plain, linePrefix+ansi.Strip(segment))
		styled = append(styled, styledPrefix+segment)
	}
	return plain, styled, true
}
