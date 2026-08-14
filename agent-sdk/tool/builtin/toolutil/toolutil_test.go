package toolutil

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestAnnotationMetadata(t *testing.T) {
	t.Parallel()

	got := AnnotationMetadata(true, false, true, false)
	annotations, ok := got["annotations"].(map[string]any)
	if !ok {
		t.Fatalf("annotations type = %T, want map[string]any", got["annotations"])
	}
	if annotations["readOnlyHint"] != true ||
		annotations["destructiveHint"] != false ||
		annotations["idempotentHint"] != true ||
		annotations["openWorldHint"] != false {
		t.Fatalf("annotations = %#v, want expected hint values", annotations)
	}
}

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
			wantErr: "tool: invalid json input:",
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

	result, err := JSONResult(" read_file ", map[string]any{"ok": true}, map[string]any{
		"display": "Read README.md",
		"":        "ignored",
	})
	if err != nil {
		t.Fatalf("JSONResult() error = %v, want nil", err)
	}
	if result.Name != "read_file" {
		t.Fatalf("result.Name = %q, want %q", result.Name, "read_file")
	}
	if len(result.Content) != 1 {
		t.Fatalf("len(result.Content) = %d, want 1", len(result.Content))
	}
	if result.Content[0].Kind != model.PartKindJSON || result.Content[0].JSON == nil {
		t.Fatalf("result.Content[0] = %#v, want JSON part", result.Content[0])
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(payload) error = %v", err)
	}
	if payload["ok"] != true {
		t.Fatalf("payload[ok] = %v, want true", payload["ok"])
	}
	caelis, ok := result.Metadata["caelis"].(map[string]any)
	if !ok {
		t.Fatalf("metadata.caelis type = %T, want map[string]any", result.Metadata["caelis"])
	}
	runtime, ok := caelis["runtime"].(map[string]any)
	if !ok {
		t.Fatalf("metadata.caelis.runtime type = %T, want map[string]any", caelis["runtime"])
	}
	toolMeta, ok := runtime["tool"].(map[string]any)
	if !ok {
		t.Fatalf("metadata.runtime.tool type = %T, want map[string]any", runtime["tool"])
	}
	if toolMeta["display"] != "Read README.md" {
		t.Fatalf("toolMeta[display] = %v, want %q", toolMeta["display"], "Read README.md")
	}
	if _, exists := toolMeta[""]; exists {
		t.Fatal("toolMeta contains empty key, want it skipped")
	}
}

func TestWithContextCancel(t *testing.T) {
	t.Parallel()

	if err := WithContextCancel(context.Background()); err != nil {
		t.Fatalf("WithContextCancel(background) = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WithContextCancel(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("WithContextCancel(canceled) = %v, want context.Canceled", err)
	}
}
