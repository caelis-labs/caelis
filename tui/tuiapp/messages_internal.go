package tuiapp

import (
	"time"

	tea "charm.land/bubbletea/v2"

	tuiadapterruntime "github.com/OnslaughtSnail/caelis/gateway/adapter/tui/runtime"
)

type bootstrapMsg struct {
	status tuiadapterruntime.StatusSnapshot
	err    error
}

type clearHintMsg struct {
	id uint64
}

type ctrlCExpireMsg struct {
	armedAt time.Time
	seq     uint64
}

type paletteAnimationMsg struct{}
type frameTickKind string

const (
	frameTickDeferredBatch    frameTickKind = "deferred_batch"
	frameTickOffscreen        frameTickKind = "offscreen"
	frameTickViewportSync     frameTickKind = "viewport_sync"
	frameTickStreamSmoothing  frameTickKind = "stream_smoothing"
	frameTickRenderDrain      frameTickKind = "render_drain"
	frameTickPanelAnimation   frameTickKind = "panel_animation"
	frameTickScrollbarVisible frameTickKind = "scrollbar_visibility"
)

type frameTickMsg struct {
	at   time.Time
	kind frameTickKind
}

func animatePaletteCmd() tea.Cmd {
	return tea.Tick(paletteAnimationInterval, func(time.Time) tea.Msg {
		return paletteAnimationMsg{}
	})
}

func frameTickCmd(kind frameTickKind, interval time.Duration) tea.Cmd {
	if interval <= 0 {
		interval = streamSmoothingTickIntervalDefault
	}
	return tea.Tick(interval, func(at time.Time) tea.Msg {
		return frameTickMsg{at: at, kind: kind}
	})
}

func clearHintLaterCmd(id uint64, after time.Duration) tea.Cmd {
	if id == 0 || after <= 0 {
		return nil
	}
	return tea.Tick(after, func(time.Time) tea.Msg {
		return clearHintMsg{id: id}
	})
}

func expireCtrlCCmd(armedAt time.Time, seq uint64) tea.Cmd {
	if armedAt.IsZero() {
		return nil
	}
	return tea.Tick(ctrlCExitWindow, func(time.Time) tea.Msg {
		return ctrlCExpireMsg{armedAt: armedAt, seq: seq}
	})
}

func tickStatusCmd() tea.Cmd {
	return tea.Tick(1200*time.Millisecond, func(time.Time) tea.Msg {
		return TickStatusMsg{}
	})
}
