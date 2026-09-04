package runtime

import (
	"fmt"
	"reflect"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

// rebaseObservedSubagentTask keeps completion persistence revision-safe. It
// carries only Task lifecycle metadata; transient output belongs to Control's
// spool and never participates in this rebase.
func (tm *taskRuntime) rebaseObservedSubagentTask(task *subagentTask, current *taskapi.Entry) *taskapi.Entry {
	if tm == nil || task == nil || current == nil {
		return nil
	}
	task.activityApplyMu.Lock()
	task.mu.Lock()
	liveMetadata := session.CloneState(task.metadata)
	mergedMetadata := session.CloneState(current.Metadata)
	if mergedMetadata == nil {
		mergedMetadata = map[string]any{}
	}
	for key, value := range liveMetadata {
		mergedMetadata[key] = value
	}
	for _, key := range []string{
		"final_event_persisted", subagentCancelPhaseKey, subagentCancelTurnSeqKey,
		"continue_phase", "continue_prompt", "continue_context", "continue_digest", "continue_turn_seq", "continue_reason",
	} {
		if _, kept := liveMetadata[key]; !kept {
			delete(mergedMetadata, key)
		}
	}
	task.metadata = mergedMetadata
	task.revision = current.Revision
	task.lease = taskapi.CloneLease(current.Lease)
	rebased := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	task.activityApplyMu.Unlock()

	mergedSpec := session.CloneState(current.Spec)
	if mergedSpec == nil {
		mergedSpec = map[string]any{}
	}
	for key, value := range rebased.Spec {
		mergedSpec[key] = value
	}
	for _, key := range []string{subagentCancelPhaseKey, subagentCancelTurnSeqKey} {
		if _, kept := rebased.Spec[key]; !kept {
			delete(mergedSpec, key)
		}
	}
	for _, key := range []string{"continue_phase", "continue_digest", "continue_turn_seq"} {
		delete(mergedSpec, key)
	}
	rebased.Spec = mergedSpec
	return rebased
}

func validateTaskActivityTarget(task *subagentTask, target agent.ChildEndpointRef) error {
	if task == nil {
		return fmt.Errorf("child activity Task is unavailable")
	}
	target = agent.NormalizeChildEndpointRef(target)
	task.mu.Lock()
	want := agent.ChildEndpointRef{
		ParticipantID: strings.TrimSpace(task.anchor.AgentID),
		SessionID:     strings.TrimSpace(task.anchor.SessionID),
		EndpointKey:   strings.TrimSpace(task.ref.TaskID),
		Role:          subagentParticipantRole(task),
		Placement:     placement.Normalize(task.target.Placement),
	}
	task.mu.Unlock()
	if target.ParticipantID != want.ParticipantID || target.SessionID != want.SessionID ||
		target.EndpointKey != want.EndpointKey || target.Role != want.Role ||
		!reflect.DeepEqual(target.Placement, want.Placement) {
		return fmt.Errorf("child activity endpoint no longer matches its Task")
	}
	return nil
}
