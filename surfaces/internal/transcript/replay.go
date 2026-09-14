package transcript

import (
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// ProjectReplayEvents presents the history selected by Control. Exact spool
// history may consist solely of transient deltas: filtering those by durability
// would discard content whose canonical final was suppressed during live delivery.
func ProjectReplayEvents(events []eventstream.Envelope, surface SurfaceProjector) []Event {
	out := make([]Event, 0, len(events))
	for _, env := range events {
		out = append(out, ProjectReplayEvent(env, surface)...)
	}
	return out
}

// ProjectReplayEvent presents one Control-selected history envelope without
// activating historical permission requests or compaction progress.
func ProjectReplayEvent(env eventstream.Envelope, surface SurfaceProjector) []Event {
	// Historical compaction progress and permission interactions are not replayed as
	// current UI actions. Reconnect bootstrap supplies active approvals separately.
	if env.Kind == eventstream.KindLifecycle && env.Lifecycle != nil && env.Lifecycle.State == "context_compacting" {
		return nil
	}
	switch env.Kind {
	case eventstream.KindSessionUpdate:
		if eventstream.UpdateType(env.Update) == eventstream.UpdateCompact {
			return nil
		}
	case eventstream.KindAgentCommunication, eventstream.KindLifecycle, eventstream.KindParticipant, eventstream.KindApprovalReview:
	default:
		return nil
	}
	return ProjectACPEventToEvents(env, surface)
}
