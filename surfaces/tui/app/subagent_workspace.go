package tuiapp

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/uipreferences"
)

type paneRect struct{ x, y, width, height int }

const (
	workspaceMinPaneWidth  = 60
	workspaceMinPaneHeight = 20
)

func (r paneRect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.width && y >= r.y && y < r.y+r.height
}

type workspaceLayout struct {
	main, child, divider paneRect
	split                bool
}
type subagentWorkspaceState struct {
	childFocused bool
	lastCallID   string
	dragging     bool
	resizing     bool
	resizeRatio  int
}
type paneMenuItem struct{ label, binding, value string }

type paneHeaderAction struct {
	x, width int
	value    string
}

// workspaceLayout is the only owner of pane rectangles. Ratios describe the
// main pane; a small terminal temporarily presents the child as an overlay.
func (m *Model) workspaceLayout() workspaceLayout {
	return m.workspaceLayoutForPreferences(m.uiPreferences.value)
}

func (m *Model) workspaceLayoutForPreferences(p uipreferences.Preferences) workspaceLayout {
	w, h := maxInt(1, m.width), maxInt(1, m.height)
	out := workspaceLayout{main: paneRect{width: w, height: h}}
	if m.subagentOutputOverlay == nil {
		return out
	}
	p = p.WithDefaults()
	horizontal := p.SubagentLayout == uipreferences.Left || p.SubagentLayout == uipreferences.Right
	vertical := p.SubagentLayout == uipreferences.Up || p.SubagentLayout == uipreferences.Down
	axis, minimum, ratio := w-1, workspaceMinPaneWidth, p.HorizontalRatio
	if vertical {
		axis, minimum, ratio = h-1, workspaceMinPaneHeight, p.VerticalRatio
	}
	if (horizontal || vertical) && m.paneLayoutAvailable(p.SubagentLayout) {
		lo, hi := maxInt(minimum, (axis*uipreferences.MinRatio+99)/100), minInt(axis-minimum, axis*uipreferences.MaxRatio/100)
		if lo <= hi {
			main := clampInt(axis*ratio/100, lo, hi)
			child := axis - main
			out.split = true
			switch p.SubagentLayout {
			case uipreferences.Right:
				out.main.width = main
				out.child = paneRect{main + 1, 0, child, h}
				out.divider = paneRect{main, 0, 1, h}
			case uipreferences.Left:
				out.main = paneRect{child + 1, 0, main, h}
				out.child = paneRect{0, 0, child, h}
				out.divider = paneRect{child, 0, 1, h}
			case uipreferences.Down:
				out.main.height = main
				out.child = paneRect{0, main + 1, w, child}
				out.divider = paneRect{0, main, w, 1}
			case uipreferences.Up:
				out.main = paneRect{0, child + 1, w, main}
				out.child = paneRect{0, 0, w, child}
				out.divider = paneRect{0, child, w, 1}
			}
			return out
		}
	}
	cw, ch := maxInt(1, w-4), maxInt(1, h-4)
	if !m.overlayUsesBorder() {
		cw = w
	}
	if !m.overlayUsesBorder() || h < 18 {
		ch = h
	}
	out.child = paneRect{(w - cw) / 2, (h - ch) / 2, cw, ch}
	return out
}

func (m *Model) resizeWorkspace() {
	m.clearPaneChromeMouse()
	m.refreshPaneLayoutMenu()
	m.reconcileSubagentPaneFocus()
	m.clearSelection()
	m.clearInputSelection()
	m.clearFixedSelection()
	m.viewport.SetWidth(m.viewportContentWidth())
	m.textarea.SetWidth(m.composerContentWidth())
	m.adjustTextareaHeight()
	m.ensureViewportLayout()
	m.syncViewportContent()
}

func (m *Model) openSubagentWorkspace() bool {
	if m.subagentOutputOverlay != nil {
		m.workspace.childFocused = true
		return true
	}
	if view := m.subagentOutputViews[m.workspace.lastCallID]; view != nil {
		return m.openSubagentOutputOverlayView(m.workspace.lastCallID, view)
	}
	rows := m.subagentRosterRows()
	if len(rows) == 0 {
		return false
	}
	return m.openSubagentOutputOverlayView(rows[0].callID, m.subagentOutputViews[rows[0].callID])
}

func (m *Model) setSubagentLayout(layout uipreferences.Layout) tea.Cmd {
	p := uipreferences.Preferences{SubagentLayout: layout}
	if p.Validate() != nil {
		return nil
	}
	cmd := m.setUIPreferences(p)
	if state := m.subagentOutputOverlay; state != nil {
		state.menu = ""
	}
	m.resizeWorkspace()
	return cmd
}
func (m *Model) handlePaneDivider(msg tea.MouseMsg) (bool, tea.Cmd) {
	layout := m.workspaceLayout()
	if !layout.split || m.activePrompt != nil || m.subagentOverlay != nil || m.workspace.resizing {
		return false, nil
	}
	mouse := msg.Mouse()
	switch msg.(type) {
	case tea.MouseClickMsg:
		if mouse.Button == tea.MouseLeft && layout.divider.contains(mouse.X, mouse.Y) {
			m.workspace.dragging = true
			m.workspace.resizeRatio = m.paneSplitRatio()
			m.clearSubagentOutputSelection()
			return true, nil
		}
	case tea.MouseMotionMsg, tea.MouseReleaseMsg:
		if !m.workspace.dragging {
			return false, nil
		}
		p := m.uiPreferences.value.WithDefaults()
		axis, pos := m.width-1, mouse.X
		if p.SubagentLayout == uipreferences.Up || p.SubagentLayout == uipreferences.Down {
			axis, pos = m.height-1, mouse.Y
		}
		if p.SubagentLayout == uipreferences.Left || p.SubagentLayout == uipreferences.Up {
			pos = axis - pos
		}
		ratio := clampInt((pos*100+axis/2)/maxInt(1, axis), uipreferences.MinRatio, uipreferences.MaxRatio)
		m.workspace.resizeRatio = ratio
		if _, released := msg.(tea.MouseReleaseMsg); released {
			m.workspace.dragging = false
			return true, m.applyPaneResize()
		}
		return true, nil
	}
	return false, nil
}

func (m *Model) paneLayoutAvailable(layout uipreferences.Layout) bool {
	switch layout {
	case uipreferences.Overlay:
		return true
	case uipreferences.Left, uipreferences.Right:
		return m.width >= 2*workspaceMinPaneWidth+1 && m.height >= workspaceMinPaneHeight
	case uipreferences.Up, uipreferences.Down:
		return m.width >= workspaceMinPaneWidth && m.height >= 2*workspaceMinPaneHeight+1
	}
	return false
}

func (m *Model) effectivePaneLayout() uipreferences.Layout {
	if !m.workspaceLayout().split {
		return uipreferences.Overlay
	}
	return m.uiPreferences.value.WithDefaults().SubagentLayout
}

func paneLayoutSymbol(layout uipreferences.Layout) string {
	switch layout {
	case uipreferences.Left:
		return "◧"
	case uipreferences.Right:
		return "◨"
	case uipreferences.Up:
		return "⬒"
	case uipreferences.Down:
		return "⬓"
	}
	return "▣"
}
func (m *Model) paneStatusParts(state *subagentOutputOverlayState) (string, string) {
	descriptor := m.subagentRosterTasks[state.callID]
	usage := ""
	if descriptor.ContextSize > 0 {
		usage = fmt.Sprintf("%s / %s · %d%%", compactPaneTokens(descriptor.ContextUsed), compactPaneTokens(descriptor.ContextSize), uint64(float64(descriptor.ContextUsed)/float64(descriptor.ContextSize)*100))
	}
	return paneModelDisplay(descriptor.Model), usage
}

func paneModelDisplay(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "Model unavailable"
	}
	if endpoint, alias, ok := strings.Cut(model, "/"); ok && strings.Contains(endpoint, "@") {
		if alias = strings.TrimSpace(alias); alias != "" {
			return alias
		}
	}
	return model
}

func compactPaneTokens(n uint64) string {
	if n >= 1000 {
		return fmt.Sprintf("%.0fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

// A modal pane owns input even when a split had focused the main composer.
func (m *Model) reconcileSubagentPaneFocus() {
	if m.subagentOutputOverlay != nil && !m.workspaceLayout().split {
		m.workspace.childFocused = true
	}
}
