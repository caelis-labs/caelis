package wirev1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlclient "github.com/caelis-labs/caelis/control/client"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func unmarshalWireValue(raw []byte, target any) error {
	switch typed := target.(type) {
	case *controlclient.CommandResult:
		normalized, err := normalizeObjectUint64Fields(raw, "revision")
		if err != nil {
			return err
		}
		return unmarshalStrict(normalized, typed)
	case *controlclient.DisconnectCandidatesSnapshot:
		normalized, err := normalizeObjectUint64Fields(raw, "revision")
		if err != nil {
			return err
		}
		return unmarshalStrict(normalized, typed)
	case *controlagents.ACPPreparation:
		normalized, err := normalizeObjectUint64Fields(raw, "observed_revision")
		if err != nil {
			return err
		}
		return unmarshalStrict(normalized, typed)
	case *controlclient.SessionState:
		normalized, err := normalizeSessionStateJSON(raw)
		if err != nil {
			return err
		}
		return unmarshalStrict(normalized, typed)
	case *controlstatus.StatusSnapshot:
		normalized, err := normalizeStatusSnapshotJSON(raw)
		if err != nil {
			return err
		}
		return unmarshalStrict(normalized, typed)
	default:
		return unmarshalStrict(raw, target)
	}
}

func normalizeStatusSnapshotJSON(raw []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	configuration, ok := fields["configuration"]
	if !ok {
		return nil, fmt.Errorf("control wire v1: status configuration is required")
	}
	normalized, err := normalizeObjectUint64Fields(configuration, "revision")
	if err != nil {
		return nil, err
	}
	fields["configuration"] = normalized
	return json.Marshal(fields)
}

// Unmarshal decodes one Control value according to the v1 JSON contract.
func Unmarshal(raw []byte, target any) error {
	return unmarshalWireValue(raw, target)
}

func unmarshalWireEnvelope(raw []byte) (eventstream.Envelope, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return eventstream.Envelope{}, err
	}
	update := append(json.RawMessage(nil), fields["update"]...)
	fields["update"] = json.RawMessage("null")
	if position, ok := fields["position"]; ok {
		normalized, err := normalizeFeedPositionJSON(position)
		if err != nil {
			return eventstream.Envelope{}, err
		}
		fields["position"] = normalized
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return eventstream.Envelope{}, err
	}
	var envelope eventstream.Envelope
	if err := json.Unmarshal(normalized, &envelope); err != nil {
		return eventstream.Envelope{}, err
	}
	if len(update) > 0 && string(update) != "null" {
		update, err = normalizeUpdateJSON(update)
		if err != nil {
			return eventstream.Envelope{}, err
		}
		envelope.Update, err = schema.DecodeUpdateJSON(update)
		if err != nil {
			return eventstream.Envelope{}, err
		}
	}
	envelope.Meta, err = parseKnownMetadata(envelope.Meta)
	if err != nil {
		return eventstream.Envelope{}, err
	}
	switch typed := envelope.Update.(type) {
	case schema.ContentChunk:
		typed.Meta, err = parseKnownMetadata(typed.Meta)
		envelope.Update = typed
	case schema.ToolCall:
		typed.Meta, err = parseKnownMetadata(typed.Meta)
		envelope.Update = typed
	case schema.ToolCallUpdate:
		typed.Meta, err = parseKnownMetadata(typed.Meta)
		envelope.Update = typed
	case schema.UsageUpdate:
		typed.Meta, err = parseKnownMetadata(typed.Meta)
		envelope.Update = typed
	}
	if err != nil {
		return eventstream.Envelope{}, err
	}
	if envelope.Permission != nil {
		envelope.Permission.Meta, err = parseKnownMetadata(envelope.Permission.Meta)
		if err != nil {
			return eventstream.Envelope{}, err
		}
		envelope.Permission.ToolCall.Meta, err = parseKnownMetadata(envelope.Permission.ToolCall.Meta)
		if err != nil {
			return eventstream.Envelope{}, err
		}
	}
	return envelope, nil
}

// UnmarshalEnvelope decodes one typed ACP-shaped Control Envelope.
func UnmarshalEnvelope(raw []byte) (eventstream.Envelope, error) {
	return unmarshalWireEnvelope(raw)
}

func normalizeSessionStateJSON(raw []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if err := normalizeRawUint64Field(fields, "revision"); err != nil {
		return nil, err
	}
	if position, ok := fields["boundary_position"]; ok {
		normalized, err := normalizeFeedPositionJSON(position)
		if err != nil {
			return nil, err
		}
		fields["boundary_position"] = normalized
	}
	if controller, ok := fields["controller"]; ok {
		normalized, err := normalizeObjectUint64Fields(controller, "context_sync_seq")
		if err != nil {
			return nil, err
		}
		fields["controller"] = normalized
	}
	if participants, ok := fields["participants"]; ok {
		var values []json.RawMessage
		if err := json.Unmarshal(participants, &values); err != nil {
			return nil, err
		}
		for index := range values {
			normalized, err := normalizeObjectUint64Fields(values[index], "context_sync_seq")
			if err != nil {
				return nil, err
			}
			values[index] = normalized
		}
		normalized, err := json.Marshal(values)
		if err != nil {
			return nil, err
		}
		fields["participants"] = normalized
	}
	return json.Marshal(fields)
}

func normalizeUpdateJSON(raw json.RawMessage) (json.RawMessage, error) {
	var probe struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	if probe.SessionUpdate != schema.UpdateUsage {
		return raw, nil
	}
	return normalizeObjectUint64Fields(raw, "size", "used")
}

func normalizeFeedPositionJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if durable, ok := fields["durable"]; ok {
		normalized, err := normalizeObjectUint64Fields(durable, "seq")
		if err != nil {
			return nil, err
		}
		fields["durable"] = normalized
	}
	if transient, ok := fields["transient"]; ok {
		var transientFields map[string]json.RawMessage
		if err := json.Unmarshal(transient, &transientFields); err != nil {
			return nil, err
		}
		if err := normalizeRawUint64Field(transientFields, "sequence"); err != nil {
			return nil, err
		}
		if anchor, ok := transientFields["anchor"]; ok {
			normalized, err := normalizeObjectUint64Fields(anchor, "seq")
			if err != nil {
				return nil, err
			}
			transientFields["anchor"] = normalized
		}
		normalized, err := json.Marshal(transientFields)
		if err != nil {
			return nil, err
		}
		fields["transient"] = normalized
	}
	return json.Marshal(fields)
}

func normalizeObjectUint64Fields(raw json.RawMessage, names ...string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for _, name := range names {
		if err := normalizeRawUint64Field(fields, name); err != nil {
			return nil, err
		}
	}
	return json.Marshal(fields)
}

func normalizeRawUint64Field(fields map[string]json.RawMessage, name string) error {
	raw, ok := fields[name]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var decimal string
	if err := json.Unmarshal(raw, &decimal); err != nil {
		return fmt.Errorf("control wire v1: %s must be a uint64 decimal string", name)
	}
	value, err := parseUint64Decimal(decimal)
	if err != nil {
		return fmt.Errorf("control wire v1: invalid %s: %w", name, err)
	}
	fields[name] = json.RawMessage(fmt.Sprintf("%d", value))
	return nil
}

func unmarshalStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("control wire v1: response contains more than one JSON value")
	}
	return nil
}
