package toolutil

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestDecodeArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   json.RawMessage
		want    map[string]any
		wantErr string
	}{
		{
			name:  "empty input",
			input: nil,
			want:  map[string]any{},
		},
		{
			name:  "valid object",
			input: json.RawMessage(`{"path":"README.md"}`),
			want:  map[string]any{"path": "README.md"},
		},
		{
			name:    "invalid json",
			input:   json.RawMessage(`{"path":`),
			wantErr: "invalid JSON input:",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := DecodeArgs(tool.Call{Input: tt.input})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("DecodeArgs() error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeArgs() error = %v, want nil", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("DecodeArgs() = %#v, want %#v", got, tt.want)
			}
			for key, want := range tt.want {
				if got[key] != want {
					t.Fatalf("DecodeArgs()[%q] = %v, want %v", key, got[key], want)
				}
			}
		})
	}
}

func TestJSONResult(t *testing.T) {
	t.Parallel()

	result, err := JSONResult(map[string]any{"ok": true})
	if err != nil {
		t.Fatalf("JSONResult() error = %v, want nil", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("len(result.Content) = %d, want 1", len(result.Content))
	}
	structured, ok := result.Structured()
	if !ok {
		t.Fatalf("result.Content[0] = %#v, want JSON part", result.Content[0])
	}
	var payload map[string]any
	if err := json.Unmarshal(structured, &payload); err != nil {
		t.Fatalf("json.Unmarshal(payload) error = %v", err)
	}
	if payload["ok"] != true {
		t.Fatalf("payload[ok] = %v, want true", payload["ok"])
	}

}
