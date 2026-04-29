package tuiapp

import (
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/tui/tuikit"
)

type scrollbarTargetKind int

const (
	scrollbarTargetViewport scrollbarTargetKind = iota + 1
	scrollbarTargetSubagentPanel
)

type panelScrollbarBlock interface {
	Block
	previewLines() int
	scrollableLineCount(BlockRenderContext) int
	scrollState() (*int, *bool)
	scrollbarVisibleUntilPtr() *time.Time
}

type scrollbarHitTarget struct {
	kind      scrollbarTargetKind
	blockID   string
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

func (m *Model) touchPanelScrollbar(block Block) tea.Cmd {
	if m == nil {
		return nil
	}
	panel, ok := block.(panelScrollbarBlock)
	if !ok {
		return nil
	}
	touchScrollbarDeadline(panel.scrollbarVisibleUntilPtr(), time.Now())
	return m.ensureScrollbarTick()
}

func (m *Model) shouldShowViewportScrollbar(now time.Time) bool {
	return m != nil && scrollbarVisibleUntil(&m.viewportScrollbarVisibleUntil, now)
}

func (b *SubagentPanelBlock) shouldShowScrollbar(now time.Time) bool {
	return scrollbarVisibleUntil(b.scrollbarVisibleUntilPtr(), now)
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
	next := time.Time{}
	consider := func(until time.Time) {
		if until.IsZero() || !until.After(now) {
			return
		}
		if next.IsZero() || until.Before(next) {
			next = until
		}
	}
	consider(m.viewportScrollbarVisibleUntil)
	for _, block := range m.doc.Blocks() {
		if panel, ok := block.(panelScrollbarBlock); ok {
			if until := panel.scrollbarVisibleUntilPtr(); until != nil {
				consider(*until)
			}
		}
	}
	if next.IsZero() {
		return 0, false
	}
	delay := time.Until(next)
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
	switch target.kind {
	case scrollbarTargetViewport:
		return m.touchViewportScrollbar()
	case scrollbarTargetSubagentPanel:
		if block := m.doc.Find(target.blockID); block != nil {
			cmd := m.touchPanelScrollbar(block)
			m.markViewportBlockDirty(block.BlockID())
			m.syncViewportContent()
			return cmd
		}
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
	target := m.scrollbarDrag.target
	switch target.kind {
	case scrollbarTargetViewport:
		return m.dragViewportScrollbarTo(mouse.Y)
	case scrollbarTargetSubagentPanel:
		return m.dragPanelScrollbarTo(target, mouse.Y)
	default:
		return false
	}
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

func (m *Model) dragPanelScrollbarTo(target scrollbarHitTarget, y int) bool {
	block := m.doc.Find(target.blockID)
	panel, ok := block.(panelScrollbarBlock)
	if !ok {
		return false
	}
	ctx := m.blockRenderContext(maxInt(1, m.viewport.Width()))
	localY := m.screenYToFrameY(y)
	if localY < 0 {
		localY = 0
	}
	offset, followTail := panel.scrollState()
	if offset == nil || followTail == nil {
		return false
	}
	changed := setPanelScrollFromPointer(offset, followTail, panel.scrollableLineCount(ctx), panel.previewLines(), localY-target.lineStart)
	if changed {
		m.markViewportBlockDirty(target.blockID)
	}
	return changed
}

func scrollbarOffsetForPosition(pos, visible, maxOffset int) int {
	if maxOffset <= 0 || visible <= 1 {
		return maxOffset
	}
	p := float64(pos) / float64(maxInt(1, visible-1))
	return int(math.Round(p * float64(maxOffset)))
}

func setPanelScrollFromPointer(offset *int, followTail *bool, total, visible, localY int) bool {
	if offset == nil || followTail == nil {
		return false
	}
	maxOffset := maxInt(0, total-visible)
	if maxOffset == 0 {
		return false
	}
	if localY < 0 {
		localY = 0
	}
	if localY >= visible {
		localY = visible - 1
	}
	next := scrollbarOffsetForPosition(localY, visible, maxOffset)
	if *offset == next && *followTail == (next == maxOffset) {
		return false
	}
	*offset = next
	*followTail = next == maxOffset
	return true
}

func (m *Model) scrollbarTargetAtMouse(x, y int) (scrollbarHitTarget, bool) {
	if target, ok := m.panelScrollbarTargetAtMouse(x, y); ok {
		return target, true
	}
	return m.viewportScrollbarTargetAtMouse(x, y)
}

func (m *Model) scrollbarHoverTargetAtMouse(x, y int) (scrollbarHitTarget, bool) {
	if target, ok := m.scrollbarTargetAtMouse(x, y); ok {
		return target, true
	}
	return m.panelHoverTargetAtMouse(y)
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

func (m *Model) panelScrollbarTargetAtMouse(x, y int) (scrollbarHitTarget, bool) {
	contentLine, ok := m.contentLineAtViewportY(y)
	if !ok {
		return scrollbarHitTarget{}, false
	}
	_, target, lineWidth, ok := m.panelScrollbarHitAtContentLine(contentLine)
	if !ok {
		return scrollbarHitTarget{}, false
	}
	if lineWidth <= 0 {
		return scrollbarHitTarget{}, false
	}
	hotZoneWidth := minInt(8, lineWidth)
	scrollbarX := m.mainColumnX() + tuikit.GutterNarrative + maxInt(0, lineWidth-hotZoneWidth)
	if x < scrollbarX || x >= scrollbarX+hotZoneWidth {
		return scrollbarHitTarget{}, false
	}
	return target, true
}

func (m *Model) panelHoverTargetAtMouse(y int) (scrollbarHitTarget, bool) {
	contentLine, ok := m.contentLineAtViewportY(y)
	if !ok {
		return scrollbarHitTarget{}, false
	}
	_, target, _, ok := m.panelScrollbarHitAtContentLine(contentLine)
	return target, ok
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

func panelScrollbarKind(block panelScrollbarBlock) scrollbarTargetKind {
	switch block.(type) {
	case *SubagentPanelBlock:
		return scrollbarTargetSubagentPanel
	default:
		return 0
	}
}

func (m *Model) panelScrollbarHitAtContentLine(contentLine int) (panelScrollbarBlock, scrollbarHitTarget, int, bool) {
	if contentLine < 0 || contentLine >= len(m.viewportBlockIDs) {
		return nil, scrollbarHitTarget{}, 0, false
	}
	blockID := strings.TrimSpace(m.viewportBlockIDs[contentLine])
	if blockID == "" {
		return nil, scrollbarHitTarget{}, 0, false
	}
	block := m.doc.Find(blockID)
	panel, ok := block.(panelScrollbarBlock)
	if !ok {
		return nil, scrollbarHitTarget{}, 0, false
	}
	kind := panelScrollbarKind(panel)
	if kind == 0 {
		return nil, scrollbarHitTarget{}, 0, false
	}
	ctx := m.blockRenderContext(maxInt(1, m.viewport.Width()))
	if panel.scrollableLineCount(ctx) <= panel.previewLines() {
		return nil, scrollbarHitTarget{}, 0, false
	}
	start, _ := m.visibleBlockLineRange(blockID, contentLine)
	lineWidth := 0
	if contentLine < len(m.viewportPlainLines) {
		lineWidth = displayColumns(m.viewportPlainLines[contentLine])
	}
	return panel, scrollbarHitTarget{
		kind:      kind,
		blockID:   blockID,
		lineStart: start - m.viewportVisibleOffset(),
	}, lineWidth, true
}

func (m *Model) visibleBlockLineRange(blockID string, contentLine int) (int, int) {
	start := contentLine
	for start > 0 && m.viewportBlockIDs[start-1] == blockID {
		start--
	}
	end := contentLine
	for end+1 < len(m.viewportBlockIDs) && m.viewportBlockIDs[end+1] == blockID {
		end++
	}
	return start, end
}
