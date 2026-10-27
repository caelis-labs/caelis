package eventstream

import (
	"bytes"
	"encoding/json"
)

// UnmarshalJSON preserves rawInput and metadata number tokens across JSON-backed
// observation caches. Other open fields retain ordinary JSON decoding.
func (t *ToolCall) UnmarshalJSON(raw []byte) error {
	type alias ToolCall
	var decoded alias
	fields := struct {
		*alias
		RawInput json.RawMessage `json:"rawInput"`
		Meta     json.RawMessage `json:"_meta"`
	}{alias: &decoded}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	input, err := decodeToolInput(fields.RawInput)
	if err != nil {
		return err
	}
	decoded.RawInput = input
	decoded.Meta, err = decodeUpdateMetadata(fields.Meta)
	if err != nil {
		return err
	}
	*t = ToolCall(decoded)
	return nil
}

// UnmarshalJSON preserves rawInput and metadata number tokens across JSON-backed
// observation caches without changing sparse-patch presence.
func (t *ToolCallUpdate) UnmarshalJSON(raw []byte) error {
	type alias ToolCallUpdate
	var decoded alias
	fields := struct {
		*alias
		RawInput json.RawMessage `json:"rawInput"`
		Meta     json.RawMessage `json:"_meta"`
	}{alias: &decoded}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	input, err := decodeToolInput(fields.RawInput)
	if err != nil {
		return err
	}
	decoded.RawInput = input
	decoded.Meta, err = decodeUpdateMetadata(fields.Meta)
	if err != nil {
		return err
	}
	*t = ToolCallUpdate(decoded)
	return nil
}

func decodeToolInput(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var input any
	err := decoder.Decode(&input)
	return input, err
}

// UnmarshalJSON keeps opaque chunk metadata out of the default float64 decoder.
func (c *ContentChunk) UnmarshalJSON(raw []byte) error {
	type alias ContentChunk
	var decoded alias
	fields := struct {
		*alias
		Meta json.RawMessage `json:"_meta"`
	}{alias: &decoded}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	var err error
	decoded.Meta, err = decodeUpdateMetadata(fields.Meta)
	if err != nil {
		return err
	}
	*c = ContentChunk(decoded)
	return nil
}

func decodeUpdateMetadata(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var meta map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	err := decoder.Decode(&meta)
	return meta, err
}
