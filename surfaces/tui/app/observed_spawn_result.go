package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// applyObservedSpawnResults closes Spawn presentation owners from the same
// normalized terminal Task observation used by ACP live projection and
// session/load replay. The Task control invocation remains a separate event;
// only a terminal observed child state can close the owner.
func (m *Model) applyObservedSpawnResults(results []acpprojector.SpawnTaskResult) tea.Cmd {
	if m == nil || m.doc == nil || len(results) == 0 {
		return nil
	}
	changed := false
	for _, result := range results {
		parentCallID := strings.TrimSpace(result.ParentCallID)
		if parentCallID == "" {
			continue
		}
		taskHandle := display.ToolTaskHandle(nil, result.RawOutput, nil)
		owner, ok := m.openMainSpawnOwner(parentCallID, taskHandle)
		if !ok {
			continue
		}
		state := display.MapString(result.RawOutput, "state")
		output := display.SubagentTaskFinalText(state, result.RawOutput)
		failed := strings.EqualFold(strings.TrimSpace(result.Status), schema.ToolStatusFailed)
		owner.block.sealNarrativeSegment()
		finalEvent := SubagentEvent{
			Kind:          SEToolCall,
			CallID:        parentCallID,
			Output:        output,
			OutputMessage: output,
			Done:          true,
			Err:           failed,
			TaskHandle:    taskHandle,
		}
		mergeOpenFinalToolEvent(&owner.block.Events[owner.eventIndex], &finalEvent, true)
		updateToolEventIndex(owner.block.toolEventIndex, owner.block.Events, parentCallID)
		m.runningActivityTracker.complete(owner.activity.Key)
		m.refreshRunningActivity()
		m.markViewportBlockDirty(owner.block.BlockID())
		changed = true
	}
	if !changed {
		return nil
	}
	return m.requestStreamViewportSync()
}

type openSpawnOwner struct {
	block      *MainACPTurnBlock
	eventIndex int
	activity   runningActivityOwner
}

// openMainSpawnOwner resolves the exact still-open presentation owner for a
// durable Task-wait fallback. Tool-call IDs may be reused across Turns, while a
// public Task handle is Session-unique. Prefer one exact handle match; when the
// handle is unavailable, accept only one unambiguous compatible owner.
func (m *Model) openMainSpawnOwner(callID string, handle string) (openSpawnOwner, bool) {
	if m == nil || m.doc == nil {
		return openSpawnOwner{}, false
	}
	callID = strings.TrimSpace(callID)
	handle = normalizeRunningActivityHandle(handle)
	candidates := m.runningActivityTracker.observedOwnerCandidates(handle, callID)
	var exact []openSpawnOwner
	var compatible []openSpawnOwner
	candidateCount := 0
	for _, activity := range candidates {
		block, _ := m.doc.Find(activity.BlockID).(*MainACPTurnBlock)
		if block == nil {
			continue
		}
		for _, eventIndex := range openSpawnEventIndexes(block, callID) {
			candidateCount++
			owner := openSpawnOwner{
				block:      block,
				eventIndex: eventIndex,
				activity:   activity,
			}
			ownerHandle := normalizeRunningActivityHandle(block.Events[eventIndex].TaskHandle)
			if handle != "" && ownerHandle != "" {
				if ownerHandle == handle {
					exact = append(exact, owner)
				}
				continue
			}
			compatible = append(compatible, owner)
		}
	}
	if handle != "" {
		switch len(exact) {
		case 1:
			return exact[0], true
		case 0:
			if candidateCount == 1 && len(compatible) == 1 {
				return compatible[0], true
			}
		}
		return openSpawnOwner{}, false
	}
	if len(compatible) != 1 {
		return openSpawnOwner{}, false
	}
	return compatible[0], true
}

func openSpawnEventIndexes(block *MainACPTurnBlock, callID string) []int {
	if block == nil {
		return nil
	}
	callID = strings.TrimSpace(callID)
	var indexes []int
	for eventIndex := len(block.Events) - 1; eventIndex >= 0; eventIndex-- {
		event := block.Events[eventIndex]
		if event.Kind != SEToolCall ||
			event.Done ||
			strings.TrimSpace(event.CallID) != callID ||
			!strings.EqualFold(toolSemanticName(event.Name, event.ToolKind), "SPAWN") {
			continue
		}
		indexes = append(indexes, eventIndex)
	}
	return indexes
}
