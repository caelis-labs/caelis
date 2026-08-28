package session

import (
	"encoding/json"
	"testing"
)

type protocolStringValue string

func (s protocolStringValue) String() string {
	return string(s)
}

func TestNormalizeProtocolRawMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  any
		want string
	}{
		{name: "map", raw: map[string]any{"stdout": "ok"}, want: "ok"},
		{name: "raw message object", raw: json.RawMessage(`{"stdout":"ok"}`), want: "ok"},
		{name: "raw message string", raw: json.RawMessage(`not-json`), want: "not-json"},
		{name: "content text", raw: map[string]any{"type": "text", "text": "ok"}, want: "ok"},
		{name: "stringer", raw: protocolStringValue("ok"), want: "ok"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := NormalizeProtocolRawMap(test.raw)
			if got["stdout"] == test.want || got["text"] == test.want {
				return
			}
			t.Fatalf("NormalizeProtocolRawMap(%T) = %#v, want value %q", test.raw, got, test.want)
		})
	}
}

func TestExtractProtocolText(t *testing.T) {
	t.Parallel()

	got := ExtractProtocolText([]any{
		map[string]any{"content": map[string]any{"type": "text", "text": "hello "}},
		map[string]any{"detailedContent": "world"},
	})
	if got != "hello world" {
		t.Fatalf("ExtractProtocolText() = %q, want nested text", got)
	}
}

func TestExtractProtocolTextPreservesRawMessageFallbacks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value json.RawMessage
		want  string
	}{
		{name: "empty", value: nil, want: ""},
		{name: "encoded text", value: json.RawMessage(`{"type":"text","text":"hello"}`), want: "hello"},
		{name: "invalid json", value: json.RawMessage("  not-json  "), want: "not-json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ExtractProtocolText(test.value); got != test.want {
				t.Fatalf("ExtractProtocolText() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestExtractProtocolTextSupportsTypedContentObject(t *testing.T) {
	t.Parallel()

	value := struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: "text", Text: "hello"}
	if got := ExtractProtocolText(value); got != "hello" {
		t.Fatalf("ExtractProtocolText() = %q, want typed content text", got)
	}
}
