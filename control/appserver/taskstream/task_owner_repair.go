package taskstream

import (
	"maps"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// TaskOwnerRepairs groups presentation repairs derived from one canonical
// terminal Task read or wait observation.
type TaskOwnerRepairs struct {
	Spawns   []SpawnTaskResult
	Commands []CommandTaskResult
}

// Empty reports whether the observation repairs no presentation owner.
func (r TaskOwnerRepairs) Empty() bool {
	return len(r.Spawns) == 0 && len(r.Commands) == 0
}

// Append preserves observation order while merging another projected batch.
func (r *TaskOwnerRepairs) Append(next TaskOwnerRepairs) {
	if r == nil {
		return
	}
	r.Spawns = append(r.Spawns, next.Spawns...)
	r.Commands = append(r.Commands, next.Commands...)
}

// Clone returns an independently appendable repair batch.
func (r TaskOwnerRepairs) Clone() TaskOwnerRepairs {
	return TaskOwnerRepairs{
		Spawns:   append([]SpawnTaskResult(nil), r.Spawns...),
		Commands: append([]CommandTaskResult(nil), r.Commands...),
	}
}

// TaskOwnerRepairsFromEnvelope normalizes one terminal Task observation once
// and derives the target-specific Spawn and RunCommand presentation repairs.
func TaskOwnerRepairsFromEnvelope(env eventstream.Envelope) TaskOwnerRepairs {
	observations := terminalTaskObservationsFromEnvelope(env)
	return TaskOwnerRepairs{
		Spawns:   spawnTaskResultsFromObservations(observations),
		Commands: commandTaskResultsFromObservations(observations),
	}
}

type terminalTaskObservation struct {
	ParentCallID   string
	ParentTool     string
	TargetKind     string
	Handle         string
	ObserverStatus string
	RawOutput      map[string]any
}

// terminalTaskObservationsFromEnvelope is the single Task read/wait envelope
// walk. Target adapters decide whether a terminal observation repairs a Spawn
// transcript owner or only a RunCommand activity owner.
func terminalTaskObservationsFromEnvelope(env eventstream.Envelope) []terminalTaskObservation {
	if env.Kind != eventstream.KindSessionUpdate || env.Update == nil ||
		(env.Scope != "" && env.Scope != eventstream.ScopeMain) {
		return nil
	}
	update, ok := eventstream.CloneUpdate(env.Update).(eventstream.ToolCallUpdate)
	if !ok || !taskObserverStatusFinal(update.Status) {
		return nil
	}
	rawInput := session.NormalizeProtocolRawMap(update.RawInput)
	rawOutput := session.NormalizeProtocolRawMap(update.RawOutput)
	if len(rawInput) == 0 || len(rawOutput) == 0 {
		return nil
	}
	switch display.ToolTaskAction(rawInput, rawOutput, update.Meta) {
	case "read", "wait":
	default:
		return nil
	}
	observerStatus := strings.TrimSpace(*update.Status)
	if tasks, batch := taskBatchOutputs(rawOutput["tasks"]); batch {
		out := make([]terminalTaskObservation, 0, len(tasks))
		type observedTaskIdentity struct {
			parentCallID string
			parentTool   string
			handle       string
		}
		seen := make(map[observedTaskIdentity]struct{}, len(tasks))
		for _, taskOutput := range tasks {
			observation, valid := terminalBatchTaskObservation(taskOutput, observerStatus)
			if !valid {
				continue
			}
			if observation.Handle != "" {
				key := observedTaskIdentity{
					parentCallID: observation.ParentCallID,
					parentTool:   observation.ParentTool,
					handle:       observation.Handle,
				}
				if _, duplicate := seen[key]; duplicate {
					continue
				}
				seen[key] = struct{}{}
			}
			out = append(out, observation)
		}
		return out
	}
	observation, valid := terminalSingularTaskObservation(env, update.ToolCallID, rawOutput, observerStatus)
	if !valid {
		return nil
	}
	return []terminalTaskObservation{observation}
}

func terminalBatchTaskObservation(rawOutput map[string]any, observerStatus string) (terminalTaskObservation, bool) {
	parentCallID := strings.TrimSpace(display.MapString(rawOutput, "parent_call"))
	parentTool := strings.TrimSpace(display.MapString(rawOutput, "parent_tool"))
	state := strings.TrimSpace(display.MapString(rawOutput, "state"))
	if parentCallID == "" || parentTool == "" ||
		(!eventstream.IsTerminalLifecycleState(state) && !taskOutputHasFinalResponses(rawOutput)) {
		return terminalTaskObservation{}, false
	}
	return newTerminalTaskObservation(parentCallID, parentTool, rawOutput, observerStatus), true
}

func terminalSingularTaskObservation(
	env eventstream.Envelope,
	observerCallID string,
	rawOutput map[string]any,
	observerStatus string,
) (terminalTaskObservation, bool) {
	if env.ParentTool == nil {
		return terminalTaskObservation{}, false
	}
	parentCallID := strings.TrimSpace(env.ParentTool.ToolCallID)
	parentTool := strings.TrimSpace(env.ParentTool.ToolName)
	state := strings.TrimSpace(display.MapString(rawOutput, "state"))
	if parentCallID == "" || parentTool == "" ||
		strings.TrimSpace(observerCallID) == parentCallID ||
		(!eventstream.IsTerminalLifecycleState(state) && !taskOutputHasFinalResponses(rawOutput)) {
		return terminalTaskObservation{}, false
	}
	if rawParentCall := strings.TrimSpace(display.MapString(rawOutput, "parent_call")); rawParentCall != "" && rawParentCall != parentCallID {
		return terminalTaskObservation{}, false
	}
	if rawParentTool := strings.TrimSpace(display.MapString(rawOutput, "parent_tool")); rawParentTool != "" &&
		rawParentTool != parentTool {
		return terminalTaskObservation{}, false
	}
	return newTerminalTaskObservation(parentCallID, parentTool, rawOutput, observerStatus), true
}

func taskOutputHasFinalResponses(rawOutput map[string]any) bool {
	responses, ok := taskBatchOutputs(rawOutput["final_responses"])
	if !ok {
		return false
	}
	for _, response := range responses {
		if strings.TrimSpace(display.MapString(response, "final_message")) != "" {
			return true
		}
	}
	return false
}

func newTerminalTaskObservation(
	parentCallID string,
	parentTool string,
	rawOutput map[string]any,
	observerStatus string,
) terminalTaskObservation {
	return terminalTaskObservation{
		ParentCallID:   strings.TrimSpace(parentCallID),
		ParentTool:     strings.TrimSpace(parentTool),
		TargetKind:     strings.ToLower(strings.TrimSpace(display.ToolTaskTargetKind(nil, rawOutput, nil))),
		Handle:         taskapi.NormalizeHandle(display.MapString(rawOutput, "handle")),
		ObserverStatus: strings.TrimSpace(observerStatus),
		RawOutput:      maps.Clone(rawOutput),
	}
}

func taskBatchOutputs(value any) ([]map[string]any, bool) {
	switch tasks := value.(type) {
	case []any:
		out := make([]map[string]any, 0, len(tasks))
		for _, taskOutput := range tasks {
			if mapped, ok := taskOutput.(map[string]any); ok {
				out = append(out, mapped)
			}
		}
		return out, true
	case []map[string]any:
		return tasks, true
	default:
		return nil, false
	}
}

func taskObserverStatusFinal(status *string) bool {
	if status == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(*status)) {
	case eventstream.ToolStatusCompleted, eventstream.ToolStatusFailed, "interrupted", "cancelled", "canceled", "terminated", "timed_out", "timeout":
		return true
	default:
		return false
	}
}
