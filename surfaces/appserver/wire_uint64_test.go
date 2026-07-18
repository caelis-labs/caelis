package appserver

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/appserver/generated"
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

			request := controlclient.PromptRequest{
				WriteBase: controlclient.WriteBase{OperationID: "operation-1", SessionID: "session-1", ExpectedRevision: &value},
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
			var decodedRequest controlclient.PromptRequest
			if err := decodeWireRequest(requestJSON, &decodedRequest); err != nil {
				t.Fatal(err)
			}
			if decodedRequest.ExpectedRevision == nil || *decodedRequest.ExpectedRevision != value {
				t.Fatalf("decoded expected_revision = %#v, want %d", decodedRequest.ExpectedRevision, value)
			}

			resultJSON := mustMarshalWire(t, controlclient.CommandResult{
				OperationID: "operation-1", Outcome: controlclient.OutcomeCommitted, Revision: value,
			})
			var resultDTO generated.CommandResult
			if err := json.Unmarshal(resultJSON, &resultDTO); err != nil {
				t.Fatal(err)
			}
			assertGeneratedDecimal(t, resultDTO.Revision, value, "CommandResult.revision")

			state := controlclient.SessionState{
				ProtocolVersion: 1, EnvelopeVersion: controlclient.EnvelopeVersion, APIVersion: controlclient.HTTPAPIVersion,
				SessionID: "session-1", Revision: value, ResumeMode: controlclient.ResumeModeExact,
				BoundaryPosition: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
					Anchor: eventstream.DurableFeedPosition{Seq: value}, Generation: "generation-1", Sequence: value,
				}},
				Run:          controlclient.RunState{},
				Controller:   session.ControllerBinding{ContextSyncSeq: value},
				Participants: []session.ParticipantBinding{{ID: "participant-1", ContextSyncSeq: value}},
				Approval:     controlclient.ApprovalState{}, Capabilities: controlclient.ClientCapabilities{CaelisTerminalStream: true},
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
	if _, err := marshalEnvelope(envelope); err == nil {
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
		if err := validateWireJSONNumbers(raw); err == nil {
			t.Fatalf("unsafe numeric token accepted: %s", raw)
		}
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"value":9007199254740991}`),
		json.RawMessage(`{"value":-9007199254740991}`),
		json.RawMessage(`{"value":1.25}`),
		json.RawMessage(`{"value":1e-1000000000}`),
	} {
		if err := validateWireJSONNumbers(raw); err != nil {
			t.Fatalf("safe numeric token rejected: %s: %v", raw, err)
		}
	}
}

func TestGeneratedTypeScriptUsesDecimalStringForUint64WireFields(t *testing.T) {
	data, err := os.ReadFile("../../clients/typescript/control-v1.gen.ts")
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
	raw, err := marshalWireValue(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustMarshalEnvelope(t *testing.T, envelope eventstream.Envelope) []byte {
	t.Helper()
	raw, err := marshalEnvelope(envelope)
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
