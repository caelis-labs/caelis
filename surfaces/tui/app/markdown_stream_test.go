package tuiapp

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func TestAssistantStreamingTailKeepsRawMarkdownAndSelectionPlane(t *testing.T) {
	raw := "**结论**：组合字 e\u0301、emoji 👩‍💻、[链接](https://example.com)"
	theme := tuikit.DefaultTheme()
	result := RenderText(TextRenderRequest{
		Kind:           TextAssistant,
		Mode:           RenderStream,
		MarkdownPolicy: MarkdownStableTail,
		Raw:            raw,
		Prefix:         "· ",
		Width:          160,
		BlockID:        "raw-tail-selection",
		Theme:          theme,
		LineStyle:      tuikit.LineStyleAssistant,
	})
	if result.GlamourCalls != 0 || result.InlineCalls != 0 {
		t.Fatalf("raw tail called Markdown renderers: Glamour=%d inline=%d", result.GlamourCalls, result.InlineCalls)
	}
	if got := joinRenderedPlain(result.Rows); got != "· "+raw {
		t.Fatalf("streaming plain = %q, want %q", got, "· "+raw)
	}
	for _, row := range result.Rows {
		if !row.activeTail {
			t.Fatalf("streaming raw row is not marked active: %#v", row)
		}
	}

	lines := []string{result.Rows[0].Plain}
	indents := []int{result.Rows[0].selectionIndent}
	start := textSelectionPoint{line: 0, col: indents[0]}
	end := textSelectionPoint{line: 0, col: displayColumns(lines[0])}
	if got := selectionTextFromLinesWithIndents(lines, indents, start, end); got != raw {
		t.Fatalf("selected tail = %q, want visible raw Markdown %q", got, raw)
	}
	if got := alignDisplayColumnToCharBoundary(raw, displayColumns("**结论**：组合字 e")+1); got < 0 || got > displayColumns(raw) {
		t.Fatalf("combining-grapheme selection column escaped visible range: %d", got)
	}
}

func TestAssistantSelectionAcrossFormattedPrefixAndRawTailCopiesVisibleText(t *testing.T) {
	theme := tuikit.DefaultTheme()
	stable := "# 已稳定\n\n"
	tail := strings.Repeat("**未完成** [链接](https://example.com) 👩‍💻 ", 12)
	raw := stable + tail
	stableRaw, tailRaw := splitStableStreamingMarkdown(raw)
	if stableRaw == "" || tailRaw == "" {
		t.Fatalf("test fixture did not split: stable=%q tail=%q", stableRaw, tailRaw)
	}
	ctx := BlockRenderContext{Width: 120, Theme: theme, ThemeKey: themeRenderCacheKey(theme)}
	rows := RenderTextWithContext(ctx, TextRenderRequest{
		Kind:            TextAssistant,
		Mode:            RenderStream,
		MarkdownPolicy:  MarkdownStableTail,
		Raw:             raw,
		Prefix:          "· ",
		Width:           ctx.Width,
		BlockID:         "mixed-selection",
		LineStyle:       tuikit.LineStyleAssistant,
		StablePrefixRaw: stableRaw,
		TailRaw:         tailRaw,
	}).Rows
	rows = alignParticipantNarrativeContinuationRows(rows, "  ")
	if len(rows) < 3 {
		t.Fatalf("mixed renderer rows = %#v, want prefix, boundary, and tail", rows)
	}
	plain := make([]string, len(rows))
	indents := make([]int, len(rows))
	for i, row := range rows {
		plain[i] = row.Plain
		indents[i] = row.selectionIndent
	}
	if !slices.Contains(plain, "") {
		t.Fatalf("visual paragraph separator leaked into copy plane: %#v", plain)
	}
	selected := selectionTextFromLinesWithIndents(
		plain,
		indents,
		textSelectionPoint{line: 0, col: indents[0]},
		textSelectionPoint{line: len(plain) - 1, col: displayColumns(plain[len(plain)-1])},
	)
	if !strings.HasPrefix(selected, "已稳定") {
		t.Fatalf("selection did not start from formatted prefix: %q", selected)
	}
	if strings.Contains(strings.Split(selected, "\n")[0], "#") {
		t.Fatalf("formatted prefix selection exposed source heading marker: %q", selected)
	}
	if !strings.Contains(selected, "**未完成**") || !strings.Contains(selected, "[链接](https://example.com)") {
		t.Fatalf("selection did not preserve visible raw tail: %q", selected)
	}
	if strings.Contains(selected, strings.Repeat(" ", 40)) {
		t.Fatalf("selection copied terminal-width separator padding: %q", selected)
	}
}

func TestCompletedAssistantFallsBackToCompatibleRenderedPrefix(t *testing.T) {
	clearGlamourCache()
	theme := tuikit.DefaultTheme()
	stable := "# 已渲染前缀\n\n"
	tail := "**最终 tail** [链接](https://example.com)"
	raw := stable + tail
	prefixRows, _, _ := cachedStreamingNarrativePrefixRowsForKey(
		"fallback-compatible",
		"fallback-compatible",
		stable,
		"· ",
		tuikit.LineStyleAssistant,
		96,
		theme,
		nil,
	)
	if len(prefixRows) == 0 {
		t.Fatal("failed to seed rendered stable prefix")
	}

	rows := renderCompletedAssistantRows(TextRenderRequest{
		Raw:       raw,
		Prefix:    "· ",
		Width:     96,
		BlockID:   "fallback-compatible",
		Theme:     theme,
		LineStyle: tuikit.LineStyleAssistant,
	}, nil)
	assertRenderedRowsPrefixEqual(t, rows, prefixRows)
	if got := joinRenderedPlain(rows); !strings.Contains(got, "**最终 tail** [链接](https://example.com)") {
		t.Fatalf("fallback lost raw final suffix: %q", got)
	}
	for _, row := range rows {
		if row.activeTail {
			t.Fatalf("completed fallback retained active tail: %#v", row)
		}
	}
	glamourStreamingCache.Lock()
	_, retained := glamourStreamingCache.entries["fallback-compatible"]
	glamourStreamingCache.Unlock()
	if retained {
		t.Fatal("completed fallback retained streaming cache entry")
	}
}

func TestCompletedAssistantRejectsIncompatibleRenderedPrefix(t *testing.T) {
	clearGlamourCache()
	theme := tuikit.DefaultTheme()
	_, _, _ = cachedStreamingNarrativePrefixRowsForKey(
		"fallback-incompatible",
		"fallback-incompatible",
		"# old prefix\n\n",
		"· ",
		tuikit.LineStyleAssistant,
		96,
		theme,
		nil,
	)
	const finalRaw = "# replacement\n\n**complete**"
	rows := renderCompletedAssistantRows(TextRenderRequest{
		Raw:       finalRaw,
		Prefix:    "· ",
		Width:     96,
		BlockID:   "fallback-incompatible",
		Theme:     theme,
		LineStyle: tuikit.LineStyleAssistant,
	}, nil)
	if got := joinRenderedPlain(rows); got != "· "+finalRaw {
		t.Fatalf("incompatible fallback reused stale prefix: got %q want raw %q", got, "· "+finalRaw)
	}
}

func TestStreamingNarrativeCacheIsBounded(t *testing.T) {
	clearGlamourCache()
	for i := range streamingNarrativeCacheMaxEntries + 17 {
		blockID := "bounded-" + strings.Repeat("x", i%5) + string(rune(i+1))
		storeStreamingNarrativeCacheEntry(blockID, streamingNarrativeCacheEntry{
			rendererVersion: streamingNarrativeRendererVersion,
			stableRaw:       "stable",
			renderedRows:    []RenderedRow{{Plain: blockID}},
		})
	}
	glamourStreamingCache.Lock()
	entries := len(glamourStreamingCache.entries)
	order := len(glamourStreamingCache.order)
	glamourStreamingCache.Unlock()
	if entries != streamingNarrativeCacheMaxEntries || order != streamingNarrativeCacheMaxEntries {
		t.Fatalf("stream cache sizes = entries:%d order:%d, want %d", entries, order, streamingNarrativeCacheMaxEntries)
	}
}

func TestCompletedNarrativeReleasesOnlyItsOwnStreamingPrefixCache(t *testing.T) {
	clearGlamourCache()
	theme := tuikit.DefaultTheme()
	const blockID = "shared-transcript-block"
	activeBuffer := &activeNarrativeBuffer{}
	completedBuffer := &activeNarrativeBuffer{}
	activeKey := activeBuffer.streamKey()
	completedKey := completedBuffer.streamKey()
	if activeKey == completedKey {
		t.Fatalf("distinct narrative buffers share stream key %q", activeKey)
	}
	stable := "# current active prefix\n\n"
	prefixRows, _, _ := cachedStreamingNarrativePrefixRowsForKey(
		activeKey,
		blockID,
		stable,
		"· ",
		tuikit.LineStyleAssistant,
		96,
		theme,
		nil,
	)
	if len(prefixRows) == 0 {
		t.Fatal("failed to seed active narrative prefix cache")
	}

	_ = renderCompletedAssistantRows(TextRenderRequest{
		Raw:       "earlier completed narrative",
		Prefix:    "· ",
		Width:     96,
		BlockID:   blockID,
		StreamKey: completedKey,
		Theme:     theme,
		LineStyle: tuikit.LineStyleAssistant,
	}, nil)

	rows, calls, hit := cachedStreamingNarrativePrefixRowsForKey(
		activeKey,
		blockID,
		stable,
		"· ",
		tuikit.LineStyleAssistant,
		96,
		theme,
		nil,
	)
	if !hit || calls != 0 {
		t.Fatalf("completed sibling evicted active cache: hit=%v Glamour calls=%d", hit, calls)
	}
	assertRenderedRowsPrefixEqual(t, rows, prefixRows)
}

func TestActiveNarrativeReusesStablePrefixWhileRawTailGrows(t *testing.T) {
	clearGlamourCache()
	theme := tuikit.DefaultTheme()
	var glamourCalls int
	ctx := BlockRenderContext{
		Width:                96,
		Theme:                theme,
		ThemeKey:             themeRenderCacheKey(theme),
		ObserveGlamourRender: func() { glamourCalls++ },
	}
	buffer := &activeNarrativeBuffer{
		stablePrefixRaw: "# cached prefix\n\n",
		tailRaw:         "raw **tail**",
		version:         1,
	}
	_ = buffer.RenderRowsAtWidth("stable-prefix-growth", "· ", tuikit.LineStyleAssistant, ctx.Width, ctx)
	if glamourCalls != 1 {
		t.Fatalf("initial stable prefix Glamour calls = %d, want 1", glamourCalls)
	}
	buffer.Append(" grows")
	rows := buffer.RenderRowsAtWidth("stable-prefix-growth", "· ", tuikit.LineStyleAssistant, ctx.Width, ctx)
	if glamourCalls != 1 {
		t.Fatalf("tail growth rerendered unchanged stable prefix: Glamour calls=%d", glamourCalls)
	}
	if got := joinRenderedPlain(rows); !strings.Contains(got, "raw **tail** grows") {
		t.Fatalf("tail growth not rendered from cache: %q", got)
	}
}

func TestViewportSelectionKeepsReleaseSnapshotAcrossStreamFinalization(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 96, Height: 24})
	model = updated.(*Model)
	event := TranscriptEvent{
		Kind:          TranscriptEventNarrative,
		NarrativeKind: TranscriptNarrativeAssistant,
		Scope:         ACPProjectionMain,
		ScopeID:       "selection-stream-final",
		TurnID:        "selection-stream-final",
		MessageID:     "message-selection-stream-final",
		Actor:         "assistant",
		Text:          "**流式** [链接](https://example.com)",
	}
	model = applyTranscriptEventForTest(t, model, event)
	model.syncViewportContent()
	line := slices.IndexFunc(model.viewportPlainLines, func(line string) bool {
		return strings.Contains(line, "**流式**")
	})
	if line < 0 {
		t.Fatalf("raw stream row not found: %#v", model.viewportPlainLines)
	}
	indent := model.viewportSelectionIndents[line]
	model.selectionStart = textSelectionPoint{line: line, col: indent}
	model.selectionEnd = textSelectionPoint{line: line, col: displayColumns(model.viewportPlainLines[line])}
	wantSelection := "**流式** [链接](https://example.com)"
	if got := model.selectionText(); got != wantSelection {
		t.Fatalf("initial selection = %q, want %q", got, wantSelection)
	}
	visibleSnapshot := append([]string(nil), model.viewportPlainLines...)

	event.Text = "**流式** [链接](https://example.com) 已完成"
	event.Final = true
	model = applyTranscriptEventForTest(t, model, event)
	if !slices.Equal(model.viewportPlainLines, visibleSnapshot) {
		t.Fatalf("viewport changed under active selection\n before=%#v\n  after=%#v", visibleSnapshot, model.viewportPlainLines)
	}
	if got := model.selectionText(); got != wantSelection {
		t.Fatalf("selection changed under finalization: got %q want %q", got, wantSelection)
	}

	model.clearSelection()
	_ = model.flushPendingOffscreenViewportSync(time.Now().Add(time.Second))
	if slices.Equal(model.viewportPlainLines, visibleSnapshot) {
		t.Fatal("viewport did not publish finalized Glamour rows after selection cleared")
	}
	if got := strings.Join(model.viewportPlainLines, "\n"); strings.Contains(got, "**流式**") || !strings.Contains(got, "流式") || !strings.Contains(got, "链接") || !strings.Contains(got, "已完成") || strings.ContainsRune(got, '\a') {
		t.Fatalf("finalized viewport did not switch to Glamour output: %q", got)
	}
	block := requireMainACPTurnBlockForTest(t, model)
	for _, row := range block.Render(model.blockRenderContext(96)) {
		if row.activeTail {
			t.Fatalf("stream finalization left an active tail row: %#v", row)
		}
	}
}

func TestWindowResizeClearsDisplayColumnSelectionBeforeReflow(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 96, Height: 24})
	model = updated.(*Model)
	model.viewportPlainLines = []string{"· **原始 Markdown** 👩‍💻"}
	model.viewportStyledLines = append([]string(nil), model.viewportPlainLines...)
	model.viewportSelectionIndents = []int{displayColumns("· ")}
	model.selectionStart = textSelectionPoint{line: 0, col: displayColumns("· ")}
	model.selectionEnd = textSelectionPoint{line: 0, col: displayColumns(model.viewportPlainLines[0])}

	updated, _ = model.Update(tea.WindowSizeMsg{Width: 48, Height: 24})
	model = updated.(*Model)
	if model.selectionStart.line >= 0 || model.selectionEnd.line >= 0 || model.selecting {
		t.Fatalf("resize retained stale display-column selection: start=%#v end=%#v", model.selectionStart, model.selectionEnd)
	}
}

func TestWindowHeightResizePreservesLogicalSelections(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 96, Height: 24})
	model = updated.(*Model)
	model.viewportPlainLines = []string{"· rendered response"}
	model.viewportStyledLines = append([]string(nil), model.viewportPlainLines...)
	model.viewportSelectionIndents = []int{displayColumns("· ")}
	model.selecting = true
	model.selectionStart = textSelectionPoint{line: 0, col: displayColumns("· ")}
	model.selectionEnd = textSelectionPoint{line: 0, col: displayColumns(model.viewportPlainLines[0])}
	model.inputSelecting = true
	model.inputSelectionStart = textSelectionPoint{line: 0, col: 0}
	model.inputSelectionEnd = textSelectionPoint{line: 0, col: 4}
	model.fixedSelecting = true
	model.fixedSelectionArea = fixedSelectionFooter
	model.fixedSelectionStart = textSelectionPoint{line: 0, col: 0}
	model.fixedSelectionEnd = textSelectionPoint{line: 0, col: 4}
	model.subagentOutputOverlay = &subagentOutputOverlayState{
		selectStart: textSelectionPoint{line: 0, col: 0},
		selectEnd:   textSelectionPoint{line: 0, col: 4},
	}

	updated, _ = model.Update(tea.WindowSizeMsg{Width: 96, Height: 30})
	model = updated.(*Model)
	if !model.selecting || model.selectionStart != (textSelectionPoint{line: 0, col: 2}) || model.selectionEnd != (textSelectionPoint{line: 0, col: 19}) {
		t.Fatalf("height-only resize cleared viewport selection: selecting=%v start=%#v end=%#v", model.selecting, model.selectionStart, model.selectionEnd)
	}
	if !model.inputSelecting || model.inputSelectionStart != (textSelectionPoint{line: 0, col: 0}) || model.inputSelectionEnd != (textSelectionPoint{line: 0, col: 4}) {
		t.Fatalf("height-only resize cleared composer selection: selecting=%v start=%#v end=%#v", model.inputSelecting, model.inputSelectionStart, model.inputSelectionEnd)
	}
	if model.fixedSelecting || model.fixedSelectionArea != fixedSelectionNone || model.fixedSelectionStart.line >= 0 || model.fixedSelectionEnd.line >= 0 {
		t.Fatalf("height-only resize retained fixed-row selection: selecting=%v area=%q start=%#v end=%#v", model.fixedSelecting, model.fixedSelectionArea, model.fixedSelectionStart, model.fixedSelectionEnd)
	}
	if state := model.subagentOutputOverlay; state.selectStart.line >= 0 || state.selectEnd.line >= 0 {
		t.Fatalf("height-only resize retained overlay selection: start=%#v end=%#v", state.selectStart, state.selectEnd)
	}
}
