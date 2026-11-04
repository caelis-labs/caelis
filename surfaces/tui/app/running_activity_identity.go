package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// observeLiveActivityTurn follows Control's main Turn identity. A new running
// Turn replaces missing terminal delivery; a retired Turn can still repair its
// transcript, but can neither revive a hint nor finish the current Turn.
func (m *Model) observeLiveActivityTurn(env eventstream.Envelope) bool {
	if env.ParentTool != nil || env.ApprovalRequestID != "" || (env.Scope != "" && env.Scope != eventstream.ScopeMain) {
		return true
	}
	t := &m.runningHintTracker
	t.ensure()
	id := strings.TrimSpace(env.TurnID)
	if _, retired := t.retiredTurns[id]; id != "" && retired {
		return false
	}
	running := env.Kind == eventstream.KindLifecycle && env.Lifecycle != nil && env.Lifecycle.State == eventstream.LifecycleStateRunning
	if running && (!m.turnRunning() || (id != "" && t.turnID != "" && id != t.turnID)) {
		if !env.OccurredAt.IsZero() && !t.turnStartedAt.IsZero() && env.OccurredAt.Before(t.turnStartedAt) {
			return false
		}
		m.beginLiveTurn(SubmissionModeDefault, true, env.OccurredAt)
	}
	if !m.turnRunning() {
		return false
	}
	if id != "" && t.turnID != "" && id != t.turnID {
		return false
	}
	if id != "" {
		t.turnID = id
	}
	return true
}

// acceptActivityPosition filters duplicate/out-of-order main feed deliveries.
// The existing broker positions order each lane; timestamps cover ACP peers
// without positions. Task streams have independent positions and never advance
// this foreground watermark.
func (t *runningHintTracker) acceptActivityPosition(env eventstream.Envelope) bool {
	if env.ParentTool != nil || (env.Scope != "" && env.Scope != eventstream.ScopeMain) {
		return true
	}
	if env.Position != nil {
		if p := env.Position.Durable; p != nil {
			if previous := t.activityDurable; previous != nil && (p.Seq < previous.Seq || p.Seq == previous.Seq && p.ProjectionIndex <= previous.ProjectionIndex) {
				return false
			}
			copy := *p
			t.activityDurable = &copy
		}
		if p := env.Position.Transient; p != nil {
			if t.activityTransientGeneration == p.Generation && p.Sequence <= t.activityTransientSequence {
				return false
			}
			t.activityTransientGeneration, t.activityTransientSequence = p.Generation, p.Sequence
		}
	}
	if !env.OccurredAt.IsZero() {
		if !t.activityAt.IsZero() && env.OccurredAt.Before(t.activityAt) {
			return false
		}
		t.activityAt = env.OccurredAt
	}
	return true
}
