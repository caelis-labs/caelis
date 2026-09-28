package eventstream

import (
	"bytes"
	"encoding/json"
)

// UnmarshalJSON preserves rawInput number tokens across JSON-backed observation
// caches. Other open fields keep their ordinary JSON decoding behavior.
func (t *ToolCall) UnmarshalJSON(raw []byte) error {
	type alias ToolCall
	var decoded alias
	fields := struct {
		*alias
		RawInput json.RawMessage `json:"rawInput"`
	}{alias: &decoded}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	input, err := decodeToolInput(fields.RawInput)
	if err != nil {
		return err
	}
	decoded.RawInput = input
	*t = ToolCall(decoded)
	return nil
}

// UnmarshalJSON preserves rawInput number tokens across JSON-backed observation
// caches without changing sparse-patch presence or other open fields.
func (t *ToolCallUpdate) UnmarshalJSON(raw []byte) error {
	type alias ToolCallUpdate
	var decoded alias
	fields := struct {
		*alias
		RawInput json.RawMessage `json:"rawInput"`
	}{alias: &decoded}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	input, err := decodeToolInput(fields.RawInput)
	if err != nil {
		return err
	}
	decoded.RawInput = input
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
