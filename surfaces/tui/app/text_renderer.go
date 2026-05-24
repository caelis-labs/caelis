package tuiapp

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/x/ansi"
)

type TextKind int

const (
	TextAssistant TextKind = iota
	TextReasoning
	TextUser
	TextLog
	TextToolHeader
	TextToolDetail
	TextShellCommand
	TextTerminalOutput
	TextDiff
)

type RenderMode int

const (
	RenderFinal RenderMode = iota
	RenderStream
	RenderInlineOnly
	RenderPlain
)

type MarkdownPolicy int

const (
	MarkdownNone MarkdownPolicy = iota
	MarkdownInline
	MarkdownFull
	MarkdownStableTail
)

type TextRenderRequest struct {
	Kind           TextKind
	Mode           RenderMode
	MarkdownPolicy MarkdownPolicy
	Raw            string
	Prefix         string
	Width          int
	BlockID        string
	ClickToken     string
	Theme          tuikit.Theme
	ThemeKey       string
	StreamKey      string

	// LineStyle lets legacy callers declare the existing transcript style while
	// they migrate to semantic TextKind values.
	LineStyle tuikit.LineStyle

	// StablePrefixRaw and TailRaw are optional fast-path inputs for active
	// streams that have already split stable markdown from the unstable tail.
	StablePrefixRaw string
	TailRaw         string

	ObserveGlamourRender  func()
	ObserveInlineMarkdown func()
}

type TextRenderResult struct {
	Rows         []RenderedRow
	GlamourCalls int
	InlineCalls  int
	CacheHit     bool
}

func RenderTextWithContext(ctx BlockRenderContext, req TextRenderRequest) TextRenderResult {
	if req.Width <= 0 {
		req.Width = ctx.Width
	}
	req.Theme = ctx.Theme
	req.ThemeKey = ctx.renderThemeKey()
	req.ObserveGlamourRender = ctx.ObserveGlamourRender
	req.ObserveInlineMarkdown = ctx.ObserveInlineMarkdown
	return RenderText(req)
}

func (m *Model) renderText(req TextRenderRequest) TextRenderResult {
	if m == nil {
		return RenderText(req)
	}
	if req.Width <= 0 {
		req.Width = maxInt(1, m.viewport.Width())
	}
	req.Theme = m.theme
	req.ThemeKey = m.cachedThemeRenderKey()
	req.ObserveGlamourRender = m.observeGlamourRender
	req.ObserveInlineMarkdown = m.observeInlineMarkdownRender
	return RenderText(req)
}

func RenderText(req TextRenderRequest) TextRenderResult {
	req.Raw = normalizeTextRenderRaw(req.Raw)
	req.StablePrefixRaw = normalizeTextRenderRaw(req.StablePrefixRaw)
	req.TailRaw = normalizeTextRenderRaw(req.TailRaw)
	if req.Width <= 0 {
		req.Width = 1
	}
	if req.LineStyle == tuikit.LineStyleDefault {
		req.LineStyle = textKindLineStyle(req.Kind)
	}
	policy := normalizeTextMarkdownPolicy(req)

	var result TextRenderResult
	switch req.Kind {
	case TextAssistant:
		result = renderAssistantText(req, policy)
	case TextReasoning:
		result.Rows = renderPlainReasoningRows(req.BlockID, req.Raw, req.Prefix, req.Width, req.Theme)
	case TextUser:
		result.Rows = renderPlainUserRows(req.BlockID, req.Raw, req.Prefix, req.Width, req.Theme)
	case TextLog, TextToolHeader, TextToolDetail, TextShellCommand, TextTerminalOutput, TextDiff:
		result.Rows = renderPlainStructuralText(req)
	default:
		result.Rows = renderPlainStructuralText(req)
	}
	if req.ClickToken != "" {
		result.Rows = applyTextRenderClickToken(result.Rows, req.ClickToken)
	}
	return result
}

func renderAssistantText(req TextRenderRequest, policy MarkdownPolicy) TextRenderResult {
	switch {
	case req.Mode == RenderPlain || policy == MarkdownNone:
		return TextRenderResult{Rows: renderPlainStructuralText(req)}
	case req.Mode == RenderInlineOnly || policy == MarkdownInline:
		rows, inlineCalls := renderInlineMarkdownTextRows(req)
		return TextRenderResult{Rows: rows, InlineCalls: inlineCalls}
	case req.Mode == RenderStream || policy == MarkdownStableTail:
		rows, glamourCalls, cacheHit := renderAssistantStableTailRows(req)
		if len(rows) > 0 {
			return TextRenderResult{Rows: rows, GlamourCalls: glamourCalls, CacheHit: cacheHit}
		}
		_, continuationPrefix := narrativeLinePrefixes(req.LineStyle)
		return TextRenderResult{
			Rows:         renderNarrativeFallbackRows(req.BlockID, req.Raw, req.Prefix, continuationPrefix, req.LineStyle, req.Theme),
			GlamourCalls: glamourCalls,
			CacheHit:     cacheHit,
		}
	default:
		if req.ObserveGlamourRender != nil {
			req.ObserveGlamourRender()
		}
		rows := glamourNarrativeRows(req.BlockID, req.Raw, req.Prefix, req.LineStyle, req.Width, req.Theme)
		if len(rows) > 0 {
			return TextRenderResult{Rows: rows, GlamourCalls: 1}
		}
		_, continuationPrefix := narrativeLinePrefixes(req.LineStyle)
		return TextRenderResult{
			Rows:         renderNarrativeFallbackRows(req.BlockID, req.Raw, req.Prefix, continuationPrefix, req.LineStyle, req.Theme),
			GlamourCalls: 1,
		}
	}
}

func renderAssistantStableTailRows(req TextRenderRequest) ([]RenderedRow, int, bool) {
	if strings.TrimSpace(req.Raw) == "" && strings.TrimSpace(req.StablePrefixRaw) == "" && strings.TrimSpace(req.TailRaw) == "" {
		return nil, 0, false
	}
	if strings.TrimSpace(req.StablePrefixRaw) == "" && strings.TrimSpace(req.TailRaw) == "" {
		return glamourStreamingNarrativeRowsObserved(req.BlockID, req.Raw, req.Prefix, req.LineStyle, req.Width, req.Theme, req.ObserveGlamourRender)
	}
	return renderSplitAssistantStableTailRows(req)
}

func renderSplitAssistantStableTailRows(req TextRenderRequest) ([]RenderedRow, int, bool) {
	stableRaw := req.StablePrefixRaw
	tailRaw := req.TailRaw
	raw := stableRaw + tailRaw
	if strings.TrimSpace(stableRaw) == "" {
		prefixWidth := maxInt(graphemeWidth(req.Prefix), 0)
		return renderStreamingNarrativeTailRows(req.BlockID, raw, req.Prefix, req.LineStyle, maxInt(1, req.Width-prefixWidth), req.Theme), 0, false
	}
	prefixRows, glamourCalls, cacheHit := cachedStreamingNarrativePrefixRows(req.BlockID, stableRaw, req.Prefix, req.LineStyle, req.Width, req.Theme, req.ObserveGlamourRender)
	if len(prefixRows) == 0 {
		prefixWidth := maxInt(graphemeWidth(req.Prefix), 0)
		return renderStreamingNarrativeTailRows(req.BlockID, raw, req.Prefix, req.LineStyle, maxInt(1, req.Width-prefixWidth), req.Theme), glamourCalls, cacheHit
	}
	prefixWidth := maxInt(graphemeWidth(req.Prefix), 0)
	tailWidth := maxInt(1, req.Width-prefixWidth)
	tailRows := renderStreamingNarrativeTailRows(req.BlockID, tailRaw, "", req.LineStyle, tailWidth, req.Theme)
	if len(tailRows) == 0 {
		return prefixRows, glamourCalls, cacheHit
	}
	separatorRows := 0
	if hasStreamingParagraphBoundary(stableRaw) {
		separatorRows = 1
	}
	rows := make([]RenderedRow, 0, len(prefixRows)+separatorRows+len(tailRows))
	rows = append(rows, prefixRows...)
	if separatorRows > 0 {
		separator := strings.Repeat(" ", tailWidth)
		rows = append(rows, RenderedRow{Styled: separator, Plain: separator, BlockID: req.BlockID, PreWrapped: true})
	}
	rows = append(rows, tailRows...)
	return rows, glamourCalls, cacheHit
}

func renderInlineMarkdownTextRows(req TextRenderRequest) ([]RenderedRow, int) {
	if req.Width <= 0 {
		req.Width = 1
	}
	baseStyle := narrativeBodyStyle(req.LineStyle, req.Theme)
	prefixWidth := displayColumns(req.Prefix)
	continuationPrefix := ""
	if prefixWidth > 0 {
		continuationPrefix = strings.Repeat(" ", prefixWidth)
	}
	styledPrefix := ""
	if req.Prefix != "" {
		styledPrefix = tuikit.ColorizeLogLine(req.Prefix, req.LineStyle, req.Theme)
	}
	rawLines := strings.Split(req.Raw, "\n")
	rows := make([]RenderedRow, 0, len(rawLines))
	inlineCalls := 0
	firstSegment := true
	for _, rawLine := range rawLines {
		bodyWidth := req.Width
		if prefixWidth > 0 {
			bodyWidth = maxInt(1, req.Width-prefixWidth)
		}
		if req.ObserveInlineMarkdown != nil {
			req.ObserveInlineMarkdown()
		}
		inlineCalls++
		styledLine := renderInlineMarkdown(rawLine, baseStyle, req.Theme)
		styledSegments, plainSegments := wrapStyledANSIText(styledLine, bodyWidth)
		for i, styled := range styledSegments {
			plain := plainSegments[i]
			if prefixWidth > 0 {
				if firstSegment {
					plain = req.Prefix + plain
					styled = styledPrefix + styled
				} else {
					plain = continuationPrefix + plain
					styled = strings.Repeat(" ", prefixWidth) + styled
				}
			}
			firstSegment = false
			rows = append(rows, RenderedRow{
				Styled:     styled,
				Plain:      plain,
				BlockID:    req.BlockID,
				PreWrapped: true,
			})
		}
	}
	return rows, inlineCalls
}

func RenderStyledWrappedLine(blockID, plain, styled string, width int) []RenderedRow {
	styledSegments, plainSegments := wrapStyledANSIText(styled, width)
	if plain != "" && len(plainSegments) == 1 {
		plainSegments[0] = plain
	}
	rows := make([]RenderedRow, 0, len(styledSegments))
	for i, segment := range styledSegments {
		rows = append(rows, RenderedRow{
			Styled:     segment,
			Plain:      plainSegments[i],
			BlockID:    blockID,
			PreWrapped: true,
		})
	}
	return rows
}

func RenderPrefixedWrappedText(blockID, prefix, text string, width int, style func(string) string) []RenderedRow {
	if style == nil {
		style = func(s string) string { return s }
	}
	prefix = strings.TrimRight(prefix, " ")
	if prefix != "" {
		prefix += " "
	}
	bodyWidth := maxInt(1, width-displayColumns(prefix))
	segments := strings.Split(hardWrapDisplayLine(text, bodyWidth), "\n")
	rows := make([]RenderedRow, 0, len(segments))
	for i, segment := range segments {
		linePrefix := prefix
		if i > 0 {
			linePrefix = strings.Repeat(" ", displayColumns(prefix))
		}
		plain := linePrefix + segment
		rows = append(rows, RenderedRow{
			Styled:     style(plain),
			Plain:      plain,
			BlockID:    blockID,
			PreWrapped: true,
		})
	}
	return rows
}

func RowsFromStyledANSI(blockID, styled string) []RenderedRow {
	lines := strings.Split(styled, "\n")
	rows := make([]RenderedRow, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, RenderedRow{
			Styled:  line,
			Plain:   strings.TrimRight(ansi.Strip(line), " "),
			BlockID: blockID,
		})
	}
	return rows
}

func RowsFromPlainAndStyled(blockID, plain, styled string) RenderedRow {
	return RenderedRow{Styled: styled, Plain: plain, BlockID: blockID}
}

func renderPlainStructuralText(req TextRenderRequest) []RenderedRow {
	raw := strings.TrimRight(req.Raw, "\n")
	if raw == "" {
		return nil
	}
	style := func(s string) string { return s }
	switch req.Kind {
	case TextLog, TextToolHeader, TextToolDetail:
		style = func(s string) string { return tuikit.ColorizeLogLine(s, req.LineStyle, req.Theme) }
	case TextShellCommand:
		style = func(s string) string {
			return styleShellCommandText(BlockRenderContext{Width: req.Width, Theme: req.Theme, ThemeKey: req.ThemeKey}, s)
		}
	case TextTerminalOutput:
		style = func(s string) string { return req.Theme.ToolOutputStyle().Render(s) }
	case TextDiff:
		style = func(s string) string { return s }
	}
	lines := strings.Split(raw, "\n")
	rows := make([]RenderedRow, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, RenderStyledWrappedLine(req.BlockID, "", style(line), req.Width)...)
	}
	return rows
}

func wrapStyledANSIText(styled string, width int) ([]string, []string) {
	if styled == "" {
		return []string{""}, []string{""}
	}
	if width <= 0 {
		width = 1
	}
	plain := strings.TrimRight(ansi.Strip(styled), " ")
	if strings.TrimSpace(plain) == "" || graphemeWidth(plain) <= width {
		return []string{styled}, []string{plain}
	}
	wrapped := ansi.Wrap(styled, width, " ")
	styledSegments := strings.Split(wrapped, "\n")
	if len(styledSegments) == 0 {
		return []string{styled}, []string{plain}
	}
	return styledSegments, deriveViewportPlainLines(nil, styledSegments)
}

func normalizeTextRenderRaw(raw string) string {
	return strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
}

func normalizeTextMarkdownPolicy(req TextRenderRequest) MarkdownPolicy {
	if req.Mode == RenderPlain {
		return MarkdownNone
	}
	if req.MarkdownPolicy != MarkdownNone {
		return req.MarkdownPolicy
	}
	switch req.Kind {
	case TextAssistant:
		switch req.Mode {
		case RenderStream:
			return MarkdownStableTail
		case RenderInlineOnly:
			return MarkdownInline
		default:
			return MarkdownFull
		}
	default:
		return MarkdownNone
	}
}

func textKindLineStyle(kind TextKind) tuikit.LineStyle {
	switch kind {
	case TextAssistant:
		return tuikit.LineStyleAssistant
	case TextReasoning:
		return tuikit.LineStyleReasoning
	case TextUser:
		return tuikit.LineStyleUser
	case TextToolHeader, TextToolDetail, TextShellCommand, TextTerminalOutput:
		return tuikit.LineStyleTool
	case TextDiff:
		return tuikit.LineStyleDiffHeader
	default:
		return tuikit.LineStyleDefault
	}
}

func textKindForLineStyle(style tuikit.LineStyle) TextKind {
	switch style {
	case tuikit.LineStyleReasoning:
		return TextReasoning
	case tuikit.LineStyleUser:
		return TextUser
	case tuikit.LineStyleAssistant:
		return TextAssistant
	default:
		return TextLog
	}
}

func applyTextRenderClickToken(rows []RenderedRow, token string) []RenderedRow {
	if token == "" || len(rows) == 0 {
		return rows
	}
	out := append([]RenderedRow(nil), rows...)
	for i := range out {
		out[i].ClickToken = token
	}
	return out
}
