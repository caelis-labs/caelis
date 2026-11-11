package file

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

// Cancellation may settle the current activity or reserve its already admitted
// next generation. It never opens an activity, changes its identity, or rewrites
// the immutable spawn specification.
func validateSubagentCancellation(current, next *taskapi.Entry) error {
	generation := taskLifecycleGeneration(current.Spec["turn_seq"])
	cancelGeneration := taskLifecycleGeneration(next.Spec["cancel_turn_seq"])
	phase, _ := next.Spec["cancel_phase"].(string)
	if generation <= 0 || taskLifecycleGeneration(next.Spec["turn_seq"]) != generation ||
		cancelGeneration < generation || cancelGeneration > generation+1 ||
		taskLifecycleGeneration(next.Metadata["cancel_turn_seq"]) != cancelGeneration || next.Metadata["cancel_phase"] != phase {
		return fmt.Errorf("agent-sdk/session/file: subagent cancellation requires the current Task generation and matching cancellation journal")
	}
	switch phase {
	case "subagent_cancel_claimed", "subagent_cancel_unknown_outcome", "subagent_cancel_effect_applied", "subagent_cancel_completed":
	default:
		return fmt.Errorf("agent-sdk/session/file: subagent cancellation has an invalid journal phase")
	}
	if next.Running && (next.State != taskapi.StateUnknownOutcome || phase == "subagent_cancel_completed") ||
		!next.Running && !detachedSubagentCompletionState(next.State) {
		return fmt.Errorf("agent-sdk/session/file: subagent cancellation requires a pending or terminal Task outcome")
	}
	if previous, _ := current.Metadata["cancel_phase"].(string); previous != "" {
		if taskLifecycleGeneration(current.Metadata["cancel_turn_seq"]) != cancelGeneration || previous == "subagent_cancel_completed" && previous != phase {
			return fmt.Errorf("agent-sdk/session/file: subagent cancellation cannot replace a claimed generation or reopen completion")
		}
	}
	before, after := taskapi.CloneEntry(current), taskapi.CloneEntry(next)
	after.State, after.Running = before.State, before.Running
	after.SupportsInput, after.SupportsCancel = before.SupportsInput, before.SupportsCancel
	after.Result, after.FailureDiagnostic = before.Result, before.FailureDiagnostic
	after.UpdatedAt = before.UpdatedAt
	for _, entry := range []*taskapi.Entry{before, after} {
		entry.CreatedAt = entry.CreatedAt.UTC()
		entry.Lease.AcquiredAt = entry.Lease.AcquiredAt.UTC()
		entry.Lease.HeartbeatAt = entry.Lease.HeartbeatAt.UTC()
		entry.Lease.ExpiresAt = entry.Lease.ExpiresAt.UTC()
		for _, key := range []string{"cancel_phase", "cancel_turn_seq", "spawn_result"} {
			delete(entry.Spec, key)
		}
		for _, key := range []string{"cancel_phase", "cancel_turn_seq", "state", "running", "interrupted_reason"} {
			delete(entry.Metadata, key)
		}
	}
	// Runtime values may contain typed target structs while Store values contain
	// decoded maps. Compare their JSON values, retaining exact numeric identities.
	var normalized [2]any
	for i, entry := range []*taskapi.Entry{before, after} {
		raw, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&normalized[i]); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(normalized[0], normalized[1]) {
		return fmt.Errorf("agent-sdk/session/file: subagent cancellation cannot change Task identity, activity, or spawn specification")
	}
	return nil
}
