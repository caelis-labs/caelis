package loader

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// spawnReplayProjector reconstructs final-only stdio Spawn results from durable
// Session history. Child Task streams are transient, so a canonical terminal
// Task read/wait result is the historical source for a child FinalResponse when
// no durable parent Spawn result exists.
type spawnReplayKey struct {
	ToolCallID string
	TurnID     string
}

type spawnReplayTerminalFingerprint struct {
	Status string
	Text   string
}

type spawnReplayProjector struct {
	closed              map[spawnReplayKey]struct{}
	authoritative       map[spawnReplayKey]struct{}
	legacyAuthoritative map[string]map[spawnReplayTerminalFingerprint]int
}

func newSpawnReplayProjector(events []*session.Event) *spawnReplayProjector {
	p := &spawnReplayProjector{
		closed:              map[spawnReplayKey]struct{}{},
		authoritative:       map[spawnReplayKey]struct{}{},
		legacyAuthoritative: map[string]map[spawnReplayTerminalFingerprint]int{},
	}
	for _, event := range events {
		if key := finalSpawnEventKey(event); key.ToolCallID != "" {
			p.authoritative[key] = struct{}{}
			if key.TurnID == "" {
				fingerprints := p.legacyAuthoritative[key.ToolCallID]
				if fingerprints == nil {
					fingerprints = map[spawnReplayTerminalFingerprint]int{}
					p.legacyAuthoritative[key.ToolCallID] = fingerprints
				}
				fingerprints[finalSpawnEventFingerprint(event)]++
			}
		}
	}
	return p
}

func (p *spawnReplayProjector) normalize(
	event *session.Event,
	notification acp.SessionNotification,
) acp.SessionNotification {
	if p == nil {
		return notification
	}
	update, ok := notification.Update.(acp.ToolCallUpdate)
	if !ok || !toolStatusFinal(update.Status) || !sessionEventOwnsSpawnCall(event, update.ToolCallID) {
		return notification
	}
	rawOutput := schema.NormalizeRawMap(update.RawOutput)
	update = withSpawnReplayResult(update, rawOutput)
	p.closed[spawnReplayKeyForRawOutput(update.ToolCallID, rawOutput)] = struct{}{}
	notification.Update = update
	return notification
}

func (p *spawnReplayProjector) observedParentCloses(env eventstream.Envelope, sessionID string) []acp.SessionNotification {
	if p == nil {
		return nil
	}
	results := projector.SpawnTaskResultsFromEnvelope(env)
	if len(results) == 0 {
		return nil
	}
	out := make([]acp.SessionNotification, 0, len(results))
	for _, result := range results {
		parentCallID := strings.TrimSpace(result.ParentCallID)
		if parentCallID == "" {
			continue
		}
		key := spawnReplayKeyForRawOutput(parentCallID, result.RawOutput)
		if spawnReplaySetContains(p.closed, key) {
			if key.TurnID == "" {
				p.consumeLegacyFingerprint(parentCallID, result.Status, result.RawOutput)
			}
			continue
		}
		if p.authoritativeContains(key, result.Status, result.RawOutput) {
			continue
		}
		p.closed[key] = struct{}{}
		status := result.Status
		update := withSpawnReplayResult(acp.ToolCallUpdate{
			SessionUpdate: acp.UpdateToolCallInfo,
			ToolCallID:    parentCallID,
			Status:        &status,
			RawOutput:     result.RawOutput,
		}, result.RawOutput)
		out = append(out, acp.SessionNotification{
			SessionID: strings.TrimSpace(sessionID),
			Update:    update,
		})
	}
	return out
}

func withSpawnReplayResult(update acp.ToolCallUpdate, rawOutput map[string]any) acp.ToolCallUpdate {
	if strings.TrimSpace(update.ToolCallID) == "" {
		return update
	}
	status := ""
	if update.Status != nil {
		status = *update.Status
	}
	if toolStatusFinal(update.Status) {
		status = spawnReplayStatus(status, rawOutput)
		update.Status = &status
	}
	text := spawnReplayResultText(status, rawOutput)
	if strings.TrimSpace(text) == "" && !strings.EqualFold(strings.TrimSpace(status), acp.ToolStatusFailed) {
		if output, ok := metautil.TerminalOutput(update.Meta); ok {
			text = output.Data
		}
	}

	meta := metautil.WithoutTerminalOutput(update.Meta)
	delete(meta, metautil.TerminalInfoKey)
	delete(meta, metautil.TerminalExitKey)
	if len(meta) == 0 {
		meta = nil
	}
	update.Meta = meta

	content := make([]acp.ToolCallContent, 0, len(update.Content)+1)
	hasResult := false
	for _, item := range update.Content {
		if strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.Type), "content") {
			if strings.EqualFold(strings.TrimSpace(status), acp.ToolStatusFailed) {
				continue
			}
			hasResult = true
		}
		content = append(content, item)
	}
	if !hasResult && strings.TrimSpace(text) != "" {
		content = append(content, acp.ToolCallContent{
			Type:    "content",
			Content: acp.TextContent{Type: "text", Text: text},
		})
	}
	update.Content = content
	return update
}

func spawnReplayResultText(status string, rawOutput map[string]any) string {
	if strings.EqualFold(strings.TrimSpace(status), acp.ToolStatusFailed) {
		for _, key := range []string{"error", "reason"} {
			if text := display.MapString(rawOutput, key); strings.TrimSpace(text) != "" {
				return text
			}
		}
		return ""
	}
	return display.SubagentTaskFinalText(display.MapString(rawOutput, "state"), rawOutput)
}

func sessionEventOwnsSpawnCall(event *session.Event, toolCallID string) bool {
	toolCallID = strings.TrimSpace(toolCallID)
	if event == nil || toolCallID == "" {
		return false
	}
	if event.Tool != nil && strings.TrimSpace(event.Tool.ID) == toolCallID &&
		event.Tool.Name == spawn.ToolName {
		return true
	}
	message := event.Message
	if message == nil {
		if projected, ok := session.ModelMessageOf(event); ok {
			message = &projected
		}
	}
	if message == nil {
		return false
	}
	for _, call := range message.ToolCalls() {
		if strings.TrimSpace(call.ID) == toolCallID && call.Name == spawn.ToolName {
			return true
		}
	}
	response := message.ToolResponse()
	return response != nil && strings.TrimSpace(response.ID) == toolCallID &&
		response.Name == spawn.ToolName
}

func finalSpawnEventKey(event *session.Event) spawnReplayKey {
	if event == nil {
		return spawnReplayKey{}
	}
	if event.Tool != nil && event.Tool.Name == spawn.ToolName &&
		toolStatusFinalString(event.Tool.Status) {
		return spawnReplayKeyForRawOutput(event.Tool.ID, event.Tool.Output)
	}
	update := session.ProtocolUpdateOf(event)
	if update != nil && toolStatusFinalString(update.Status) &&
		sessionEventOwnsSpawnCall(event, update.ToolCallID) {
		return spawnReplayKeyForRawOutput(update.ToolCallID, update.RawOutput)
	}
	return spawnReplayKey{}
}

func spawnReplayKeyForRawOutput(toolCallID string, rawOutput map[string]any) spawnReplayKey {
	return spawnReplayKey{
		ToolCallID: strings.TrimSpace(toolCallID),
		TurnID:     strings.TrimSpace(display.MapString(rawOutput, "turn_id")),
	}
}

func spawnReplaySetContains(values map[spawnReplayKey]struct{}, key spawnReplayKey) bool {
	_, ok := values[key]
	return ok
}

// authoritativeContains pairs a legacy parent result without a Turn ID only
// with an observed result carrying the same terminal payload. A successful
// pairing adds an exact alias for repeated observations; it never makes the
// empty Turn ID a wildcard for later executions reusing the Spawn call ID.
func (p *spawnReplayProjector) authoritativeContains(key spawnReplayKey, status string, rawOutput map[string]any) bool {
	if p == nil {
		return false
	}
	if spawnReplaySetContains(p.authoritative, key) {
		if key.TurnID == "" {
			p.consumeLegacyFingerprint(key.ToolCallID, status, rawOutput)
		}
		return true
	}
	if key.ToolCallID == "" || key.TurnID == "" ||
		!p.consumeLegacyFingerprint(key.ToolCallID, status, rawOutput) {
		return false
	}
	p.authoritative[key] = struct{}{}
	return true
}

func (p *spawnReplayProjector) consumeLegacyFingerprint(toolCallID string, status string, rawOutput map[string]any) bool {
	if p == nil {
		return false
	}
	fingerprints := p.legacyAuthoritative[strings.TrimSpace(toolCallID)]
	fingerprint := spawnReplayFingerprint(status, rawOutput)
	if fingerprints[fingerprint] == 0 {
		return false
	}
	fingerprints[fingerprint]--
	return true
}

func spawnReplayFingerprint(status string, rawOutput map[string]any) spawnReplayTerminalFingerprint {
	status = spawnReplayStatus(status, rawOutput)
	return spawnReplayTerminalFingerprint{
		Status: status,
		Text:   strings.TrimSpace(spawnReplayResultText(status, rawOutput)),
	}
}

func finalSpawnEventFingerprint(event *session.Event) spawnReplayTerminalFingerprint {
	if event == nil {
		return spawnReplayTerminalFingerprint{}
	}
	if event.Tool != nil && event.Tool.Name == spawn.ToolName &&
		toolStatusFinalString(event.Tool.Status) {
		return spawnReplayFingerprint(event.Tool.Status, event.Tool.Output)
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		return spawnReplayFingerprint(update.Status, update.RawOutput)
	}
	return spawnReplayTerminalFingerprint{}
}

func spawnReplayStatus(status string, rawOutput map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(display.MapString(rawOutput, "state"))) {
	case "completed", "complete", "succeeded", "success", "done":
		return acp.ToolStatusCompleted
	case "failed", "interrupted", "cancelled", "canceled", "terminated", "timed_out", "timeout", "unknown_outcome":
		return acp.ToolStatusFailed
	}
	if strings.EqualFold(strings.TrimSpace(status), acp.ToolStatusCompleted) {
		return acp.ToolStatusCompleted
	}
	return acp.ToolStatusFailed
}

func toolStatusFinal(status *string) bool {
	if status == nil {
		return false
	}
	return toolStatusFinalString(*status)
}

func toolStatusFinalString(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case acp.ToolStatusCompleted, acp.ToolStatusFailed, "interrupted", "cancelled", "canceled", "terminated", "timed_out", "timeout", "unknown_outcome":
		return true
	default:
		return false
	}
}
