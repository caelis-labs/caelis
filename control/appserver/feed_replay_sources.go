package appserver

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/internal/eventmeta"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// freshReplayContentKey identifies one append-only content stream. It contains
// no byte position: fresh replay selects one complete source by typed semantic
// identity and never compares, trims, or merges the source payloads.
type freshReplayContentKey struct {
	sessionID   string
	scope       eventstream.Scope
	scopeID     string
	participant string
	updateType  string
	logicalID   string
}

type freshReplaySelectedSource struct {
	position   eventstream.DurableFeedPosition
	selected   bool
	parentTool *eventstream.ParentToolRelation
}

// freshReplayCompleteSources collects complete materializations already held
// by the captured ring. The backfill plan adds durable storage projections to
// the same set before selecting the one replay source.
func freshReplayCompleteSources(ring []feedRingItem) map[freshReplayContentKey]struct{} {
	complete := make(map[freshReplayContentKey]struct{})
	for _, item := range ring {
		if isDurableFeedEnvelope(item.envelope) {
			addFreshReplayCompleteSource(complete, item.envelope)
		}
	}
	return complete
}

func addFreshReplayCompleteSource(complete map[freshReplayContentKey]struct{}, envelope eventstream.Envelope) {
	if key, ok := freshReplayNarrativeKey(envelope); ok {
		complete[key] = struct{}{}
	}
	if terminalMaterializationComplete(envelope) {
		for _, key := range freshReplayTerminalKeys(envelope) {
			complete[key] = struct{}{}
		}
	}
}

func addFreshReplayDurableCompleteSource(
	selected map[freshReplayContentKey]freshReplaySelectedSource,
	envelope eventstream.Envelope,
) {
	if envelope.Position == nil || envelope.Position.Durable == nil {
		return
	}
	position := *envelope.Position.Durable
	if narrativeKey, ok := freshReplayNarrativeKey(envelope); ok {
		selectFreshReplayDurableSource(selected, narrativeKey, position, nil)
		return
	}
	keys := freshReplayTerminalKeys(envelope)
	if len(keys) == 0 {
		return
	}
	owner := freshReplayTerminalOwner(envelope)
	complete := terminalMaterializationComplete(envelope)
	for _, key := range keys {
		current := selected[key]
		if owner != nil {
			current.parentTool = owner
			selected[key] = current
		}
		if complete {
			selectFreshReplayDurableSource(selected, key, position, owner)
		}
	}
}

func selectFreshReplayDurableSource(
	selected map[freshReplayContentKey]freshReplaySelectedSource,
	key freshReplayContentKey,
	position eventstream.DurableFeedPosition,
	owner *eventstream.ParentToolRelation,
) {
	current := selected[key]
	if !current.selected || compareDurablePosition(position, current.position) > 0 {
		current.position = position
		current.selected = true
	}
	if owner != nil {
		current.parentTool = owner
	}
	selected[key] = current
}

// selectFreshReplaySources removes retained live deltas when durable storage
// or the captured ring contains a complete materialization for the same typed
// content stream. Durable ring items covered by the storage checkpoint are
// also omitted so a live state-only supplement cannot shadow the full stored
// projection at the same durable position. Cursor resumes do not use this
// selection because the receiver may already have consumed a prefix.
func selectFreshReplaySources(
	ring []feedRingItem,
	complete map[freshReplayContentKey]struct{},
	storageThrough uint64,
) []feedRingItem {
	out := make([]feedRingItem, 0, len(ring))
	for _, item := range ring {
		if isDurableFeedEnvelope(item.envelope) {
			if item.envelope.Position.Durable.Seq <= storageThrough {
				continue
			}
			out = append(out, item)
			continue
		}
		if key, ok := freshReplayNarrativeKey(item.envelope); ok {
			if _, materialized := complete[key]; materialized {
				continue
			}
		}
		if keys := freshReplayTerminalKeys(item.envelope); len(keys) > 0 {
			if freshReplayAnyCompleteSource(complete, keys) {
				continue
			}
		}
		out = append(out, item)
	}
	return out
}

func freshReplayAnyCompleteSource(complete map[freshReplayContentKey]struct{}, keys []freshReplayContentKey) bool {
	for _, key := range keys {
		if _, ok := complete[key]; ok {
			return true
		}
	}
	return false
}

func freshReplayNarrativeKey(envelope eventstream.Envelope) (freshReplayContentKey, bool) {
	chunk, ok := envelope.Update.(schema.ContentChunk)
	if !ok {
		return freshReplayContentKey{}, false
	}
	updateType := strings.TrimSpace(chunk.SessionUpdate)
	if updateType != schema.UpdateAgentMessage && updateType != schema.UpdateAgentThought {
		return freshReplayContentKey{}, false
	}
	messageID := strings.TrimSpace(chunk.MessageID)
	if messageID == "" {
		return freshReplayContentKey{}, false
	}
	return freshReplayEnvelopeKey(envelope, updateType, messageID), true
}

func freshReplayTerminalKey(envelope eventstream.Envelope) (freshReplayContentKey, bool) {
	var toolCallID string
	switch update := envelope.Update.(type) {
	case schema.ToolCall:
		toolCallID = update.ToolCallID
	case schema.ToolCallUpdate:
		toolCallID = update.ToolCallID
	default:
		return freshReplayContentKey{}, false
	}
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return freshReplayContentKey{}, false
	}
	terminalID, _, ok := freshReplayTerminalMaterialization(envelope)
	if !ok {
		return freshReplayContentKey{}, false
	}
	if terminalID != "" {
		toolCallID = terminalID
	} else if envelope.ParentTool != nil && envelope.ParentTool.ToolName == shell.RunCommandToolName {
		toolCallID = strings.TrimSpace(envelope.ParentTool.ToolCallID)
	}
	if toolCallID == "" {
		return freshReplayContentKey{}, false
	}
	return freshReplayEnvelopeKey(envelope, schema.UpdateToolCallInfo, toolCallID), true
}

func freshReplayTerminalKeys(envelope eventstream.Envelope) []freshReplayContentKey {
	primary, ok := freshReplayTerminalKey(envelope)
	if !ok {
		return nil
	}
	logicalIDs := []string{primary.logicalID, freshReplayEnvelopeToolCallID(envelope)}
	if envelope.ParentTool != nil {
		logicalIDs = append(logicalIDs, strings.TrimSpace(envelope.ParentTool.ToolCallID))
	}
	keys := make([]freshReplayContentKey, 0, len(logicalIDs))
	seen := make(map[string]struct{}, len(logicalIDs))
	for _, logicalID := range logicalIDs {
		logicalID = strings.TrimSpace(logicalID)
		if logicalID == "" {
			continue
		}
		if _, exists := seen[logicalID]; exists {
			continue
		}
		seen[logicalID] = struct{}{}
		key := primary
		key.logicalID = logicalID
		keys = append(keys, key)
	}
	return keys
}

func freshReplayEnvelopeKey(envelope eventstream.Envelope, updateType, logicalID string) freshReplayContentKey {
	scope := envelope.Scope
	if scope == "" {
		scope = eventstream.ScopeMain
	}
	scopeID := strings.TrimSpace(envelope.ScopeID)
	participant := strings.TrimSpace(envelope.ParticipantID)
	if scope == eventstream.ScopeMain {
		// Main live transport and durable Runtime events can carry different
		// Turn-derived ScopeIDs. The typed message/tool identity is the stable
		// logical content identity; transport Turn IDs are not replay identity.
		scopeID = ""
		participant = ""
	}
	return freshReplayContentKey{
		sessionID:   strings.TrimSpace(envelope.SessionID),
		scope:       scope,
		scopeID:     scopeID,
		participant: participant,
		updateType:  strings.TrimSpace(updateType),
		logicalID:   strings.TrimSpace(logicalID),
	}
}

func terminalMaterializationComplete(envelope eventstream.Envelope) bool {
	var status string
	var rawOutput map[string]any
	switch update := envelope.Update.(type) {
	case schema.ToolCall:
		status = update.Status
		rawOutput = schema.NormalizeRawMap(update.RawOutput)
	case schema.ToolCallUpdate:
		if update.Status != nil {
			status = *update.Status
		}
		rawOutput = schema.NormalizeRawMap(update.RawOutput)
	}
	_, output, ok := freshReplayTerminalMaterialization(envelope)
	if !ok || !terminalMaterializationStartsAtZero(freshReplayTerminalMeta(envelope), output) {
		return false
	}
	targetKind, _ := rawOutput["target_kind"].(string)
	if (envelope.ParentTool != nil && envelope.ParentTool.ToolName == shell.RunCommandToolName) ||
		strings.EqualFold(strings.TrimSpace(targetKind), "command") || strings.EqualFold(strings.TrimSpace(targetKind), "terminal") {
		targetState, _ := rawOutput["state"].(string)
		return eventstream.IsTerminalLifecycleState(targetState)
	}
	return eventstream.IsTerminalLifecycleState(status)
}

func freshReplayTerminalMaterialization(envelope eventstream.Envelope) (terminalID string, output string, ok bool) {
	meta := freshReplayTerminalMeta(envelope)
	terminalID = metautil.String(
		meta,
		metautil.Root,
		metautil.Runtime,
		metautil.RuntimeTask,
		metautil.RuntimeTaskTerminalID,
	)
	if terminalOutput, found := metautil.TerminalOutput(meta); found {
		if terminalID == "" {
			terminalID = strings.TrimSpace(terminalOutput.TerminalID)
		}
		return terminalID, terminalOutput.Data, true
	}
	output, ok = metautil.RuntimeSection(meta, metautil.RuntimeTask)[metautil.RuntimeOutputDelta].(string)
	return terminalID, output, ok
}

func freshReplayTerminalMeta(envelope eventstream.Envelope) map[string]any {
	var updateMeta map[string]any
	switch update := envelope.Update.(type) {
	case schema.ToolCall:
		updateMeta = update.Meta
	case schema.ToolCallUpdate:
		updateMeta = update.Meta
	}
	return metautil.Merge(envelope.Meta, updateMeta)
}

func freshReplayTerminalOwner(envelope eventstream.Envelope) *eventstream.ParentToolRelation {
	var callID string
	var input, output map[string]any
	switch update := envelope.Update.(type) {
	case schema.ToolCall:
		callID = update.ToolCallID
		input, _ = update.RawInput.(map[string]any)
		output, _ = update.RawOutput.(map[string]any)
	case schema.ToolCallUpdate:
		callID = update.ToolCallID
		input, _ = update.RawInput.(map[string]any)
		output, _ = update.RawOutput.(map[string]any)
	}
	if callID = strings.TrimSpace(callID); callID == "" {
		return nil
	}
	meta := freshReplayTerminalMeta(envelope)
	terminalInfo, hasTerminalInfo := metautil.TerminalInfo(meta)
	if !strings.EqualFold(display.ToolTaskTargetKind(input, output, meta), string(task.KindCommand)) ||
		!hasTerminalInfo || strings.TrimSpace(terminalInfo.TerminalID) != callID {
		return nil
	}
	return &eventstream.ParentToolRelation{ToolCallID: callID, ToolName: shell.RunCommandToolName}
}

func terminalMaterializationStartsAtZero(meta map[string]any, data string) bool {
	start, hasStart := metautil.Int64(
		meta,
		metautil.Root,
		metautil.Runtime,
		metautil.RuntimeTask,
		metautil.RuntimeOutputStart,
	)
	if hasStart && start != 0 {
		return false
	}
	cursor, hasCursor := metautil.Int64(
		meta,
		metautil.Root,
		metautil.Runtime,
		metautil.RuntimeTask,
		metautil.RuntimeOutputCursor,
	)
	return !hasCursor || cursor == int64(len([]byte(data)))
}

func selectFreshReplayDurableEnvelope(
	envelope eventstream.Envelope,
	selected map[freshReplayContentKey]freshReplaySelectedSource,
) (eventstream.Envelope, bool) {
	if envelope.Position == nil || envelope.Position.Durable == nil {
		return envelope, true
	}
	position := *envelope.Position.Durable
	if key, ok := freshReplayNarrativeKey(envelope); ok {
		if chosen, found := selected[key]; found && chosen.selected && compareDurablePosition(position, chosen.position) != 0 {
			return eventstream.Envelope{}, false
		}
		return envelope, true
	}
	keys := freshReplayTerminalKeys(envelope)
	if len(keys) == 0 {
		return envelope, true
	}
	chosen, found := freshReplaySelectedSourceForKeys(selected, keys)
	if !found || !chosen.selected || compareDurablePosition(position, chosen.position) == 0 {
		if found && chosen.parentTool != nil && envelope.ParentTool == nil && freshReplayEnvelopeToolCallID(envelope) != chosen.parentTool.ToolCallID {
			parent := *chosen.parentTool
			envelope.ParentTool = &parent
		}
		return envelope, true
	}
	switch update := envelope.Update.(type) {
	case schema.ToolCall:
		update.Meta = withoutFreshReplayTerminalContent(update.Meta)
		envelope.Update = update
	case schema.ToolCallUpdate:
		update.Meta = withoutFreshReplayTerminalContent(update.Meta)
		envelope.Update = update
	}
	envelope.Meta = withoutFreshReplayTerminalContent(envelope.Meta)
	return envelope, true
}

func freshReplaySelectedSourceForKeys(
	selected map[freshReplayContentKey]freshReplaySelectedSource,
	keys []freshReplayContentKey,
) (freshReplaySelectedSource, bool) {
	var out freshReplaySelectedSource
	found := false
	for _, key := range keys {
		candidate, ok := selected[key]
		if !ok {
			continue
		}
		if candidate.parentTool != nil && out.parentTool == nil {
			parent := *candidate.parentTool
			out.parentTool = &parent
		}
		if candidate.selected && (!out.selected || compareDurablePosition(candidate.position, out.position) > 0) {
			parent := out.parentTool
			out = candidate
			if out.parentTool == nil {
				out.parentTool = parent
			}
		}
		found = true
	}
	return out, found
}

func freshReplayEnvelopeToolCallID(envelope eventstream.Envelope) string {
	switch update := envelope.Update.(type) {
	case schema.ToolCall:
		return strings.TrimSpace(update.ToolCallID)
	case schema.ToolCallUpdate:
		return strings.TrimSpace(update.ToolCallID)
	default:
		return ""
	}
}

func withoutFreshReplayTerminalContent(meta map[string]any) map[string]any {
	meta = metautil.WithoutTerminalOutput(meta)
	return eventmeta.WithoutRuntimeSectionKeys(meta, metautil.RuntimeTask, metautil.RuntimeOutputDelta)
}
