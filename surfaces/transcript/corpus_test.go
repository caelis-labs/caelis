package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

const (
	corpusRawInputKey  = "__corpus_raw_input"
	corpusRawOutputKey = "__corpus_raw_output"
	corpusContentKey   = "__corpus_content"
	corpusLocationsKey = "__corpus_locations"
)

type transcriptCorpus struct {
	Version string                 `json:"version"`
	Cases   []transcriptCorpusCase `json:"cases"`
}

type transcriptCorpusCase struct {
	Name      string                    `json:"name"`
	Boundary  *transcriptCorpusBoundary `json:"boundary"`
	Envelopes []json.RawMessage         `json:"envelopes"`
	Expected  json.RawMessage           `json:"expected"`
}

type transcriptCorpusBoundary struct {
	ResumeMode     string `json:"resume_mode"`
	TransientGap   bool   `json:"transient_gap"`
	BoundaryCursor string `json:"boundary_cursor"`
}

type decodedCorpusEnvelope struct {
	Envelope eventstream.Envelope
	Usage    *corpusUsageUpdate
}

type corpusUsageUpdate struct {
	Size string         `json:"size"`
	Used string         `json:"used"`
	Cost map[string]any `json:"cost"`
}

type corpusWireEnvelope struct {
	Kind              eventstream.Kind                 `json:"kind"`
	Cursor            string                           `json:"cursor"`
	EventID           string                           `json:"event_id"`
	ProjectionID      string                           `json:"projection_id"`
	SessionID         string                           `json:"session_id"`
	HandleID          string                           `json:"handle_id"`
	RunID             string                           `json:"run_id"`
	TurnID            string                           `json:"turn_id"`
	Scope             eventstream.Scope                `json:"scope"`
	ScopeID           string                           `json:"scope_id"`
	Actor             string                           `json:"actor"`
	ParticipantID     string                           `json:"participant_id"`
	Final             bool                             `json:"final"`
	ParentTool        *eventstream.ParentToolRelation  `json:"parent_tool"`
	Delivery          *eventstream.Delivery            `json:"delivery"`
	ApprovalRequestID eventstream.ApprovalRequestID    `json:"approval_request_id"`
	Update            json.RawMessage                  `json:"update"`
	Permission        *schema.RequestPermissionRequest `json:"permission"`
	Notice            string                           `json:"notice"`
	ApprovalReview    *eventstream.ApprovalReview      `json:"approval_review"`
	Participant       *eventstream.Participant         `json:"participant"`
	Lifecycle         *eventstream.Lifecycle           `json:"lifecycle"`
	Meta              map[string]any                   `json:"_meta"`
	Error             string                           `json:"error"`
	Position          corpusWirePosition               `json:"position"`
}

type corpusWirePosition struct {
	Durable   *corpusWireDurablePosition   `json:"durable"`
	Transient *corpusWireTransientPosition `json:"transient"`
}

type corpusWireDurablePosition struct {
	Seq             string `json:"seq"`
	ProjectionIndex uint32 `json:"projection_index"`
}

type corpusWireTransientPosition struct {
	Anchor     corpusWireDurablePosition `json:"anchor"`
	Generation string                    `json:"generation"`
	Sequence   string                    `json:"sequence"`
}

func TestSharedTranscriptCorpusMatchesGoldenSemanticState(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "transcript-parity", "corpus.json"))
	if err != nil {
		t.Fatalf("read transcript corpus: %v", err)
	}
	var corpus transcriptCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("decode transcript corpus: %v", err)
	}
	if corpus.Version != "caelis.transcript-parity/v1" {
		t.Fatalf("corpus version = %q", corpus.Version)
	}

	for _, fixture := range corpus.Cases {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			t.Parallel()

			state := newCorpusTranscriptState()
			for _, rawEnvelope := range fixture.Envelopes {
				decoded := decodeTranscriptCorpusEnvelope(t, rawEnvelope)
				state.reduce(decoded)
			}
			if fixture.Boundary != nil {
				state.boundary = map[string]any{
					"resumeMode": fixture.Boundary.ResumeMode, "transientGap": fixture.Boundary.TransientGap,
				}
				if fixture.Boundary.BoundaryCursor != "" {
					state.boundary["boundaryCursor"] = fixture.Boundary.BoundaryCursor
				}
			}

			got := normalizeCorpusJSON(t, state.publicValue())
			var want any
			if err := json.Unmarshal(fixture.Expected, &want); err != nil {
				t.Fatalf("decode golden expectation: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				gotJSON, _ := json.MarshalIndent(got, "", "  ")
				wantJSON, _ := json.MarshalIndent(want, "", "  ")
				t.Fatalf("semantic state mismatch\ngot:\n%s\nwant:\n%s", gotJSON, wantJSON)
			}
		})
	}
}

func decodeTranscriptCorpusEnvelope(t *testing.T, raw json.RawMessage) decodedCorpusEnvelope {
	t.Helper()

	var wire corpusWireEnvelope
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("decode corpus Envelope: %v", err)
	}
	env := eventstream.Envelope{
		Kind: wire.Kind, Cursor: wire.Cursor, EventID: wire.EventID, ProjectionID: wire.ProjectionID,
		SessionID: wire.SessionID, HandleID: wire.HandleID, RunID: wire.RunID, TurnID: wire.TurnID,
		Scope: wire.Scope, ScopeID: wire.ScopeID, Actor: wire.Actor, ParticipantID: wire.ParticipantID,
		Final: wire.Final, ParentTool: wire.ParentTool, Delivery: wire.Delivery,
		ApprovalRequestID: wire.ApprovalRequestID, Permission: wire.Permission, Notice: wire.Notice,
		ApprovalReview: wire.ApprovalReview, Participant: wire.Participant, Lifecycle: wire.Lifecycle,
		Meta: wire.Meta, Error: wire.Error,
	}
	env.Position = decodeCorpusPosition(t, wire.Position)

	decoded := decodedCorpusEnvelope{Envelope: env}
	if len(wire.Update) == 0 {
		return decoded
	}
	var discriminator struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(wire.Update, &discriminator); err != nil {
		t.Fatalf("decode update discriminator: %v", err)
	}
	switch discriminator.SessionUpdate {
	case schema.UpdateUserMessage, schema.UpdateAgentMessage, schema.UpdateAgentThought, schema.UpdateCompact:
		decodeCorpusUpdate(t, wire.Update, &decoded.Envelope.Update)
	case schema.UpdateToolCall:
		var update schema.ToolCall
		decodeCorpusRaw(t, wire.Update, &update)
		decoded.Envelope.Update = update
	case schema.UpdateToolCallInfo:
		var update schema.ToolCallUpdate
		decodeCorpusRaw(t, wire.Update, &update)
		decoded.Envelope.Update = update
	case schema.UpdatePlan:
		var update schema.PlanUpdate
		decodeCorpusRaw(t, wire.Update, &update)
		decoded.Envelope.Update = update
	case schema.UpdateUsage:
		var usage corpusUsageUpdate
		decodeCorpusRaw(t, wire.Update, &usage)
		decoded.Usage = &usage
		decoded.Envelope.Update = schema.UsageUpdate{
			SessionUpdate: schema.UpdateUsage,
			Size:          parseCorpusUint64(t, usage.Size),
			Used:          parseCorpusUint64(t, usage.Used),
		}
	default:
		decoded.Envelope.Update = schema.RawUpdate{
			SessionUpdate: discriminator.SessionUpdate,
			Raw:           append(json.RawMessage(nil), wire.Update...),
		}
	}
	return decoded
}

func decodeCorpusUpdate(t *testing.T, raw json.RawMessage, target *schema.Update) {
	t.Helper()
	var update schema.ContentChunk
	decodeCorpusRaw(t, raw, &update)
	*target = update
}

func decodeCorpusRaw(t *testing.T, raw json.RawMessage, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode corpus value: %v", err)
	}
}

func decodeCorpusPosition(t *testing.T, wire corpusWirePosition) *eventstream.FeedPosition {
	t.Helper()
	position := &eventstream.FeedPosition{}
	if wire.Durable != nil {
		position.Durable = &eventstream.DurableFeedPosition{
			Seq: parseCorpusUint64(t, wire.Durable.Seq), ProjectionIndex: wire.Durable.ProjectionIndex,
		}
	}
	if wire.Transient != nil {
		position.Transient = &eventstream.TransientFeedPosition{
			Anchor: eventstream.DurableFeedPosition{
				Seq: parseCorpusUint64(t, wire.Transient.Anchor.Seq), ProjectionIndex: wire.Transient.Anchor.ProjectionIndex,
			},
			Generation: wire.Transient.Generation,
			Sequence:   parseCorpusUint64(t, wire.Transient.Sequence),
		}
	}
	return position
}

func parseCorpusUint64(t *testing.T, value string) uint64 {
	t.Helper()
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		t.Fatalf("parse corpus uint64 %q: %v", value, err)
	}
	return parsed
}

type corpusTranscriptState struct {
	items      []map[string]any
	seen       map[string]struct{}
	boundary   map[string]any
	usage      map[string]any
	activeTool string
}

func newCorpusTranscriptState() *corpusTranscriptState {
	return &corpusTranscriptState{items: []map[string]any{}, seen: map[string]struct{}{}}
}

func (s *corpusTranscriptState) reduce(decoded decodedCorpusEnvelope) {
	env := decoded.Envelope
	identity := corpusEnvelopeIdentity(env)
	if _, ok := s.seen[identity]; ok {
		return
	}
	s.seen[identity] = struct{}{}

	if env.Kind == eventstream.KindRequestPermission && env.Permission != nil {
		toolEnvelope := eventstream.CloneEnvelope(env)
		toolEnvelope.Kind = eventstream.KindSessionUpdate
		toolEnvelope.Update = env.Permission.ToolCall
		for _, event := range ProjectACPEventToEvents(toolEnvelope, corpusSurfaceProjector{}) {
			s.reduceProjected(env, identity+":tool", event)
		}
	}
	for _, event := range ProjectACPEventToEvents(env, corpusSurfaceProjector{}) {
		s.reduceProjected(env, identity, event)
	}
	if decoded.Usage != nil {
		s.usage = map[string]any{"size": decoded.Usage.Size, "used": decoded.Usage.Used}
		if len(decoded.Usage.Cost) > 0 {
			s.usage["cost"] = decoded.Usage.Cost
		}
	}
}

func (s *corpusTranscriptState) reduceProjected(env eventstream.Envelope, identity string, event Event) {
	switch event.Kind {
	case EventNarrative:
		s.reduceNarrative(env, identity, event)
	case EventTool:
		s.reduceTool(env, event)
	case EventPlan:
		entries := make([]map[string]any, 0, len(event.PlanEntries))
		for _, entry := range event.PlanEntries {
			entries = append(entries, map[string]any{"content": entry.Content, "status": entry.Status, "priority": entry.Priority})
		}
		item := corpusOrigin(env)
		item["entries"] = entries
		item["id"] = corpusPlanIdentity(env)
		item["kind"] = "plan"
		s.replaceOrAppend(item)
	case EventApprovalRequest:
		item := corpusOrigin(env)
		item["id"] = identity
		item["kind"] = "approval"
		item["requestId"] = event.ApprovalRequestID
		item["status"] = "requested"
		item["toolCallId"] = event.ToolCallID
		if event.ApprovalTool != "" {
			item["toolName"] = event.ApprovalTool
		}
		s.items = append(s.items, item)
		s.activeTool = event.ToolCallID
	case EventApproval:
		item := corpusOrigin(env)
		item["id"] = identity
		item["kind"] = "approval"
		item["status"] = event.ApprovalStatus
		corpusSet(item, "authorization", event.ApprovalAuth)
		corpusSet(item, "risk", event.ApprovalRisk)
		corpusSet(item, "text", event.ApprovalText)
		corpusSet(item, "toolCallId", event.ToolCallID)
		corpusSet(item, "toolName", event.ApprovalTool)
		s.items = append(s.items, item)
		if event.ToolCallID == s.activeTool {
			s.activeTool = ""
		}
	case EventParticipant:
		item := corpusOrigin(env)
		item["id"] = identity
		item["kind"] = "participant"
		corpusSet(item, "participantId", event.ParticipantID)
		item["state"] = event.State
		s.items = append(s.items, item)
	case EventLifecycle:
		item := corpusOrigin(env)
		item["id"] = identity
		item["kind"] = "lifecycle"
		item["state"] = event.State
		corpusSet(item, "reason", event.Reason)
		corpusSet(item, "stopReason", event.StopReason)
		s.items = append(s.items, item)
	case EventError:
		item := corpusOrigin(env)
		item["id"] = identity
		item["kind"] = "error"
		item["text"] = event.Text
		s.items = append(s.items, item)
	case EventRawExtension:
		item := corpusOrigin(env)
		item["id"] = identity
		item["kind"] = "raw_extension"
		item["sessionUpdate"] = event.RawSessionUpdate
		var update any
		if err := json.Unmarshal(event.RawUpdate, &update); err != nil {
			panic(err)
		}
		item["update"] = update
		s.items = append(s.items, item)
	}
}

func (s *corpusTranscriptState) reduceNarrative(env eventstream.Envelope, identity string, event Event) {
	role := string(event.NarrativeKind)
	id := identity
	if event.MessageID != "" {
		id = corpusMessageIdentity(env, event.MessageID)
	}
	item := corpusOrigin(env)
	item["final"] = event.Final
	item["id"] = id
	item["kind"] = "narrative"
	if event.MessageID != "" {
		item["messageId"] = event.MessageID
	}
	item["role"] = role
	item["text"] = event.Text
	if index := s.itemIndex(id); index >= 0 {
		item["text"] = s.items[index]["text"].(string) + event.Text
		item["final"] = s.items[index]["final"].(bool) || event.Final
		s.items[index] = item
		return
	}
	s.items = append(s.items, item)
}

func (s *corpusTranscriptState) reduceTool(env eventstream.Envelope, event Event) {
	parentObserver := ACPEventScope(env.Scope) == ScopeMain && event.AnchorToolCallID != ""
	toolCallID := event.ToolCallID
	if parentObserver {
		toolCallID = event.AnchorToolCallID
	}
	id := corpusToolIdentity(env, toolCallID, parentObserver)
	index := s.itemIndex(id)
	var item map[string]any
	if index >= 0 {
		item = cloneCorpusMap(s.items[index])
	} else {
		item = corpusOrigin(env)
		item["content"] = []schema.ToolCallContent{}
		item["locations"] = []schema.ToolCallLocation{}
	}
	item["cursor"] = env.Cursor
	if !parentObserver {
		for key, value := range corpusOrigin(env) {
			item[key] = value
		}
	}
	item["error"] = event.ToolError
	item["id"] = id
	item["kind"] = "tool"
	item["status"] = firstNonEmptyString(event.ToolStatus, corpusMapString(item, "status"), "in_progress")
	item["terminal"] = event.ToolTerminal
	item["toolCallId"] = toolCallID
	if !parentObserver {
		corpusSet(item, "title", firstNonEmptyString(event.ToolTitle, corpusMapString(item, "title"), event.ToolName))
		corpusSet(item, "toolKind", firstNonEmptyString(event.ToolKind, corpusMapString(item, "toolKind")))
	}
	if parentObserver && event.AnchorToolName != "" {
		item["title"] = event.AnchorToolName
	}
	if rawInput, ok := event.Meta[corpusRawInputKey].(map[string]any); ok && len(rawInput) > 0 {
		item["rawInput"] = rawInput
	}
	if rawOutput, ok := event.Meta[corpusRawOutputKey]; ok && rawOutput != nil {
		item["rawOutput"] = rawOutput
	}
	if content, ok := event.Meta[corpusContentKey].([]schema.ToolCallContent); ok && len(content) > 0 {
		current, _ := item["content"].([]schema.ToolCallContent)
		item["content"] = append(current, content...)
	}
	if locations, ok := event.Meta[corpusLocationsKey].([]schema.ToolCallLocation); ok && len(locations) > 0 {
		item["locations"] = locations
	}
	if terminalInfo, ok := metautil.TerminalInfo(event.Meta); ok {
		item["terminalId"] = terminalInfo.TerminalID
	}
	if terminalOutput, ok := metautil.TerminalOutput(event.Meta); ok {
		item["terminalId"] = terminalOutput.TerminalID
		item["terminalOutput"] = corpusMapString(item, "terminalOutput") + terminalOutput.Data
	}
	if terminalExit, ok := metautil.TerminalExit(event.Meta); ok {
		item["terminalId"] = terminalExit.TerminalID
		if terminalExit.ExitCode != nil {
			item["terminalExitCode"] = *terminalExit.ExitCode
		}
		if terminalExit.Signal != nil {
			item["terminalSignal"] = *terminalExit.Signal
		}
	}
	if MetaBool(event.Meta, "caelis", "runtime", "stream", "truncated") || MetaString(event.Meta, "caelis", "runtime", "stream", "truncated_before") != "" {
		item["terminalGapBefore"] = true
	}
	if before := MetaString(event.Meta, "caelis", "runtime", "stream", "truncated_before"); before != "" {
		item["terminalTruncatedBefore"] = before
	}
	if index >= 0 {
		s.items[index] = item
	} else {
		s.items = append(s.items, item)
	}
}

func (s *corpusTranscriptState) publicValue() map[string]any {
	out := map[string]any{"items": s.items}
	if s.usage != nil {
		out["usage"] = s.usage
	}
	if s.boundary != nil {
		out["boundary"] = s.boundary
	}
	return out
}

func (s *corpusTranscriptState) replaceOrAppend(item map[string]any) {
	if index := s.itemIndex(corpusMapString(item, "id")); index >= 0 {
		s.items[index] = item
		return
	}
	s.items = append(s.items, item)
}

func (s *corpusTranscriptState) itemIndex(id string) int {
	for i, item := range s.items {
		if corpusMapString(item, "id") == id {
			return i
		}
	}
	return -1
}

type corpusSurfaceProjector struct{}

func (corpusSurfaceProjector) ResolveToolName(_ map[string]any, title string, kind string) string {
	return firstNonEmptyString(title, kind)
}

func (corpusSurfaceProjector) ProjectToolCall(input ToolProjectionInput) Event {
	return corpusToolEvent(input, NormalizeToolStartStatus(input.Status), input.Error)
}

func (corpusSurfaceProjector) ProjectToolResult(input ToolProjectionInput, defaultSuccessStatus string) (Event, bool) {
	status, isErr := NormalizeToolResultStatus(input.Status, RawMap(input.RawOutput), input.Error, defaultSuccessStatus)
	return corpusToolEvent(input, status, isErr), true
}

func (corpusSurfaceProjector) ApprovalCommandPreview(map[string]any) string { return "" }

func corpusToolEvent(input ToolProjectionInput, status string, isErr bool) Event {
	meta := CloneAnyMap(input.Meta)
	if meta == nil {
		meta = map[string]any{}
	}
	meta[corpusRawInputKey] = input.RawInput
	meta[corpusRawOutputKey] = input.RawOutput
	meta[corpusContentKey] = append([]schema.ToolCallContent(nil), input.Content...)
	meta[corpusLocationsKey] = append([]schema.ToolCallLocation(nil), input.Locations...)
	if exitCode, ok := rawExitCode(RawMap(input.RawOutput)); ok && exitCode > 0 {
		isErr = true
	}
	_, hasExit := metautil.TerminalExit(meta)
	return Event{
		Kind: EventTool, Scope: input.Scope, ScopeID: input.ScopeID, Actor: input.Actor, OccurredAt: input.OccurredAt,
		Meta: meta, ToolCallID: input.CallID, ToolName: input.ToolName, ToolKind: input.ToolKind,
		ToolTitle: input.ToolTitle, ToolStatus: status, ToolError: isErr,
		ToolTerminal: ToolStatusFinal(status, isErr) || hasExit,
	}
}

func corpusOrigin(env eventstream.Envelope) map[string]any {
	out := map[string]any{"cursor": env.Cursor, "scope": string(ACPEventScope(env.Scope))}
	corpusSet(out, "actor", strings.TrimSpace(env.Actor))
	corpusSet(out, "runId", strings.TrimSpace(env.RunID))
	corpusSet(out, "scopeId", strings.TrimSpace(env.ScopeID))
	corpusSet(out, "turnId", strings.TrimSpace(env.TurnID))
	if env.ParentTool != nil {
		out["parentTool"] = map[string]any{
			"tool_call_id": env.ParentTool.ToolCallID,
			"tool_name":    env.ParentTool.ToolName,
		}
	}
	return out
}

func corpusEnvelopeIdentity(env eventstream.Envelope) string {
	if value := strings.TrimSpace(env.ProjectionID); value != "" {
		return "projection:" + value
	}
	if value := strings.TrimSpace(env.EventID); value != "" {
		return "event:" + value + ":" + corpusPositionIdentity(env.Position)
	}
	return "cursor:" + env.Cursor
}

func corpusPositionIdentity(position *eventstream.FeedPosition) string {
	if position == nil {
		return ""
	}
	if position.Durable != nil {
		return strconv.FormatUint(position.Durable.Seq, 10) + ":" + strconv.FormatUint(uint64(position.Durable.ProjectionIndex), 10)
	}
	if position.Transient != nil {
		return strings.Join([]string{
			strconv.FormatUint(position.Transient.Anchor.Seq, 10),
			strconv.FormatUint(uint64(position.Transient.Anchor.ProjectionIndex), 10),
			position.Transient.Generation,
			strconv.FormatUint(position.Transient.Sequence, 10),
		}, ":")
	}
	return ""
}

func corpusMessageIdentity(env eventstream.Envelope, messageID string) string {
	return "message:" + strings.Join([]string{
		string(ACPEventScope(env.Scope)), strings.TrimSpace(env.ScopeID), strings.TrimSpace(env.RunID), messageID,
	}, ":")
}

func corpusToolIdentity(env eventstream.Envelope, toolCallID string, parentObserver bool) string {
	scope := string(ACPEventScope(env.Scope))
	scopeID := strings.TrimSpace(env.ScopeID)
	runID := strings.TrimSpace(env.RunID)
	if parentObserver {
		scope = string(ScopeMain)
		scopeID = ""
	}
	return "tool:" + strings.Join([]string{scope, scopeID, runID, toolCallID}, ":")
}

func corpusPlanIdentity(env eventstream.Envelope) string {
	return "plan:" + strings.Join([]string{
		string(ACPEventScope(env.Scope)), strings.TrimSpace(env.ScopeID), strings.TrimSpace(env.RunID),
	}, ":")
}

func normalizeCorpusJSON(t *testing.T, value any) any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal corpus state: %v", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("normalize corpus state: %v", err)
	}
	return out
}

func cloneCorpusMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func corpusSet(values map[string]any, key string, value string) {
	if strings.TrimSpace(value) != "" {
		values[key] = value
	}
}

func corpusMapString(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}
