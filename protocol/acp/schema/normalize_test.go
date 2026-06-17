package schema

import (
	"encoding/json"
	"testing"
)

type stringValue string

func (s stringValue) String() string {
	return string(s)
}

func TestNormalizeRawMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  any
		want string
	}{
		{
			name: "map",
			raw:  map[string]any{"stdout": "ok"},
			want: "ok",
		},
		{
			name: "raw message object",
			raw:  json.RawMessage(`{"stdout":"ok"}`),
			want: "ok",
		},
		{
			name: "raw message string",
			raw:  json.RawMessage(`not-json`),
			want: "not-json",
		},
		{
			name: "content text",
			raw:  map[string]any{"type": "text", "text": "ok"},
			want: "ok",
		},
		{
			name: "stringer",
			raw:  stringValue("ok"),
			want: "ok",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := NormalizeRawMap(tt.raw)
			if got["stdout"] == tt.want || got["text"] == tt.want {
				return
			}
			t.Fatalf("NormalizeRawMap(%T) = %#v, want value %q", tt.raw, got, tt.want)
		})
	}
}

func TestExtractTextValue(t *testing.T) {
	t.Parallel()

	got := ExtractTextValue([]any{
		map[string]any{"content": map[string]any{"type": "text", "text": "hello "}},
		map[string]any{"detailedContent": "world"},
	})
	if got != "hello world" {
		t.Fatalf("ExtractTextValue = %q, want nested text", got)
	}
}
