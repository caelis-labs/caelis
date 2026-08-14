package wirev1

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/client/wirev1/generated"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	jsonschema "github.com/google/jsonschema-go/jsonschema"
)

func TestProductionRequestAndResponseJSONConformsToOpenAPI(t *testing.T) {
	revision := uint64(7)
	base := controlclient.WriteBase{OperationID: "operation-1", SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: "epoch-1"}
	target := controlclient.TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"}
	contentParts := []model.ContentPart{
		{Type: model.ContentPartText, Text: "hello "},
		{Type: model.ContentPartImage, MimeType: "image/png", Data: "aW1n", FileName: "shot.png"},
	}
	requests := map[string]any{
		"CreateSessionRequest":   controlclient.CreateSessionRequest{WriteBase: base, PreferredSessionID: "session-1", WorkspaceKey: "workspace-1", CWD: "/tmp/workspace", Title: "Session", Metadata: map[string]any{"source": "test"}},
		"CloseSessionRequest":    controlclient.CloseSessionRequest{WriteBase: base},
		"PromptRequest":          controlclient.PromptRequest{WriteBase: base, Input: "hello", DisplayInput: "hello", ContentParts: contentParts},
		"SteerRequest":           controlclient.SteerRequest{WriteBase: base, Target: target, ContentParts: contentParts},
		"CancelRequest":          controlclient.CancelRequest{WriteBase: base, Target: target, Reason: "stop"},
		"ResolveApprovalRequest": controlclient.ResolveApprovalRequest{WriteBase: base, Target: target, ApprovalRequestID: "approval-1", Outcome: "selected", OptionID: schema.PermAllowOnce, Approved: true},
		"SessionModeRequest":     controlclient.SessionModeRequest{WriteBase: base, Mode: "manual"},
		"SessionModelRequest": controlclient.SessionModelRequest{
			WriteBase: base, Model: "mimo", ReasoningEffort: "high",
		},
		"SessionControllerModeRequest": controlclient.SessionControllerModeRequest{WriteBase: base, Mode: "code"},
		"SessionPresentationModeRequest": controlclient.SessionPresentationModeRequest{
			WriteBase: base, Mode: "focus",
		},
		"SessionPresentationConfigRequest": controlclient.SessionPresentationConfigRequest{
			WriteBase: base, ConfigID: "tone", Value: "quiet",
		},
		"SandboxRequest": controlclient.SandboxRequest{
			WriteBase: controlclient.WriteBase{OperationID: "sandbox-operation-1", ExpectedRevision: &revision}, Backend: "host",
		},
		"BindAgentBindingRequest": controlclient.BindAgentBindingRequest{
			WriteBase: controlclient.WriteBase{OperationID: "agent-binding-operation-1", ExpectedRevision: &revision},
			Binding:   agentbinding.Binding{Handle: agentbinding.HandleOrbit, ProfileID: "provider:mimo", Effort: "high"},
		},
		"ResetAgentBindingRequest": controlclient.ResetAgentBindingRequest{
			WriteBase: controlclient.WriteBase{OperationID: "agent-binding-reset-operation-1", ExpectedRevision: &revision},
			Handle:    agentbinding.HandleOrbit,
		},
		"CreateAgentRoleRequest": controlclient.CreateAgentRoleRequest{
			WriteBase: controlclient.WriteBase{OperationID: "agent-role-create-operation-1", ExpectedRevision: &revision},
			Role:      agentbinding.Role{Handle: "research", Description: "Investigate unfamiliar systems."},
			Binding:   agentbinding.Binding{ProfileID: "provider:mimo", Effort: "high"},
		},
		"DeleteAgentRoleRequest": controlclient.DeleteAgentRoleRequest{
			WriteBase: controlclient.WriteBase{OperationID: "agent-role-delete-operation-1", ExpectedRevision: &revision},
			Handle:    "research",
		},
		"AgentBindingSetRequest": controlclient.AgentBindingSetRequest{
			WriteBase: controlclient.WriteBase{OperationID: "agent-binding-set-operation-1", ExpectedRevision: &revision},
			SetName:   "baseline",
		},
		"AgentMessageRequest": controlclient.AgentMessageRequest{
			SessionID: "session-1", MessageID: "message-1", To: "parent", Text: "status", DisplayFrom: "@display-only",
		},
		"CompletionRequest": controlclient.CompletionRequest{
			SessionID: "session-1", WorkspaceKey: "workspace-1", CWD: "/tmp/workspace",
			Surface: "tui", Query: "read", Command: "model use", Name: "review", Limit: 10,
		},
	}
	for name, request := range requests {
		t.Run("request/"+name, func(t *testing.T) { validateWireValue(t, name, request) })
	}

	for _, outcome := range []controlclient.Outcome{
		controlclient.OutcomeAccepted, controlclient.OutcomeCommitted, controlclient.OutcomeConflicted,
		controlclient.OutcomeRejected, controlclient.OutcomeUnknown,
	} {
		t.Run("response/CommandResult/"+string(outcome), func(t *testing.T) {
			validateWireValue(t, "CommandResult", controlclient.CommandResult{
				OperationID: "operation-1", Outcome: outcome, SessionID: "session-1", Revision: 8, Target: target, Detail: "detail",
				ErrorCode: errorcode.FailedPrecondition, ErrorKind: controlclient.ErrorKindSessionClosed,
			})
		})
	}
	validateWireValue(t, "AgentMessageResult", controlclient.AgentMessageResult{
		MessageID: "message-1", Accepted: true, State: "running", Target: target, StartedTurn: true,
	})
	validateWireValue(t, "ErrorResponse", map[string]any{
		"error": "invalid request", "code": errorcode.FailedPrecondition, "kind": controlclient.ErrorKindSessionClosed,
	})
	validateWireValue(t, "SessionList", session.SessionList{Sessions: []session.SessionSummary{{
		SessionRef: session.SessionRef{AppName: "caelis", UserID: "owner", SessionID: "session-1", WorkspaceKey: "workspace-1"},
		CWD:        "/tmp/workspace", Title: "Session", UpdatedAt: time.Unix(100, 0).UTC(), Metadata: map[string]any{"source": "test"},
	}}})
	state := controlclient.SessionState{
		ProtocolVersion: 1, EnvelopeVersion: controlclient.EnvelopeVersion, APIVersion: controlclient.HTTPAPIVersion,
		SessionID: "session-1", Revision: 8, ResumeMode: controlclient.ResumeModeExact,
		Run: controlclient.RunState{}, Controller: session.ControllerBinding{}, Approval: controlclient.ApprovalState{},
		Capabilities: controlclient.ClientCapabilities{CaelisTerminalStream: true},
	}
	validateWireValue(t, "SessionState", state)
	validateWireValue(t, "StatusSnapshot", controlstatus.StatusSnapshot{
		Configuration: controlstatus.StatusConfiguration{Revision: math.MaxUint64},
	})
}

func TestStatusConfigurationRevisionWireRoundTrip(t *testing.T) {
	want := controlstatus.StatusSnapshot{Configuration: controlstatus.StatusConfiguration{Revision: math.MaxUint64}}
	raw, err := Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"revision":"18446744073709551615"`)) {
		t.Fatalf("wire JSON = %s, want decimal string revision", raw)
	}
	var got controlstatus.StatusSnapshot
	if err := Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
}

func TestEveryProductionEnvelopeVariantConformsToOpenAPI(t *testing.T) {
	text := schema.TextContent{Type: "text", Text: "hello"}
	title := "tool"
	updatedAt := "2026-07-13T00:00:00Z"
	updates := []schema.Update{
		schema.ContentChunk{SessionUpdate: schema.UpdateUserMessage, Content: text},
		schema.ContentChunk{SessionUpdate: schema.UpdateAgentMessage, Content: text, MessageID: "message-1"},
		schema.ContentChunk{SessionUpdate: schema.UpdateAgentThought, Content: text},
		schema.ContentChunk{SessionUpdate: schema.UpdateCompact, Content: text},
		schema.ToolCall{SessionUpdate: schema.UpdateToolCall, ToolCallID: "tool-1", Title: "Read", Kind: schema.ToolKindRead, Status: schema.ToolStatusPending},
		schema.ToolCallUpdate{SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "tool-1", Title: &title, Status: stringPointer(schema.ToolStatusCompleted)},
		schema.PlanUpdate{SessionUpdate: schema.UpdatePlan, Entries: []schema.PlanEntry{{Content: "Inspect", Status: "completed", Priority: "high"}}},
		schema.UsageUpdate{SessionUpdate: schema.UpdateUsage, Size: 200000, Used: 42000, Cost: &schema.UsageCost{Total: 0.47, Currency: "USD"}},
		schema.CurrentModeUpdate{SessionUpdate: schema.UpdateCurrentMode, CurrentModeID: "manual"},
		schema.ConfigOptionUpdate{SessionUpdate: schema.UpdateConfigOption, ConfigOptions: []schema.SessionConfigOption{}},
		schema.SessionInfoUpdate{SessionUpdate: schema.UpdateSessionInfo, Title: &title, UpdatedAt: &updatedAt},
		schema.AvailableCommandsUpdate{SessionUpdate: schema.UpdateAvailableCmds, AvailableCommands: []schema.AvailableCommand{{Name: "review"}}},
		schema.RawUpdate{SessionUpdate: "vendor/custom", Raw: json.RawMessage(`{"sessionUpdate":"vendor/custom","value":42,"nested":{"ok":true}}`)},
	}
	for _, update := range updates {
		update := update
		t.Run("session_update/"+update.SessionUpdateType(), func(t *testing.T) {
			envelope := baseEnvelope(eventstream.KindSessionUpdate)
			envelope.Update = update
			validateWireValue(t, "Envelope", envelope)
		})
	}

	permission := baseEnvelope(eventstream.KindRequestPermission)
	permission.ApprovalRequestID = "approval-1"
	permission.Permission = &schema.RequestPermissionRequest{
		SessionID: "session-1",
		ToolCall:  schema.ToolCallUpdate{SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "tool-1", Title: &title},
		Options:   []schema.PermissionOption{{OptionID: schema.PermAllowOnce, Name: "Allow once", Kind: schema.PermAllowOnce}},
	}
	participant := baseEnvelope(eventstream.KindParticipant)
	participant.Participant = &eventstream.Participant{State: "attached"}
	lifecycle := baseEnvelope(eventstream.KindLifecycle)
	lifecycle.Lifecycle = &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted, StopReason: schema.StopReasonEndTurn}
	review := baseEnvelope(eventstream.KindApprovalReview)
	review.ApprovalReview = &eventstream.ApprovalReview{ToolCallID: "tool-1", Status: "completed", RawInput: map[string]any{"path": "README.md"}}
	failure := baseEnvelope(eventstream.KindError)
	failure.Error = "failed"
	for name, envelope := range map[string]eventstream.Envelope{
		"request_permission": permission,
		"notice":             noticeEnvelope(),
		"compact_notice":     compactNoticeEnvelope(),
		"participant":        participant,
		"lifecycle":          lifecycle,
		"approval_review":    review,
		"error":              failure,
	} {
		t.Run(name, func(t *testing.T) { validateWireValue(t, "Envelope", envelope) })
	}
}

func TestRawACPUpdateSchemaRejectsKnownStandardDiscriminator(t *testing.T) {
	validateWireValue(t, "ACPRawUpdate", map[string]any{"sessionUpdate": "vendor/custom", "value": 42})
	validator := openAPIValidator(t, "ACPRawUpdate")
	if err := validator.Validate(map[string]any{"sessionUpdate": schema.UpdateToolCall, "vendor": true}); err == nil {
		t.Fatal("ACPRawUpdate accepted a known standard discriminator")
	}
}

func TestGeneratedGoEnvelopePreservesRawACPVendorFields(t *testing.T) {
	t.Parallel()

	rawEnvelope := []byte(`{
		"kind":"session/update",
		"cursor":"cursor-1",
		"position":{"durable":{"seq":"1","projection_index":0}},
		"delivery":{"mode":"canonical"},
		"session_id":"session-1",
		"update":{
			"sessionUpdate":"vendor/custom",
			"value":42,
			"nested":{"ok":true},
			"items":["one",2]
		}
	}`)
	var envelope generated.Envelope
	if err := json.Unmarshal(rawEnvelope, &envelope); err != nil {
		t.Fatalf("decode generated Envelope: %v", err)
	}
	encodedEnvelope, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("encode generated Envelope: %v", err)
	}
	assertJSONEquivalent(t, encodedEnvelope, rawEnvelope)

	rawUpdateJSON := []byte(`{"sessionUpdate":"vendor/custom","value":42,"nested":{"ok":true}}`)
	var rawUpdate generated.ACPRawUpdate
	if err := json.Unmarshal(rawUpdateJSON, &rawUpdate); err != nil {
		t.Fatalf("decode generated ACPRawUpdate: %v", err)
	}
	var discriminator struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(rawUpdate, &discriminator); err != nil {
		t.Fatalf("inspect generated ACPRawUpdate: %v", err)
	}
	if discriminator.SessionUpdate != "vendor/custom" {
		t.Fatalf("generated ACPRawUpdate sessionUpdate = %v, want vendor/custom", discriminator.SessionUpdate)
	}
	encodedUpdate, err := json.Marshal(rawUpdate)
	if err != nil {
		t.Fatalf("encode generated ACPRawUpdate: %v", err)
	}
	assertJSONEquivalent(t, encodedUpdate, rawUpdateJSON)
}

func assertJSONEquivalent(t *testing.T, got, want []byte) {
	t.Helper()
	gotValue := decodeJSONWithNumbers(t, got)
	wantValue := decodeJSONWithNumbers(t, want)
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch\ngot:  %s\nwant: %s", got, want)
	}
}

func decodeJSONWithNumbers(t *testing.T, raw []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode JSON: %v\nJSON: %s", err, raw)
	}
	return normalizeJSONNumbers(t, value)
}

func normalizeJSONNumbers(t *testing.T, value any) any {
	t.Helper()
	switch typed := value.(type) {
	case json.Number:
		text := typed.String()
		if !strings.ContainsAny(text, ".eE") {
			if strings.HasPrefix(text, "-") {
				parsed, err := strconv.ParseInt(text, 10, 64)
				if err != nil {
					t.Fatalf("decode JSON integer %q: %v", text, err)
				}
				return parsed
			}
			parsed, err := strconv.ParseUint(text, 10, 64)
			if err != nil {
				t.Fatalf("decode JSON integer %q: %v", text, err)
			}
			return parsed
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil {
			t.Fatalf("decode JSON number %q: %v", text, err)
		}
		return parsed
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = normalizeJSONNumbers(t, item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeJSONNumbers(t, item)
		}
		return out
	default:
		return value
	}
}

func baseEnvelope(kind eventstream.Kind) eventstream.Envelope {
	return eventstream.Envelope{
		Kind: kind, Cursor: "cursor-1", SessionID: "session-1",
		Position: &eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{Seq: 1, ProjectionIndex: 0}},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryCanonical},
	}
}

func noticeEnvelope() eventstream.Envelope {
	envelope := baseEnvelope(eventstream.KindNotice)
	envelope.Notice = "connected"
	return envelope
}

func compactNoticeEnvelope() eventstream.Envelope {
	envelope := baseEnvelope(eventstream.KindNotice)
	envelope.Notice = "Context compacted"
	envelope.NoticeKind = eventstream.NoticeKindCompact
	return envelope
}

func stringPointer(value string) *string { return &value }

func validateWireValue(t *testing.T, schemaName string, value any) {
	t.Helper()
	raw, err := Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", schemaName, err)
	}
	instance := decodeJSONWithNumbers(t, raw)
	if err := openAPIValidator(t, schemaName).Validate(instance); err != nil {
		t.Fatalf("%s wire does not conform: %v\nJSON: %s", schemaName, err, raw)
	}
}

func openAPIValidator(t *testing.T, schemaName string) *jsonschema.Resolved {
	t.Helper()
	data, err := os.ReadFile("../../../api/control/v1/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.ReplaceAll(data, []byte("#/components/schemas/"), []byte("#/$defs/"))
	var spec struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	defs := make(map[string]*jsonschema.Schema, len(spec.Components.Schemas))
	for name, raw := range spec.Components.Schemas {
		var definition jsonschema.Schema
		if err := json.Unmarshal(raw, &definition); err != nil {
			t.Fatalf("decode OpenAPI schema %s: %v", name, err)
		}
		defs[name] = &definition
	}
	root := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Ref:    "#/$defs/" + schemaName,
		Defs:   defs,
	}
	resolved, err := root.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve OpenAPI schema %s: %v", schemaName, err)
	}
	return resolved
}
