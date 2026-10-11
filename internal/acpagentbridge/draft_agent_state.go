package acpagentbridge

import "github.com/caelis-labs/caelis/control/appserver"

func (s *draftSession) emitCurrentRuntimeState() error {
	s.projectionMu.Lock()
	defer s.projectionMu.Unlock()
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if previous := s.lastForeground; previous != nil {
		// Replacing a connection's observation must preserve already published
		// identity and completion evidence. Inspection supplies neither a new
		// command Activity nor a foreground terminal reason.
		if !state.Run.Active || (!previous.active && state.Run.TurnID == previous.activityID) {
			return s.emitForegroundStateLocked(*previous)
		}
	}
	return s.emitRuntimeStateLocked(state, "")
}

func (s *draftSession) stateInspectionVersion() uint64 {
	s.projectionMu.Lock()
	defer s.projectionMu.Unlock()
	return s.stateVersion
}

// A passive inspection supplies approval details, never foreground completion.
// The live native lifecycle owns Activity identity, order and terminal reason.
// Callers hold projectionMu, including notification publication.
func (s *draftSession) applyApprovalSnapshotLocked(snapshot appserver.SessionState, version uint64) (appserver.SessionState, bool) {
	if version != s.stateVersion {
		return appserver.SessionState{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state
	if snapshot.Run.TurnID != current.Run.TurnID || (!current.Run.Active && snapshot.Run.Active) {
		return current, false
	}
	// A FIFO head owned by a background task remains independently resolvable.
	// An old head owned by the settled foreground must not be displayed again.
	if !current.Run.Active && snapshot.Approval.Active != nil && snapshot.Approval.Active.Target.TurnID == current.Run.TurnID {
		snapshot.Approval.Active = nil
	}
	snapshot.Run = current.Run
	snapshot.Run.WaitingApproval = snapshot.Run.Active && snapshot.Approval.Active != nil && snapshot.Approval.Active.Target.TurnID == snapshot.Run.TurnID
	s.state = snapshot
	s.approvalObserved = snapshot.Approval.Active != nil
	s.stateVersion++
	return snapshot, true
}
