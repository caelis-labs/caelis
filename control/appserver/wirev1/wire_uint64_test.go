package wirev1

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/wirev1/generated"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestUint64WireRoundTripAtJavaScriptBoundary(t *testing.T) {
	values := []uint64{
		9007199254740991,
		9007199254740992,
		9007199254740993,
		math.MaxUint64,
	}
	for _, value := range values {
		value := value
		t.Run(strconv.FormatUint(value, 10), func(t *testing.T) {
			decimal := strconv.FormatUint(value, 10)

			request := appserver.PromptRequest{
				WriteBase: appserver.WriteBase{OperationID: "operation-1", SessionID: "session-1", ExpectedRevision: &value},
				Input:     "hello",
			}
			requestJSON := mustMarshalWire(t, request)
			var requestDTO generated.PromptRequest
			if err := json.Unmarshal(requestJSON, &requestDTO); err != nil {
				t.Fatal(err)
			}
			if requestDTO.ExpectedRevision == nil || string(*requestDTO.ExpectedRevision) != decimal {
				t.Fatalf("generated expected_revision = %#v, want %q", requestDTO.ExpectedRevision, decimal)
			}
			var decodedRequest appserver.PromptRequest
			if err := DecodeRequest(requestJSON, &decodedRequest); err != nil {
				t.Fatal(err)
			}
			if decodedRequest.ExpectedRevision == nil || *decodedRequest.ExpectedRevision != value {
				t.Fatalf("decoded expected_revision = %#v, want %d", decodedRequest.ExpectedRevision, value)
			}
			sandboxJSON := mustMarshalWire(t, appserver.SandboxRequest{WriteBase: appserver.WriteBase{
				OperationID: "sandbox-operation-1", ExpectedRevision: &value,
			}})
			var sandboxDTO generated.SandboxRequest
			if err := json.Unmarshal(sandboxJSON, &sandboxDTO); err != nil {
				t.Fatal(err)
			}
			if sandboxDTO.ExpectedRevision == nil || string(*sandboxDTO.ExpectedRevision) != decimal {
				t.Fatalf("generated sandbox expected_revision = %#v, want %q", sandboxDTO.ExpectedRevision, decimal)
			}
			var decodedSandbox appserver.SandboxRequest
			if err := DecodeRequest(sandboxJSON, &decodedSandbox); err != nil {
				t.Fatal(err)
			}
			if decodedSandbox.ExpectedRevision == nil || *decodedSandbox.ExpectedRevision != value {
				t.Fatalf("decoded sandbox expected_revision = %#v, want %d", decodedSandbox.ExpectedRevision, value)
			}
			modelRequests := []struct {
				name    string
				request any
				decoded any
			}{
				{
					name: "connect",
					request: appserver.ConnectModelRequest{
						WriteBase: appserver.WriteBase{OperationID: "model-connect-operation-1", ExpectedRevision: &value},
						Config:    appserver.ConnectConfig{Provider: "ollama", Model: "mimo"},
					},
					decoded: &appserver.ConnectModelRequest{},
				},
				{
					name: "use",
					request: appserver.UseModelRequest{
						WriteBase: appserver.WriteBase{OperationID: "model-use-operation-1", ExpectedRevision: &value},
						Model:     "mimo",
					},
					decoded: &appserver.UseModelRequest{},
				},
				{
					name: "delete",
					request: appserver.DeleteModelRequest{
						WriteBase: appserver.WriteBase{OperationID: "model-delete-operation-1", ExpectedRevision: &value},
						Model:     "mimo",
					},
					decoded: &appserver.DeleteModelRequest{},
				},
			}
			for _, modelRequest := range modelRequests {
				raw := mustMarshalWire(t, modelRequest.request)
				if err := DecodeRequest(raw, modelRequest.decoded); err != nil {
					t.Fatalf("decode Host model %s request: %v", modelRequest.name, err)
				}
				var base appserver.WriteBase
				switch decoded := modelRequest.decoded.(type) {
				case *appserver.ConnectModelRequest:
					base = decoded.WriteBase
				case *appserver.UseModelRequest:
					base = decoded.WriteBase
				case *appserver.DeleteModelRequest:
					base = decoded.WriteBase
				}
				if base.ExpectedRevision == nil || *base.ExpectedRevision != value {
					t.Fatalf("decoded Host model %s expected_revision = %#v, want %d", modelRequest.name, base.ExpectedRevision, value)
				}
				var payload map[string]any
				if err := json.Unmarshal(raw, &payload); err != nil {
					t.Fatal(err)
				}
				if payload["expected_revision"] != decimal {
					t.Fatalf("Host model %s expected_revision = %#v, want %q", modelRequest.name, payload["expected_revision"], decimal)
				}
			}
			sessionModelJSON := mustMarshalWire(t, appserver.SessionModelRequest{
				WriteBase: appserver.WriteBase{
					OperationID: "session-model-operation-1", SessionID: "session-1", ExpectedRevision: &value,
				},
				Model: "mimo",
			})
			var sessionModelDTO generated.SessionModelRequest
			if err := json.Unmarshal(sessionModelJSON, &sessionModelDTO); err != nil {
				t.Fatal(err)
			}
			if sessionModelDTO.ExpectedRevision == nil || string(*sessionModelDTO.ExpectedRevision) != decimal {
				t.Fatalf("generated Session model expected_revision = %#v, want %q", sessionModelDTO.ExpectedRevision, decimal)
			}
			var decodedSessionModel appserver.SessionModelRequest
			if err := DecodeRequest(sessionModelJSON, &decodedSessionModel); err != nil {
				t.Fatal(err)
			}
			if decodedSessionModel.ExpectedRevision == nil || *decodedSessionModel.ExpectedRevision != value {
				t.Fatalf("decoded Session model expected_revision = %#v, want %d", decodedSessionModel.ExpectedRevision, value)
			}
			handoffJSON := mustMarshalWire(t, appserver.HandoffAgentRequest{
				WriteBase: appserver.WriteBase{
					OperationID: "handoff-operation-1", SessionID: "session-1", ExpectedRevision: &value,
				},
				Target: "orbit",
			})
			var handoffDTO generated.HandoffAgentRequest
			if err := json.Unmarshal(handoffJSON, &handoffDTO); err != nil {
				t.Fatal(err)
			}
			if handoffDTO.ExpectedRevision == nil || string(*handoffDTO.ExpectedRevision) != decimal {
				t.Fatalf("generated handoff expected_revision = %#v, want %q", handoffDTO.ExpectedRevision, decimal)
			}
			var decodedHandoff appserver.HandoffAgentRequest
			if err := DecodeRequest(handoffJSON, &decodedHandoff); err != nil {
				t.Fatal(err)
			}
			if decodedHandoff.ExpectedRevision == nil || *decodedHandoff.ExpectedRevision != value {
				t.Fatalf("decoded handoff expected_revision = %#v, want %d", decodedHandoff.ExpectedRevision, value)
			}
			bindingJSON := mustMarshalWire(t, appserver.BindAgentBindingRequest{
				WriteBase: appserver.WriteBase{OperationID: "agent-binding-operation-1", ExpectedRevision: &value},
				Binding:   agentbinding.Binding{Handle: agentbinding.HandleOrbit, ProfileID: "provider:mimo", Effort: "high"},
			})
			var bindingDTO generated.BindAgentBindingRequest
			if err := json.Unmarshal(bindingJSON, &bindingDTO); err != nil {
				t.Fatal(err)
			}
			if bindingDTO.ExpectedRevision == nil || string(*bindingDTO.ExpectedRevision) != decimal {
				t.Fatalf("generated Agent binding expected_revision = %#v, want %q", bindingDTO.ExpectedRevision, decimal)
			}
			var decodedBinding appserver.BindAgentBindingRequest
			if err := DecodeRequest(bindingJSON, &decodedBinding); err != nil {
				t.Fatal(err)
			}
			if decodedBinding.ExpectedRevision == nil || *decodedBinding.ExpectedRevision != value {
				t.Fatalf("decoded Agent binding expected_revision = %#v, want %d", decodedBinding.ExpectedRevision, value)
			}

			resultJSON := mustMarshalWire(t, appserver.CommandResult{
				OperationID: "operation-1", Outcome: appserver.OutcomeCommitted, Revision: value,
				Resource: &appserver.CommandResource{
					Kind: appserver.CommandResourceACPPreparation, Ref: "preparation-1", Digest: "digest-1",
				},
			})
			var resultDTO generated.CommandResult
			if err := json.Unmarshal(resultJSON, &resultDTO); err != nil {
				t.Fatal(err)
			}
			assertGeneratedDecimal(t, resultDTO.Revision, value, "CommandResult.revision")
			if resultDTO.Resource == nil || resultDTO.Resource.Ref == nil || *resultDTO.Resource.Ref != "preparation-1" {
				t.Fatalf("CommandResult.resource = %#v", resultDTO.Resource)
			}

			state := appserver.SessionState{
				ProtocolVersion: 1, EnvelopeVersion: appserver.EnvelopeVersion, APIVersion: appserver.HTTPAPIVersion,
				SessionID: "session-1", Revision: value, ResumeMode: appserver.ResumeModeExact,
				BoundaryPosition: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
					Anchor: eventstream.DurableFeedPosition{Seq: value}, Generation: "generation-1", Sequence: value,
				}},
				Run:          appserver.RunState{},
				Controller:   session.ControllerBinding{ContextSyncSeq: value},
				Participants: []session.ParticipantBinding{{ID: "participant-1", ContextSyncSeq: value}},
				Approval:     appserver.ApprovalState{}, Capabilities: appserver.ClientCapabilities{CaelisTerminalStream: true},
			}
			stateJSON := mustMarshalWire(t, state)
			var stateDTO generated.SessionState
			if err := json.Unmarshal(stateJSON, &stateDTO); err != nil {
				t.Fatal(err)
			}
			if string(stateDTO.Revision) != decimal {
				t.Fatalf("SessionState.revision = %q, want %q", stateDTO.Revision, decimal)
			}
			assertGeneratedDecimal(t, stateDTO.Controller.ContextSyncSeq, value, "controller.context_sync_seq")
			assertGeneratedDecimal(t, stateDTO.Participants[0].ContextSyncSeq, value, "participant.context_sync_seq")
			if stateDTO.BoundaryPosition == nil || stateDTO.BoundaryPosition.Transient == nil {
				t.Fatalf("generated boundary position = %#v", stateDTO.BoundaryPosition)
			}
			if got := string(stateDTO.BoundaryPosition.Transient.Anchor.Seq); got != decimal {
				t.Fatalf("transient anchor seq = %q, want %q", got, decimal)
			}
			if got := string(stateDTO.BoundaryPosition.Transient.Sequence); got != decimal {
				t.Fatalf("transient sequence = %q, want %q", got, decimal)
			}

			envelope := noticeEnvelope()
			envelope.Position.Durable.Seq = value
			envelope.Meta = map[string]any{
				"from": map[string]any{"context_sync_seq": value},
				"to":   map[string]any{"context_sync_seq": value},
				"compact": map[string]any{
					"revision": value, "contract_version": value, "summarized_through_seq": value,
					"source_event_count": value, "total_tokens": value, "context_window_tokens": value,
				},
				"caelis": map[string]any{
					"runtime": map[string]any{
						"task": map[string]any{
							"output_cursor": value, "event_cursor": value, "turn_seq": value,
						},
						"stream": map[string]any{"truncated_before": value},
					},
					"usage": map[string]any{"total_tokens": value},
					"sdk": map[string]any{
						"context_window_tokens": value,
						"usage":                 map[string]any{"total_tokens": value, "cost_micros": value},
					},
				},
			}
			envelopeJSON := mustMarshalEnvelope(t, envelope)
			var envelopeDTO generated.Envelope
			if err := json.Unmarshal(envelopeJSON, &envelopeDTO); err != nil {
				t.Fatal(err)
			}
			if envelopeDTO.Position.Durable == nil || string(envelopeDTO.Position.Durable.Seq) != decimal {
				t.Fatalf("Envelope durable seq = %#v, want %q", envelopeDTO.Position.Durable, decimal)
			}
			if err := openAPIValidator(t, "Envelope").Validate(decodeJSONWithNumbers(t, envelopeJSON)); err != nil {
				t.Fatalf("max-value Envelope does not conform: %v\nJSON: %s", err, envelopeJSON)
			}
			assertKnownMetadataDecimals(t, envelopeJSON, decimal)
		})
	}
}

func TestACPOnboardingWirePreservesHostRevisionAtUint64Boundary(t *testing.T) {
	revision := uint64(math.MaxUint64)
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	requests := []struct {
		name    string
		schema  string
		request any
		decoded any
	}{
		{
			name:   "prepare",
			schema: "PrepareACPRequest",
			request: appserver.PrepareACPRequest{
				WriteBase: appserver.WriteBase{OperationID: "prepare-1", ExpectedRevision: &revision},
				Request:   controlagents.ACPPrepareRequest{AdapterID: "codex", Launcher: controlagents.LauncherChoiceNPX},
			},
			decoded: &appserver.PrepareACPRequest{},
		},
		{
			name:   "prepare-auth",
			schema: "PrepareACPAuthenticationRequest",
			request: appserver.PrepareACPAuthenticationRequest{
				WriteBase:         appserver.WriteBase{OperationID: "prepare-auth-1", ExpectedRevision: &revision},
				PreparationRef:    "preparation-1",
				PreparationDigest: digest,
				MethodID:          "login",
			},
			decoded: &appserver.PrepareACPAuthenticationRequest{},
		},
		{
			name:   "connect",
			schema: "ConnectACPRequest",
			request: appserver.ConnectACPRequest{
				WriteBase:         appserver.WriteBase{OperationID: "connect-1", ExpectedRevision: &revision},
				PreparationRef:    "preparation-1",
				PreparationDigest: digest,
				ConfigValues:      map[string]string{"effort": "high"},
			},
			decoded: &appserver.ConnectACPRequest{},
		},
	}
	for _, test := range requests {
		t.Run(test.name, func(t *testing.T) {
			raw := mustMarshalWire(t, test.request)
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["expected_revision"] != "18446744073709551615" {
				t.Fatalf("expected_revision = %#v", payload["expected_revision"])
			}
			if err := DecodeRequest(raw, test.decoded); err != nil {
				t.Fatal(err)
			}
			if err := openAPIValidator(t, test.schema).Validate(decodeJSONWithNumbers(t, raw)); err != nil {
				t.Fatalf("%s does not conform: %v\nJSON: %s", test.schema, err, raw)
			}
		})
	}

	preparation := controlagents.ACPPreparation{
		Ref:              "preparation-1",
		State:            controlagents.PreparationStateReady,
		Request:          controlagents.ACPPrepareRequest{AdapterID: "codex", Launcher: controlagents.LauncherChoiceNPX},
		ObservedRevision: revision,
		CreatedAt:        time.Date(2026, time.August, 11, 1, 2, 3, 0, time.UTC),
		ExpiresAt:        time.Date(2026, time.August, 11, 1, 12, 3, 0, time.UTC),
		ContentDigest:    digest,
	}
	raw := mustMarshalWire(t, preparation)
	var generatedPreparation generated.ACPPreparation
	if err := json.Unmarshal(raw, &generatedPreparation); err != nil {
		t.Fatal(err)
	}
	if string(generatedPreparation.ObservedRevision) != "18446744073709551615" {
		t.Fatalf("generated preparation revision = %q", generatedPreparation.ObservedRevision)
	}
	var decodedPreparation controlagents.ACPPreparation
	if err := Unmarshal(raw, &decodedPreparation); err != nil {
		t.Fatal(err)
	}
	if decodedPreparation.ObservedRevision != revision || decodedPreparation.Ref != preparation.Ref {
		t.Fatalf("decoded preparation = %#v", decodedPreparation)
	}
	if err := openAPIValidator(t, "ACPPreparation").Validate(decodeJSONWithNumbers(t, raw)); err != nil {
		t.Fatalf("ACPPreparation does not conform: %v\nJSON: %s", err, raw)
	}
}

func TestParticipantAndCompactWriteRequestsPreserveExpectedRevisionAsDecimal(t *testing.T) {
	revision := uint64(math.MaxUint64)
	write := appserver.WriteBase{
		OperationID: "participant-1", SessionID: "session-1", ExpectedRevision: &revision,
	}
	requests := []struct {
		name    string
		schema  string
		request any
		decoded any
	}{
		{
			name:    "compact",
			schema:  "CompactSessionRequest",
			request: appserver.CompactSessionRequest{WriteBase: write},
			decoded: &appserver.CompactSessionRequest{},
		},
		{
			name:   "start-participant",
			schema: "StartParticipantRequest",
			request: appserver.StartParticipantRequest{
				WriteBase: write, Handle: "zenith", Role: session.ParticipantRoleSidecar,
				Label: "@zenith", Source: "slash_profile_zenith", Input: "introduce yourself",
				DisplayAddress: "/zenith",
			},
			decoded: &appserver.StartParticipantRequest{},
		},
		{
			name:   "prompt-participant",
			schema: "PromptParticipantRequest",
			request: appserver.PromptParticipantRequest{
				WriteBase: write, ParticipantID: "participant-1", Input: "continue",
				DisplayAddress: "/zenith", Source: "user_side_agent",
			},
			decoded: &appserver.PromptParticipantRequest{},
		},
		{
			name:   "cancel-participant",
			schema: "CancelParticipantRequest",
			request: appserver.CancelParticipantRequest{
				WriteBase: write, ParticipantID: "participant-1",
				Target: appserver.TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"},
				Reason: "interrupt",
			},
			decoded: &appserver.CancelParticipantRequest{},
		},
		{
			name: "attach-participant",
			request: appserver.AttachParticipantRequest{
				WriteBase: write, ProfileID: "provider:mimo", Effort: "high",
				Role: session.ParticipantRoleSidecar, Label: "@review", Source: "slash_review",
			},
			decoded: &appserver.AttachParticipantRequest{},
		},
		{
			name: "detach-participant",
			request: appserver.DetachParticipantRequest{
				WriteBase: write, ParticipantID: "participant-1", Source: "side_agent_complete",
			},
			decoded: &appserver.DetachParticipantRequest{},
		},
		{
			name: "handoff",
			request: appserver.HandoffRequest{
				WriteBase: write, Kind: session.ControllerKindACP, Agent: "orbit", Source: "user",
			},
			decoded: &appserver.HandoffRequest{},
		},
	}
	for _, test := range requests {
		t.Run(test.name, func(t *testing.T) {
			raw := mustMarshalWire(t, test.request)
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["expected_revision"] != "18446744073709551615" {
				t.Fatalf("expected_revision = %#v", payload["expected_revision"])
			}
			if err := DecodeRequest(raw, test.decoded); err != nil {
				t.Fatal(err)
			}
			if test.schema == "" {
				return
			}
			if err := openAPIValidator(t, test.schema).Validate(decodeJSONWithNumbers(t, raw)); err != nil {
				t.Fatalf("%s does not conform: %v\nJSON: %s", test.schema, err, raw)
			}
		})
	}
}

func TestDisconnectWirePreservesHostRevisionAtUint64Boundary(t *testing.T) {
	revision := uint64(math.MaxUint64)
	request := appserver.DisconnectACPRequest{
		WriteBase: appserver.WriteBase{OperationID: "disconnect-1", ExpectedRevision: &revision},
		AgentID:   "codex",
	}
	raw := mustMarshalWire(t, request)
	var generatedRequest generated.DisconnectACPRequest
	if err := json.Unmarshal(raw, &generatedRequest); err != nil {
		t.Fatal(err)
	}
	if generatedRequest.ExpectedRevision == nil || string(*generatedRequest.ExpectedRevision) != "18446744073709551615" {
		t.Fatalf("generated disconnect expected_revision = %#v", generatedRequest.ExpectedRevision)
	}
	var decodedRequest appserver.DisconnectACPRequest
	if err := DecodeRequest(raw, &decodedRequest); err != nil {
		t.Fatal(err)
	}
	if decodedRequest.ExpectedRevision == nil || *decodedRequest.ExpectedRevision != revision {
		t.Fatalf("decoded disconnect expected_revision = %#v", decodedRequest.ExpectedRevision)
	}
	if err := openAPIValidator(t, "DisconnectACPRequest").Validate(decodeJSONWithNumbers(t, raw)); err != nil {
		t.Fatalf("DisconnectACPRequest does not conform: %v\nJSON: %s", err, raw)
	}

	snapshot := appserver.DisconnectCandidatesSnapshot{
		Revision: revision,
		Candidates: []controlagents.DisconnectCandidate{{
			AgentID: "codex", ConnectionID: "codex", LastOnConnection: true,
		}},
	}
	raw = mustMarshalWire(t, snapshot)
	var generatedSnapshot generated.DisconnectCandidatesSnapshot
	if err := json.Unmarshal(raw, &generatedSnapshot); err != nil {
		t.Fatal(err)
	}
	if string(generatedSnapshot.Revision) != "18446744073709551615" {
		t.Fatalf("generated disconnect snapshot revision = %q", generatedSnapshot.Revision)
	}
	var decodedSnapshot appserver.DisconnectCandidatesSnapshot
	if err := Unmarshal(raw, &decodedSnapshot); err != nil {
		t.Fatal(err)
	}
	if decodedSnapshot.Revision != revision || len(decodedSnapshot.Candidates) != 1 {
		t.Fatalf("decoded disconnect snapshot = %#v", decodedSnapshot)
	}
	if err := openAPIValidator(t, "DisconnectCandidatesSnapshot").Validate(decodeJSONWithNumbers(t, raw)); err != nil {
		t.Fatalf("DisconnectCandidatesSnapshot does not conform: %v\nJSON: %s", err, raw)
	}
}

func TestUsageUpdateUsesDecimalStringsBeyondJavaScriptSafeInteger(t *testing.T) {
	for _, value := range []uint64{9007199254740991, 9007199254740992, 9007199254740993, math.MaxInt64, math.MaxUint64} {
		value := value
		t.Run(strconv.FormatUint(value, 10), func(t *testing.T) {
			envelope := baseEnvelope(eventstream.KindSessionUpdate)
			envelope.Update = schema.UsageUpdate{
				SessionUpdate: schema.UpdateUsage, Size: value, Used: value,
			}
			raw := mustMarshalEnvelope(t, envelope)
			var root struct {
				Update json.RawMessage `json:"update"`
			}
			if err := json.Unmarshal(raw, &root); err != nil {
				t.Fatal(err)
			}
			var update generated.ACPUsageUpdate
			if err := json.Unmarshal(root.Update, &update); err != nil {
				t.Fatal(err)
			}
			want := strconv.FormatUint(value, 10)
			if string(update.Size) != want || string(update.Used) != want {
				t.Fatalf("usage size/used = %q/%q, want %q", update.Size, update.Used, want)
			}
			if err := openAPIValidator(t, "Envelope").Validate(decodeJSONWithNumbers(t, raw)); err != nil {
				t.Fatalf("usage Envelope does not conform: %v\nJSON: %s", err, raw)
			}
		})
	}
}

func TestControlV1UsageCostBridgesStandardAndLegacyFields(t *testing.T) {
	envelope := baseEnvelope(eventstream.KindSessionUpdate)
	envelope.Update = schema.UsageUpdate{
		SessionUpdate: schema.UpdateUsage,
		Size:          200000,
		Used:          42000,
		Cost:          &acpsdk.Cost{Amount: 0.47, Currency: "USD"},
	}
	raw := mustMarshalEnvelope(t, envelope)
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	var update map[string]json.RawMessage
	if err := json.Unmarshal(root["update"], &update); err != nil {
		t.Fatal(err)
	}
	var cost map[string]json.RawMessage
	if err := json.Unmarshal(update["cost"], &cost); err != nil {
		t.Fatal(err)
	}
	if string(cost["amount"]) != "0.47" || string(cost["total"]) != "0.47" {
		t.Fatalf("Control v1 cost = %s, want standard amount plus legacy total", update["cost"])
	}
	if err := openAPIValidator(t, "Envelope").Validate(decodeJSONWithNumbers(t, raw)); err != nil {
		t.Fatalf("usage Envelope does not conform: %v\nJSON: %s", err, raw)
	}

	legacyCases := []struct {
		name         string
		cost         string
		wantAmount   float64
		wantCurrency string
	}{
		{name: "total", cost: `{"total":0.47,"currency":"USD"}`, wantAmount: 0.47, wantCurrency: "USD"},
		{name: "components", cost: `{"input":0.1,"output":0.2,"cache_read":0.03,"cache_write":0.04,"currency":"USD"}`, wantAmount: 0.37, wantCurrency: "USD"},
		{name: "missing currency", cost: `{"input":0.1,"output":0.2}`, wantAmount: 0.3},
		{name: "empty", cost: `{}`},
	}
	for _, test := range legacyCases {
		t.Run(test.name, func(t *testing.T) {
			legacyValue := decodeJSONWithNumbers(t, []byte(test.cost))
			if err := openAPIValidator(t, "UsageCost").Validate(legacyValue); err != nil {
				t.Fatalf("legacy UsageCost does not conform: %v\nJSON: %s", err, test.cost)
			}
			update["cost"] = json.RawMessage(test.cost)
			legacyUpdate, err := json.Marshal(update)
			if err != nil {
				t.Fatal(err)
			}
			root["update"] = legacyUpdate
			legacyEnvelope, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := UnmarshalEnvelope(legacyEnvelope)
			if err != nil {
				t.Fatal(err)
			}
			usage, ok := decoded.Update.(schema.UsageUpdate)
			if !ok || usage.Cost == nil || math.Abs(usage.Cost.Amount-test.wantAmount) > 1e-12 || usage.Cost.Currency != test.wantCurrency {
				t.Fatalf("legacy Control v1 cost = %#v (%T), want amount=%v currency=%q", decoded.Update, decoded.Update, test.wantAmount, test.wantCurrency)
			}
		})
	}
}

func TestDurableEventMetadataRoundTripsIntoLosslessHTTPEnvelope(t *testing.T) {
	for _, value := range []uint64{
		9007199254740991,
		9007199254740992,
		9007199254740993,
		math.MaxUint64,
	} {
		value := value
		t.Run(strconv.FormatUint(value, 10), func(t *testing.T) {
			event := session.Event{Meta: map[string]any{
				"from": map[string]any{"context_sync_seq": value},
				"to":   map[string]any{"context_sync_seq": value},
				"compact": map[string]any{
					"revision": value, "contract_version": value, "summarized_through_seq": value,
					"source_event_count": value, "total_tokens": value, "context_window_tokens": value,
				},
				"caelis": map[string]any{
					"runtime": map[string]any{
						"task":   map[string]any{"output_cursor": value, "event_cursor": value, "turn_seq": value},
						"stream": map[string]any{"truncated_before": value},
					},
					"usage": map[string]any{"total_tokens": value},
					"sdk": map[string]any{
						"context_window_tokens": value,
						"usage":                 map[string]any{"total_tokens": value, "cost_micros": value},
					},
				},
			}}
			migrated, err := session.MigrateEvent(event)
			if err != nil {
				t.Fatal(err)
			}
			envelope := noticeEnvelope()
			envelope.Meta = migrated.Meta
			raw := mustMarshalEnvelope(t, envelope)
			decimal := strconv.FormatUint(value, 10)
			assertKnownMetadataDecimals(t, raw, decimal)
			if err := openAPIValidator(t, "Envelope").Validate(decodeJSONWithNumbers(t, raw)); err != nil {
				t.Fatalf("durable metadata Envelope does not conform: %v\nJSON: %s", err, raw)
			}
		})
	}
}

func TestUint64DecimalSchemasRejectOverflowAndZeroSequence(t *testing.T) {
	uint64Validator := openAPIValidator(t, "Uint64Decimal")
	for _, value := range []string{"0", "9007199254740993", "18446744073709551615"} {
		if err := uint64Validator.Validate(value); err != nil {
			t.Fatalf("Uint64Decimal rejected %q: %v", value, err)
		}
	}
	for _, value := range []string{"00", "01", "18446744073709551616", "99999999999999999999"} {
		if err := uint64Validator.Validate(value); err == nil {
			t.Fatalf("Uint64Decimal accepted %q", value)
		}
	}
	positiveValidator := openAPIValidator(t, "PositiveUint64Decimal")
	if err := positiveValidator.Validate("1"); err != nil {
		t.Fatalf("PositiveUint64Decimal rejected 1: %v", err)
	}
	for _, value := range []string{"0", "18446744073709551616"} {
		if err := positiveValidator.Validate(value); err == nil {
			t.Fatalf("PositiveUint64Decimal accepted %q", value)
		}
	}
}

func TestUnsafeExtensionIntegerMustUseDecimalString(t *testing.T) {
	envelope := baseEnvelope(eventstream.KindSessionUpdate)
	envelope.Update = schema.RawUpdate{
		SessionUpdate: "vendor/custom",
		Raw:           json.RawMessage(`{"sessionUpdate":"vendor/custom","unsafe":9007199254740993}`),
	}
	if _, err := MarshalEnvelope(envelope); err == nil {
		t.Fatal("unsafe extension JSON number was emitted")
	}
	envelope.Update = schema.RawUpdate{
		SessionUpdate: "vendor/custom",
		Raw:           json.RawMessage(`{"sessionUpdate":"vendor/custom","unsafe":"9007199254740993"}`),
	}
	raw := mustMarshalEnvelope(t, envelope)
	if !bytes.Contains(raw, []byte(`"unsafe":"9007199254740993"`)) {
		t.Fatalf("decimal string extension was not preserved: %s", raw)
	}
}

func TestWireNumberGuardComparesDecimalBoundsWithoutFloatRounding(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"value":9007199254740991.1}`),
		json.RawMessage(`{"value":-9007199254740991.1}`),
		json.RawMessage(`{"value":9.0071992547409911e15}`),
		json.RawMessage(`{"value":1e1000000000}`),
	} {
		if err := ValidateJSONNumbers(raw); err == nil {
			t.Fatalf("unsafe numeric token accepted: %s", raw)
		}
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"value":9007199254740991}`),
		json.RawMessage(`{"value":-9007199254740991}`),
		json.RawMessage(`{"value":1.25}`),
		json.RawMessage(`{"value":1e-1000000000}`),
	} {
		if err := ValidateJSONNumbers(raw); err != nil {
			t.Fatalf("safe numeric token rejected: %s: %v", raw, err)
		}
	}
}

func TestGeneratedTypeScriptUsesDecimalStringForUint64WireFields(t *testing.T) {
	data, err := os.ReadFile("../../../clients/typescript/control-v1.gen.ts")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, field := range []string{"expected_revision", "revision", "seq", "sequence", "context_sync_seq"} {
		pattern := regexp.MustCompile(`(?m)^\s*` + field + `\??:\s*number;`)
		if pattern.MatchString(text) {
			t.Fatalf("generated TypeScript declares %s as number", field)
		}
	}
	for _, want := range []string{
		"export type Uint64Decimal = string;",
		"export type PositiveUint64Decimal = string;",
		"expected_revision?: Uint64Decimal;",
		"revision: Uint64Decimal;",
		"seq: Uint64Decimal;",
		"sequence: PositiveUint64Decimal;",
		"context_sync_seq?: Uint64Decimal;",
		"output_cursor?: Uint64Decimal;",
		"event_cursor?: Uint64Decimal;",
		"turn_seq?: Uint64Decimal;",
		"truncated_before?: Uint64Decimal;",
		"prompt_tokens?: Uint64Decimal;",
		"context_window_tokens?: Uint64Decimal;",
		"cost_micros?: Uint64Decimal;",
		"summarized_through_seq?: Uint64Decimal;",
		"source_event_count?: Uint64Decimal;",
		"size: Uint64Decimal;",
		"used: Uint64Decimal;",
	} {
		if !stringsContains(text, want) {
			t.Fatalf("generated TypeScript missing %q", want)
		}
	}
}

func mustMarshalWire(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustMarshalEnvelope(t *testing.T, envelope eventstream.Envelope) []byte {
	t.Helper()
	raw, err := MarshalEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertGeneratedDecimal(t *testing.T, value *generated.Uint64Decimal, want uint64, name string) {
	t.Helper()
	if value == nil {
		t.Fatalf("%s is nil", name)
	}
	parsed, err := strconv.ParseUint(string(*value), 10, 64)
	if err != nil || parsed != want {
		t.Fatalf("%s = %q (%v), want %d", name, *value, err, want)
	}
}

func assertKnownMetadataDecimals(t *testing.T, raw []byte, want string) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		t.Fatal(err)
	}
	caelis := root["_meta"].(map[string]any)["caelis"].(map[string]any)
	runtime := caelis["runtime"].(map[string]any)
	task := runtime["task"].(map[string]any)
	for _, key := range []string{"output_cursor", "event_cursor", "turn_seq"} {
		if got := task[key]; got != want {
			t.Fatalf("%s = %#v, want %q", key, got, want)
		}
	}
	if got := runtime["stream"].(map[string]any)["truncated_before"]; got != want {
		t.Fatalf("truncated_before = %#v, want %q", got, want)
	}
	if got := caelis["usage"].(map[string]any)["total_tokens"]; got != want {
		t.Fatalf("total_tokens = %#v, want %q", got, want)
	}
	sdk := caelis["sdk"].(map[string]any)
	if got := sdk["context_window_tokens"]; got != want {
		t.Fatalf("sdk context_window_tokens = %#v, want %q", got, want)
	}
	for _, key := range []string{"total_tokens", "cost_micros"} {
		if got := sdk["usage"].(map[string]any)[key]; got != want {
			t.Fatalf("sdk usage %s = %#v, want %q", key, got, want)
		}
	}
	meta := root["_meta"].(map[string]any)
	for _, key := range []string{"from", "to"} {
		if got := meta[key].(map[string]any)["context_sync_seq"]; got != want {
			t.Fatalf("%s context_sync_seq = %#v, want %q", key, got, want)
		}
	}
	compact := meta["compact"].(map[string]any)
	for _, key := range []string{
		"revision", "contract_version", "summarized_through_seq",
		"source_event_count", "total_tokens", "context_window_tokens",
	} {
		if got := compact[key]; got != want {
			t.Fatalf("compact %s = %#v, want %q", key, got, want)
		}
	}
}

func stringsContains(value, fragment string) bool {
	return bytes.Contains([]byte(value), []byte(fragment))
}
