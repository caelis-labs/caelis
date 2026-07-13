package appserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/appserver/generated"
	jsonschema "github.com/google/jsonschema-go/jsonschema"
)

func TestCommandOutcomeHTTPStatusesMatchOpenAPI(t *testing.T) {
	for _, test := range []struct {
		outcome controlclient.Outcome
		status  int
	}{
		{outcome: controlclient.OutcomeCommitted, status: http.StatusOK},
		{outcome: controlclient.OutcomeAccepted, status: http.StatusAccepted},
		{outcome: controlclient.OutcomeUnknown, status: http.StatusAccepted},
		{outcome: controlclient.OutcomeRejected, status: http.StatusBadRequest},
		{outcome: controlclient.OutcomeConflicted, status: http.StatusConflict},
	} {
		t.Run(string(test.outcome), func(t *testing.T) {
			result := controlclient.CommandResult{OperationID: "operation-1", Outcome: test.outcome}
			recorder := httptest.NewRecorder()
			writeCommandResult(recorder, result, nil)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
			validateWireValue(t, "CommandResult", result)
		})
	}
}

func TestProductionRequestAndResponseJSONConformsToOpenAPI(t *testing.T) {
	revision := uint64(7)
	base := controlclient.WriteBase{OperationID: "operation-1", SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: "epoch-1"}
	target := controlclient.TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"}
	requests := map[string]any{
		"CreateSessionRequest":     controlclient.CreateSessionRequest{WriteBase: base, PreferredSessionID: "session-1", WorkspaceKey: "workspace-1", CWD: "/tmp/workspace", Title: "Session", Metadata: map[string]any{"source": "test"}},
		"CloseSessionRequest":      controlclient.CloseSessionRequest{WriteBase: base},
		"PromptRequest":            controlclient.PromptRequest{WriteBase: base, Input: "hello", DisplayInput: "hello"},
		"SteerRequest":             controlclient.SteerRequest{WriteBase: base, Target: target, Input: "continue"},
		"CancelRequest":            controlclient.CancelRequest{WriteBase: base, Target: target, Reason: "stop"},
		"ResolveApprovalRequest":   controlclient.ResolveApprovalRequest{WriteBase: base, Target: target, ApprovalRequestID: "approval-1", Outcome: "selected", OptionID: schema.PermAllowOnce, Approved: true},
		"AttachParticipantRequest": controlclient.AttachParticipantRequest{WriteBase: base, Agent: "reviewer", Role: session.ParticipantRoleSidecar, Label: "Reviewer", Source: "control"},
		"PromptParticipantRequest": controlclient.PromptParticipantRequest{WriteBase: base, ParticipantID: "participant-1", Input: "review"},
		"CancelParticipantRequest": controlclient.CancelParticipantRequest{WriteBase: base, ParticipantID: "participant-1", Target: target, Reason: "stop"},
		"DetachParticipantRequest": controlclient.DetachParticipantRequest{WriteBase: base, ParticipantID: "participant-1", Source: "control"},
		"HandoffRequest":           controlclient.HandoffRequest{WriteBase: base, Kind: session.ControllerKindACP, Agent: "external", Source: "control", Reason: "delegate"},
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
			})
		})
	}
	validateWireValue(t, "ErrorResponse", map[string]any{"error": "invalid request"})
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
	validateWireValue(t, "ResumeBoundary", resumeBoundary{ResumeMode: controlclient.ResumeModeExact, BoundaryCursor: "cursor-1"})
	validateWireValue(t, "EventBatch", controlclient.EventBatch{ResumeMode: controlclient.ResumeModeExact, Events: []eventstream.Envelope{noticeEnvelope()}})
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
		"position":{"durable":{"seq":1,"projection_index":0}},
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
	if got := rawUpdate["sessionUpdate"]; got != "vendor/custom" {
		t.Fatalf("generated ACPRawUpdate sessionUpdate = %v, want vendor/custom", got)
	}
	encodedUpdate, err := json.Marshal(rawUpdate)
	if err != nil {
		t.Fatalf("encode generated ACPRawUpdate: %v", err)
	}
	assertJSONEquivalent(t, encodedUpdate, rawUpdateJSON)
}

func assertJSONEquivalent(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode got JSON: %v\nJSON: %s", err, got)
	}
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode want JSON: %v\nJSON: %s", err, want)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch\ngot:  %s\nwant: %s", got, want)
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

func stringPointer(value string) *string { return &value }

func validateWireValue(t *testing.T, schemaName string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", schemaName, err)
	}
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatalf("decode %s instance: %v", schemaName, err)
	}
	if err := openAPIValidator(t, schemaName).Validate(instance); err != nil {
		t.Fatalf("%s wire does not conform: %v\nJSON: %s", schemaName, err, raw)
	}
}

func openAPIValidator(t *testing.T, schemaName string) *jsonschema.Resolved {
	t.Helper()
	data, err := os.ReadFile("../../api/control/v1/openapi.json")
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
