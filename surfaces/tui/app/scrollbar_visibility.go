package tuiapp

import (
	"math"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
)

type scrollbarTargetKind int

const (
	scrollbarTargetViewport scrollbarTargetKind = iota + 1
)

type scrollbarHitTarget struct {
	kind      scrollbarTargetKind
	lineStart int
}

type scrollbarDragState struct {
	active bool
	target scrollbarHitTarget
}

func (m *Model) touchViewportScrollbar() tea.Cmd {
	if m == nil {
		return nil
	}
	touchScrollbarDeadline(&m.viewportScrollbarVisibleUntil, time.Now())
	return m.ensureScrollbarTick()
}

func (m *Model) shouldShowViewportScrollbar(now time.Time) bool {
	return m != nil && scrollbarVisibleUntil(&m.viewportScrollbarVisibleUntil, now)
}

func (m *Model) ensureScrollbarTick() tea.Cmd {
	if m == nil || m.scrollbarTickScheduled {
		return nil
	}
	delay, ok := m.nextScrollbarVisibilityDelay(time.Now())
	if !ok {
		return nil
	}
	m.scrollbarTickScheduled = true
	return frameTickCmd(frameTickScrollbarVisible, delay)
}

func (m *Model) advanceScrollbarVisibility(_ time.Time) tea.Cmd {
	if m == nil {
		return nil
	}
	m.scrollbarTickScheduled = false
	return m.ensureScrollbarTick()
}

func (m *Model) nextScrollbarVisibilityDelay(now time.Time) (time.Duration, bool) {
	if m == nil {
		return 0, false
	}
	if m.viewportScrollbarVisibleUntil.IsZero() || !m.viewportScrollbarVisibleUntil.After(now) {
		return 0, false
	}
	delay := time.Until(m.viewportScrollbarVisibleUntil)
	if delay <= 0 {
		delay = time.Millisecond
	}
	return delay, true
}

func (m *Model) hoverScrollbarAtMouse(mouse tea.Mouse) tea.Cmd {
	target, ok := m.scrollbarHoverTargetAtMouse(mouse.X, mouse.Y)
	if !ok {
		return nil
	}
	return m.touchScrollbarTarget(target)
}

func (m *Model) touchScrollbarTarget(target scrollbarHitTarget) tea.Cmd {
	if target.kind == scrollbarTargetViewport {
		return m.touchViewportScrollbar()
	}
	return nil
}

func (m *Model) beginScrollbarDrag(mouse tea.Mouse) (bool, tea.Cmd) {
	target, ok := m.scrollbarTargetAtMouse(mouse.X, mouse.Y)
	if !ok {
		return false, nil
	}
	m.scrollbarDrag = scrollbarDragState{active: true, target: target}
	cmd := m.touchScrollbarTarget(target)
	wasFollowTail := m.isViewportFollowTail()
	changed := m.applyScrollbarDrag(mouse)
	var resumeCmd tea.Cmd
	if changed {
		m.syncViewportContent()
		if !wasFollowTail && m.isViewportFollowTail() {
			resumeCmd = m.resumeRunningAnimationIfNeeded()
		}
	}
	return true, tea.Batch(cmd, m.ensureScrollbarTick(), resumeCmd)
}

func (m *Model) updateScrollbarDrag(mouse tea.Mouse) tea.Cmd {
	if !m.scrollbarDrag.active {
		return nil
	}
	wasFollowTail := m.isViewportFollowTail()
	changed := m.applyScrollbarDrag(mouse)
	var resumeCmd tea.Cmd
	if changed {
		m.syncViewportContent()
		if !wasFollowTail && m.isViewportFollowTail() {
			resumeCmd = m.resumeRunningAnimationIfNeeded()
		}
	}
	return tea.Batch(m.touchScrollbarTarget(m.scrollbarDrag.target), m.ensureScrollbarTick(), resumeCmd)
}

func (m *Model) endScrollbarDrag() {
	m.scrollbarDrag = scrollbarDragState{}
}

func (m *Model) applyScrollbarDrag(mouse tea.Mouse) bool {
	if !m.scrollbarDrag.active {
		return false
	}
	if m.scrollbarDrag.target.kind == scrollbarTargetViewport {
		return m.dragViewportScrollbarTo(mouse.Y)
	}
	return false
}

func (m *Model) dragViewportScrollbarTo(y int) bool {
	m.materializeViewportContentIfStale()
	total := m.viewport.TotalLineCount()
	visible := maxInt(1, m.viewport.Height())
	maxOffset := maxInt(0, total-visible)
	if maxOffset == 0 {
		return false
	}
	vy := m.screenYToFrameY(y)
	if vy < 0 {
		vy = 0
	}
	if vy >= visible {
		vy = visible - 1
	}
	next := scrollbarOffsetForPosition(vy, visible, maxOffset)
	if next == m.viewport.YOffset() {
		return false
	}
	m.viewport.SetYOffset(next)
	m.refreshViewportFollowStateFromOffset()
	return true
}

func scrollbarOffsetForPosition(pos, visible, maxOffset int) int {
	if maxOffset <= 0 || visible <= 1 {
		return maxOffset
	}
	p := float64(pos) / float64(maxInt(1, visible-1))
	return int(math.Round(p * float64(maxOffset)))
}

func (m *Model) scrollbarTargetAtMouse(x, y int) (scrollbarHitTarget, bool) {
	return m.viewportScrollbarTargetAtMouse(x, y)
}

func (m *Model) scrollbarHoverTargetAtMouse(x, y int) (scrollbarHitTarget, bool) {
	return m.viewportScrollbarTargetAtMouse(x, y)
}

func (m *Model) viewportScrollbarTargetAtMouse(x, y int) (scrollbarHitTarget, bool) {
	if m.viewportScrollbarWidth() == 0 {
		return scrollbarHitTarget{}, false
	}
	total := m.viewportLineCount()
	visible := maxInt(1, m.viewport.Height())
	y = m.screenYToFrameY(y)
	if total <= visible || y < 0 || y >= visible {
		return scrollbarHitTarget{}, false
	}
	scrollbarX := m.mainColumnX() + tuikit.GutterNarrative + m.viewport.Width()
	if x < scrollbarX || x >= scrollbarX+m.viewportScrollbarWidth() {
		return scrollbarHitTarget{}, false
	}
	return scrollbarHitTarget{kind: scrollbarTargetViewport, lineStart: 0}, true
}

func touchScrollbarDeadline(until *time.Time, now time.Time) {
	if until == nil {
		return
	}
	*until = now.Add(scrollbarVisibleDuration)
}

func scrollbarVisibleUntil(until *time.Time, now time.Time) bool {
	return until != nil && now.Before(*until)
}

func (m *Model) contentLineAtViewportY(y int) (int, bool) {
	y = m.screenYToFrameY(y)
	if y < 0 || y >= m.viewport.Height() {
		return 0, false
	}
	contentLine := m.viewportVisibleOffset() + y
	if contentLine < 0 || contentLine >= len(m.viewportBlockIDs) {
		return 0, false
	}
	return contentLine, true
}
