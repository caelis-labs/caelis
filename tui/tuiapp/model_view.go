package tuiapp

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/OnslaughtSnail/caelis/tui/tuikit"
)

func (m *Model) View() tea.View {
	start := time.Now()

	if !m.ready {
		m.frameTopTrim = 0
		view := tea.NewView("loading...")
		view.AltScreen = true
		view.MouseMode = tea.MouseModeCellMotion
		view.KeyboardEnhancements.ReportEventTypes = true
		return view
	}

	// Compute layout; bottomHeight is needed for overlay positioning.
	// Viewport height is reconciled in Update() via ensureViewportLayout(),
	// so we intentionally do NOT mutate viewport state here.
	_, bottomHeight := m.computeLayout()

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
	if pendingView := m.renderPendingQueueDrawer(); pendingView != "" {
		sections = append(sections, m.placeInMainColumn(pendingView))
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

	// 3. Workspace + model status.
	sections = append(sections, m.placeInMainColumn(m.renderStatusHeader()))

	// 4. Separator above the composer input.
	if width := m.fixedRowWidth(); width > 0 {
		sep := m.theme.SeparatorStyle().Render(strings.Repeat("─", width))
		sections = append(sections, m.placeInMainColumn(sep))
	}

	// 5. Composer top padding before input.
	for range tuikit.ComposerPadTop {
		sections = append(sections, "")
	}

	// 6. Input bar.
	sections = append(sections, m.placeInMainColumn(m.renderInputBar()))

	// 7. Composer bottom padding before footer separator.
	for range tuikit.ComposerPadBottom {
		sections = append(sections, "")
	}

	// 8. Lower separator + secondary status bar.
	if width := m.fixedRowWidth(); width > 0 {
		sep := m.theme.SeparatorStyle().Render(strings.Repeat("─", width))
		sections = append(sections, m.placeInMainColumn(sep))
	}
	sections = append(sections, m.placeInMainColumn(m.renderStatusFooter()))

	// 9. Status bar bottom padding.
	for range tuikit.StatusBarPadBottom {
		sections = append(sections, "")
	}

	view := strings.Join(sections, "\n")
	topTrim := 0
	view, topTrim = normalizeFullscreenFrameWithTopTrim(view, m.width, m.height)

	if m.activePrompt != nil && m.width > 0 && m.height > 0 {
		if promptView := m.renderPromptModal(); promptView != "" {
			view = overlayAboveBottomAreaLeft(view, promptView, m.width, m.mainColumnX()+inputHorizontalInset, maxInt(0, bottomHeight-m.promptModalReservedHeight()), 0)
		}
	} else if overlayView := m.renderInputOverlay(); overlayView != "" && m.width > 0 && m.height > 0 {
		view = overlayAboveBottomAreaLeft(view, overlayView, m.width, m.mainColumnX()+inputHorizontalInset, bottomHeight, 0)
	}

	// Overlay: command palette.
	if m.shouldRenderPalette() && m.width > 0 && m.height > 0 {
		if paletteView := m.renderPaletteOverlay(); paletteView != "" {
			view = overlayAboveBottomAreaLeft(view, paletteView, m.width, m.mainColumnX()+inputHorizontalInset, bottomHeight, 0)
		}
	}
	secondTrim := 0
	view, secondTrim = normalizeFullscreenFrameWithTopTrim(view, m.width, m.height)
	topTrim += secondTrim
	m.frameTopTrim = topTrim

	duration := time.Since(start)
	m.observeRender(duration, len(view), "fullscreen")
	frame := tea.NewView(view)
	frame.AltScreen = true
	frame.MouseMode = tea.MouseModeAllMotion
	frame.ReportFocus = true
	frame.KeyboardEnhancements.ReportEventTypes = true
	frame.WindowTitle = m.windowTitle()
	if cursor := m.regularInputCursor(); cursor != nil {
		cursor.X += m.mainColumnX()
		cursor.Y += m.viewport.Height() + m.preComposerFixedHeight() + tuikit.ComposerPadTop
		cursor.Y -= topTrim
		if cursor.Y < 0 {
			cursor.Y = 0
		}
		if m.height > 0 && cursor.Y >= m.height {
			cursor.Y = m.height - 1
		}
		frame.Cursor = cursor
	}
	return frame
}
