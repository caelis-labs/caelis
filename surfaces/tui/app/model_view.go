package tuiapp

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func (m *Model) View() tea.View {
	start := time.Now()

	if !m.ready {
		m.frameTopTrim = 0
		view := tea.NewView("loading...")
		view.AltScreen = true
		view.MouseMode = tea.MouseModeCellMotion
		return view
	}

	// Compute layout; bottomHeight is needed for overlay positioning.
	// Viewport height is reconciled in Update() via ensureViewportLayout(),
	// so we intentionally do NOT mutate viewport state here.
	_, bottomHeight := m.computeLayout()

	// Cache composer layout for the frame, but not while dragging a selection:
	// cursor-driven window scroll must see a fresh layout so highlight stays aligned.
	if m.activePrompt == nil && !m.inputSelecting {
		snapshot := m.buildComposeInputLayout()
		m.composerViewSnapshot = &snapshot
	} else {
		m.composerViewSnapshot = nil
	}
	defer func() { m.composerViewSnapshot = nil }()

	var sections []string

	// 1. Viewport (scrollable history + streaming + spinner) with left gutter.
	vpView := m.renderViewportView()
	if tuikit.GutterNarrative > 0 {
		vpView = indentBlock(vpView, tuikit.GutterNarrative)
	}
	sections = append(sections, m.placeInMainColumn(vpView))
	sections = append(sections, "")

	if drawerView := m.renderPrimaryDrawer(); drawerView != "" {
		sections = append(sections, m.placeInMainColumn(drawerView))
		sections = append(sections, "")
	}
	if reserve := m.promptModalReservedHeight(); reserve > 0 {
		for range reserve {
			sections = append(sections, "")
		}
	}

	// 2. Hint row (contextual guidance).
	sections = append(sections, m.placeInMainColumn(m.renderHintRow()))
	sections = append(sections, "")

	// 5. Composer top padding before input.
	for range tuikit.ComposerPadTop {
		sections = append(sections, "")
	}

	// 6. Input bar.
	sections = append(sections, m.placeInMainColumn(m.renderInputBar()))

	// 7. Composer bottom padding before footer.
	for range tuikit.ComposerPadBottom {
		sections = append(sections, "")
	}

	// 8. Secondary status bar.
	sections = append(sections, m.placeInMainColumn(m.renderStatusFooter()))

	// 9. Status bar bottom padding.
	for range tuikit.StatusBarPadBottom {
		sections = append(sections, "")
	}

	view := strings.Join(sections, "\n")
	mainRect := m.workspaceLayout().main
	topTrim := 0
	if m.workspaceLayout().split {
		view, topTrim = normalizeFullscreenFrameWithTopTrim(view, m.width, mainRect.height)
	}
	if mainRect.y > 0 {
		view = strings.Repeat("\n", mainRect.y) + view
	}
	baseNormalized := false
	normalizeBaseForOverlay := func() {
		if baseNormalized {
			return
		}
		var trim int
		view, trim = m.normalizeFullscreenFrameWithTopTrim(view)
		topTrim += trim
		baseNormalized = true
	}

	if m.subagentOutputOverlay != nil && m.width > 0 && m.height > 0 {
		if overlay := m.renderSubagentOutputOverlay(); overlay != "" {
			normalizeBaseForOverlay()
			if layout := m.workspaceLayout(); layout.split {
				view = m.subagentOutputOverlay.splitFrame.compose(view, overlay, m.paneDivider(layout.divider), layout)
				view = m.renderPaneResizePreview(view)
			} else {
				view = m.subagentOutputOverlay.composition.compose(view, overlay, m.width, m.height)
			}
			if menu := m.renderPaneMenu(); menu != "" {
				rect := m.subagentOutputOverlay.menuRect
				view = tuikit.OverlayAt(view, menu, m.width, m.height, rect.x, rect.y)
			}
			view = m.renderPaneTooltip(view)
		}
	}

	if m.activePrompt != nil && m.width > 0 && m.height > 0 {
		if promptView := m.renderPromptModal(); promptView != "" {
			normalizeBaseForOverlay()
			view = overlayAboveBottomAreaLeft(view, promptView, m.width, m.mainColumnX()+inputHorizontalInset, maxInt(0, m.height-mainRect.y-mainRect.height+bottomHeight-m.promptModalReservedHeight()), 0)
		}
	} else if overlayView := m.renderInputOverlay(); overlayView != "" && m.width > 0 && m.height > 0 {
		normalizeBaseForOverlay()
		view = overlayAboveBottomAreaLeft(view, overlayView, m.width, m.mainColumnX()+inputHorizontalInset, m.height-mainRect.y-mainRect.height+bottomHeight, 0)
	}

	// Overlay: command palette.
	if m.shouldRenderPalette() && m.width > 0 && m.height > 0 {
		if paletteView := m.renderPaletteOverlay(); paletteView != "" {
			normalizeBaseForOverlay()
			view = overlayAboveBottomAreaLeft(view, paletteView, m.width, m.mainColumnX()+inputHorizontalInset, m.height-mainRect.y-mainRect.height+bottomHeight, 0)
		}
	}
	if m.width > 0 && m.height > 0 {
		if progressView := m.renderSandboxProgressOverlay(); progressView != "" {
			normalizeBaseForOverlay()
			view = overlayTopRight(view, progressView, m.width, sandboxProgressOverlayTopInset, sandboxProgressOverlayRightInset)
		}
	}
	if m.subagentOverlay != nil && m.width > 0 && m.height > 0 {
		if overlay := m.renderSubagentOverlay(); overlay != "" {
			normalizeBaseForOverlay()
			view = tuikit.OverlayCenter(view, overlay, m.width, m.height)
		}
	}
	var finalTrim int
	view, finalTrim = m.normalizeFullscreenFrameWithTopTrim(view)
	topTrim += finalTrim
	m.frameTopTrim = topTrim

	duration := time.Since(start)
	m.observeRender(duration, len(view), "fullscreen")
	frame := tea.NewView(view)
	frame.AltScreen = true
	frame.MouseMode = m.desiredMouseMode()
	frame.ReportFocus = true
	frame.WindowTitle = m.windowTitle()
	if m.subagentOverlay == nil && (!m.workspace.childFocused || m.activePrompt != nil) {
		if cursor := m.regularInputCursor(); cursor != nil {
			cursor.X += m.mainColumnX()
			cursor.Y += m.viewport.Height() + m.preComposerFixedHeight() + tuikit.ComposerPadTop
			cursor.Y += m.composerChrome().topRows()
			cursor.Y += mainRect.y
			cursor.Y -= topTrim
			if cursor.Y < 0 {
				cursor.Y = 0
			}
			if m.height > 0 && cursor.Y >= m.height {
				cursor.Y = m.height - 1
			}
			frame.Cursor = cursor
		}
	}
	if m.activePrompt == nil {
		if cursor := m.paneCursor(); cursor != nil {
			frame.Cursor = cursor
		}
	}
	return frame
}

func (m *Model) desiredMouseMode() tea.MouseMode {
	if m.subagentOutputOverlay != nil && m.activePrompt == nil && m.subagentOverlay == nil {
		return tea.MouseModeAllMotion
	}
	return tea.MouseModeCellMotion
}
