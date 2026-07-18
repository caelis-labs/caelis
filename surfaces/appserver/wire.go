package appserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"

	controlclient "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

const maxSafeJSONInteger = uint64(1<<53 - 1)

type wireDurableFeedPosition struct {
	Seq             string `json:"seq"`
	ProjectionIndex uint32 `json:"projection_index"`
}

type wireTransientFeedPosition struct {
	Anchor     wireDurableFeedPosition `json:"anchor"`
	Generation string                  `json:"generation"`
	Sequence   string                  `json:"sequence"`
}

type wireFeedPosition struct {
	Durable   *wireDurableFeedPosition   `json:"durable,omitempty"`
	Transient *wireTransientFeedPosition `json:"transient,omitempty"`
}

func wirePosition(position *eventstream.FeedPosition) *wireFeedPosition {
	if position == nil {
		return nil
	}
	out := &wireFeedPosition{}
	if position.Durable != nil {
		out.Durable = &wireDurableFeedPosition{
			Seq:             strconv.FormatUint(position.Durable.Seq, 10),
			ProjectionIndex: position.Durable.ProjectionIndex,
		}
	}
	if position.Transient != nil {
		out.Transient = &wireTransientFeedPosition{
			Anchor: wireDurableFeedPosition{
				Seq:             strconv.FormatUint(position.Transient.Anchor.Seq, 10),
				ProjectionIndex: position.Transient.Anchor.ProjectionIndex,
			},
			Generation: position.Transient.Generation,
			Sequence:   strconv.FormatUint(position.Transient.Sequence, 10),
		}
	}
	return out
}

func marshalWireValue(value any) ([]byte, error) {
	raw, err := marshalWireValueUnchecked(value)
	if err != nil {
		return nil, err
	}
	if err := validateWireJSONNumbers(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func marshalWireValueUnchecked(value any) ([]byte, error) {
	switch typed := value.(type) {
	case controlclient.CreateSessionRequest:
		return marshalWriteRequest(typed, typed.ExpectedRevision)
	case controlclient.CloseSessionRequest:
		return marshalWriteRequest(typed, typed.ExpectedRevision)
	case controlclient.PromptRequest:
		return marshalWriteRequest(typed, typed.ExpectedRevision)
	case controlclient.SteerRequest:
		return marshalWriteRequest(typed, typed.ExpectedRevision)
	case controlclient.CancelRequest:
		return marshalWriteRequest(typed, typed.ExpectedRevision)
	case controlclient.ResolveApprovalRequest:
		return marshalWriteRequest(typed, typed.ExpectedRevision)
	case controlclient.AttachParticipantRequest:
		return marshalWriteRequest(typed, typed.ExpectedRevision)
	case controlclient.PromptParticipantRequest:
		return marshalWriteRequest(typed, typed.ExpectedRevision)
	case controlclient.CancelParticipantRequest:
		return marshalWriteRequest(typed, typed.ExpectedRevision)
	case controlclient.DetachParticipantRequest:
		return marshalWriteRequest(typed, typed.ExpectedRevision)
	case controlclient.HandoffRequest:
		return marshalWriteRequest(typed, typed.ExpectedRevision)
	case controlclient.CommandResult:
		return marshalCommandResult(typed)
	case controlclient.SessionState:
		return marshalSessionState(typed)
	case controlclient.EventBatch:
		return marshalEventBatch(typed)
	case eventstream.Envelope:
		return marshalEnvelope(typed)
	default:
		return json.Marshal(value)
	}
}

func marshalWriteRequest(request any, expectedRevision *uint64) ([]byte, error) {
	fields, err := marshalObject(request)
	if err != nil {
		return nil, err
	}
	if expectedRevision != nil {
		fields["expected_revision"] = decimalRaw(*expectedRevision)
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	if err := validateWireJSONNumbers(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func marshalCommandResult(result controlclient.CommandResult) ([]byte, error) {
	fields, err := marshalObject(result)
	if err != nil {
		return nil, err
	}
	if result.Revision != 0 {
		fields["revision"] = decimalRaw(result.Revision)
	}
	return json.Marshal(fields)
}

func marshalSessionState(state controlclient.SessionState) ([]byte, error) {
	if err := validateSafeInt("queued_count", state.Approval.QueuedCount); err != nil {
		return nil, err
	}
	if state.BoundaryPosition != nil {
		if err := state.BoundaryPosition.Validate(); err != nil {
			return nil, fmt.Errorf("appserver: invalid boundary position: %w", err)
		}
	}
	fields, err := marshalObject(state)
	if err != nil {
		return nil, err
	}
	fields["revision"] = decimalRaw(state.Revision)
	if state.BoundaryPosition != nil {
		fields["boundary_position"], err = json.Marshal(wirePosition(state.BoundaryPosition))
		if err != nil {
			return nil, err
		}
	}
	fields["controller"], err = marshalBinding(state.Controller, state.Controller.ContextSyncSeq)
	if err != nil {
		return nil, err
	}
	if len(state.Participants) > 0 {
		participants := make([]json.RawMessage, 0, len(state.Participants))
		for _, participant := range state.Participants {
			raw, marshalErr := marshalBinding(participant, participant.ContextSyncSeq)
			if marshalErr != nil {
				return nil, marshalErr
			}
			participants = append(participants, raw)
		}
		fields["participants"], err = json.Marshal(participants)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(fields)
}

func marshalBinding(binding any, contextSyncSeq uint64) (json.RawMessage, error) {
	fields, err := marshalObject(binding)
	if err != nil {
		return nil, err
	}
	if contextSyncSeq > 0 {
		fields["context_sync_seq"] = decimalRaw(contextSyncSeq)
	}
	return json.Marshal(fields)
}

func marshalEventBatch(batch controlclient.EventBatch) ([]byte, error) {
	type wireBatch struct {
		Events         []json.RawMessage        `json:"events,omitempty"`
		ResumeMode     controlclient.ResumeMode `json:"resume_mode"`
		TransientGap   bool                     `json:"transient_gap,omitempty"`
		BoundaryCursor string                   `json:"boundary_cursor,omitempty"`
	}
	out := wireBatch{
		ResumeMode: batch.ResumeMode, TransientGap: batch.TransientGap, BoundaryCursor: batch.BoundaryCursor,
	}
	if len(batch.Events) > 0 {
		out.Events = make([]json.RawMessage, 0, len(batch.Events))
		for _, envelope := range batch.Events {
			raw, err := marshalEnvelope(envelope)
			if err != nil {
				return nil, err
			}
			out.Events = append(out.Events, raw)
		}
	}
	return json.Marshal(out)
}

func marshalEnvelope(envelope eventstream.Envelope) ([]byte, error) {
	wireEnvelope := eventstream.CloneEnvelope(envelope)
	if wireEnvelope.Position != nil {
		if err := wireEnvelope.Position.Validate(); err != nil {
			return nil, fmt.Errorf("appserver: invalid Envelope position: %w", err)
		}
	}
	if err := prepareEnvelopeMetadata(&wireEnvelope); err != nil {
		return nil, err
	}
	fields, err := marshalObject(wireEnvelope)
	if err != nil {
		return nil, err
	}
	if wireEnvelope.Position != nil {
		fields["position"], err = json.Marshal(wirePosition(wireEnvelope.Position))
		if err != nil {
			return nil, err
		}
	}
	if update, ok := wireEnvelope.Update.(schema.UsageUpdate); ok {
		fields["update"], err = marshalUsageUpdate(update)
		if err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	if err := validateWireJSONNumbers(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func prepareEnvelopeMetadata(envelope *eventstream.Envelope) error {
	if envelope == nil {
		return fmt.Errorf("appserver: nil Envelope")
	}
	var err error
	envelope.Meta, err = decimalizeKnownMetadata(envelope.Meta)
	if err != nil {
		return err
	}
	switch update := envelope.Update.(type) {
	case schema.ContentChunk:
		update.Meta, err = decimalizeKnownMetadata(update.Meta)
		envelope.Update = update
	case schema.ToolCall:
		err = validateLocations(update.Locations)
		if err == nil {
			update.Meta, err = decimalizeKnownMetadata(update.Meta)
		}
		envelope.Update = update
	case schema.ToolCallUpdate:
		err = validateLocations(update.Locations)
		if err == nil {
			update.Meta, err = decimalizeKnownMetadata(update.Meta)
		}
		envelope.Update = update
	case schema.UsageUpdate:
		update.Meta, err = decimalizeKnownMetadata(update.Meta)
		envelope.Update = update
	}
	if err != nil {
		return err
	}
	if envelope.Permission != nil {
		envelope.Permission.Meta, err = decimalizeKnownMetadata(envelope.Permission.Meta)
		if err != nil {
			return err
		}
		if err := validateLocations(envelope.Permission.ToolCall.Locations); err != nil {
			return err
		}
		envelope.Permission.ToolCall.Meta, err = decimalizeKnownMetadata(envelope.Permission.ToolCall.Meta)
	}
	return err
}

func marshalUsageUpdate(update schema.UsageUpdate) (json.RawMessage, error) {
	fields, err := marshalObject(update)
	if err != nil {
		return nil, err
	}
	fields["size"] = decimalRaw(update.Size)
	fields["used"] = decimalRaw(update.Used)
	return json.Marshal(fields)
}

func validateLocations(locations []schema.ToolCallLocation) error {
	for _, location := range locations {
		if location.Line != nil {
			if err := validateSafeInt("tool location line", *location.Line); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSafeInt(name string, value int) error {
	if value < 0 || uint64(value) > maxSafeJSONInteger {
		return fmt.Errorf("appserver: %s exceeds the exact JSON integer range", name)
	}
	return nil
}

func decimalizeKnownMetadata(meta map[string]any) (map[string]any, error) {
	out := metautil.CloneMap(meta)
	if len(out) == 0 {
		return out, nil
	}
	for _, key := range []string{"from", "to"} {
		binding, present, err := childMap(out, key)
		if err != nil {
			return nil, err
		}
		if present {
			if err := decimalizeMapValue(binding, "context_sync_seq"); err != nil {
				return nil, err
			}
		}
	}
	compact, hasCompact, err := childMap(out, "compact")
	if err != nil {
		return nil, err
	}
	if hasCompact {
		for _, key := range compactIntegerKeys() {
			if err := decimalizeMapValue(compact, key); err != nil {
				return nil, err
			}
		}
	}
	caelis, present, err := childMap(out, metautil.Root)
	if err != nil || !present {
		return out, err
	}
	runtime, hasRuntime, err := childMap(caelis, metautil.Runtime)
	if err != nil {
		return nil, err
	}
	if hasRuntime {
		if task, present, mapErr := childMap(runtime, metautil.RuntimeTask); mapErr != nil {
			return nil, mapErr
		} else if present {
			for _, key := range []string{"output_cursor", "event_cursor", "turn_seq"} {
				if err := decimalizeMapValue(task, key); err != nil {
					return nil, err
				}
			}
		}
		if stream, present, mapErr := childMap(runtime, metautil.RuntimeStream); mapErr != nil {
			return nil, mapErr
		} else if present {
			if err := decimalizeMapValue(stream, metautil.RuntimeStreamBefore); err != nil {
				return nil, err
			}
		}
	}
	if usage, present, mapErr := childMap(caelis, "usage"); mapErr != nil {
		return nil, mapErr
	} else if present {
		for _, key := range usageMetadataIntegerKeys(false) {
			if err := decimalizeMapValue(usage, key); err != nil {
				return nil, err
			}
		}
	}
	if sdk, present, mapErr := childMap(caelis, "sdk"); mapErr != nil {
		return nil, mapErr
	} else if present {
		if err := decimalizeMapValue(sdk, "context_window_tokens"); err != nil {
			return nil, err
		}
		if usage, present, usageErr := childMap(sdk, "usage"); usageErr != nil {
			return nil, usageErr
		} else if present {
			for _, key := range usageMetadataIntegerKeys(true) {
				if err := decimalizeMapValue(usage, key); err != nil {
					return nil, err
				}
			}
		}
	}
	return out, nil
}

func compactIntegerKeys() []string {
	return []string{
		"revision", "contract_version", "summarized_through_seq",
		"source_event_count", "total_tokens", "context_window_tokens",
	}
}

func usageMetadataIntegerKeys(includeCost bool) []string {
	keys := []string{
		"prompt_tokens", "cached_input_tokens", "completion_tokens",
		"reasoning_tokens", "total_tokens", "context_window_tokens",
	}
	if includeCost {
		keys = append(keys, "cost_micros")
	}
	return keys
}

func childMap(parent map[string]any, key string) (map[string]any, bool, error) {
	value, ok := parent[key]
	if !ok {
		return nil, false, nil
	}
	child, ok := value.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("appserver: _meta.%s must be an object", key)
	}
	return child, true, nil
}

func decimalizeMapValue(values map[string]any, key string) error {
	value, ok := values[key]
	if !ok {
		return nil
	}
	decimal, err := decimalString(value)
	if err != nil {
		return fmt.Errorf("appserver: _meta %s: %w", key, err)
	}
	values[key] = decimal
	return nil
}

func decimalString(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		parsed, err := parseUint64Decimal(typed)
		if err != nil {
			return "", err
		}
		return strconv.FormatUint(parsed, 10), nil
	case uint64:
		return strconv.FormatUint(typed, 10), nil
	case uint:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(typed), 10), nil
	case int:
		if typed < 0 {
			return "", fmt.Errorf("negative integer")
		}
		return strconv.FormatUint(uint64(typed), 10), nil
	case int64:
		if typed < 0 {
			return "", fmt.Errorf("negative integer")
		}
		return strconv.FormatUint(uint64(typed), 10), nil
	case int32:
		if typed < 0 {
			return "", fmt.Errorf("negative integer")
		}
		return strconv.FormatUint(uint64(typed), 10), nil
	case json.Number:
		parsed, err := parseUint64Decimal(typed.String())
		if err != nil {
			return "", err
		}
		return strconv.FormatUint(parsed, 10), nil
	case float64:
		if typed < 0 || typed > float64(maxSafeJSONInteger) || math.Trunc(typed) != typed {
			return "", fmt.Errorf("inexact JSON number")
		}
		return strconv.FormatUint(uint64(typed), 10), nil
	default:
		return "", fmt.Errorf("unsupported integer type %T", value)
	}
}

func decodeWireRequest(raw json.RawMessage, target any) error {
	if err := validateWireJSONNumbers(raw); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return fmt.Errorf("request body must be a JSON object")
	}
	if revisionRaw, ok := fields["expected_revision"]; ok {
		var decimal string
		if err := json.Unmarshal(revisionRaw, &decimal); err != nil {
			return fmt.Errorf("expected_revision must be a decimal string")
		}
		revision, err := parseUint64Decimal(decimal)
		if err != nil {
			return fmt.Errorf("invalid expected_revision: %w", err)
		}
		fields["expected_revision"] = json.RawMessage(strconv.FormatUint(revision, 10))
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(normalized))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func parseUint64Decimal(value string) (uint64, error) {
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || strconv.FormatUint(parsed, 10) != value {
		return 0, fmt.Errorf("invalid uint64 decimal %q", value)
	}
	return parsed, nil
}

func marshalObject(value any) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func decimalRaw(value uint64) json.RawMessage {
	return json.RawMessage(strconv.Quote(strconv.FormatUint(value, 10)))
}

func validateWireJSONNumbers(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("appserver: decode marshaled wire JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("appserver: marshaled wire JSON contains trailing data")
	}
	return validateWireJSONValue(value)
}

func validateWireJSONValue(value any) error {
	switch typed := value.(type) {
	case json.Number:
		text := typed.String()
		approximate, err := strconv.ParseFloat(text, 64)
		magnitude := math.Abs(approximate)
		if math.IsInf(approximate, 0) || math.IsNaN(approximate) || err != nil && magnitude != 0 {
			return fmt.Errorf("appserver: JSON number %q exceeds the exact JavaScript range; encode it as a string", typed)
		}
		maximum := float64(maxSafeJSONInteger)
		if magnitude < maximum {
			return nil
		}
		if magnitude > maximum || len(text) > 128 {
			return fmt.Errorf("appserver: JSON number %q exceeds the exact JavaScript range; encode it as a string", typed)
		}
		number, ok := new(big.Rat).SetString(text)
		if !ok || new(big.Rat).Abs(number).Cmp(big.NewRat(int64(maxSafeJSONInteger), 1)) > 0 {
			return fmt.Errorf("appserver: JSON number %q exceeds the exact JavaScript range; encode it as a string", typed)
		}
	case []any:
		for _, item := range typed {
			if err := validateWireJSONValue(item); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, item := range typed {
			if err := validateWireJSONValue(item); err != nil {
				return err
			}
		}
	}
	return nil
}
