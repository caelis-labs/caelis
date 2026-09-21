package client

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
)

func TestAntigravityCommandResultIsValidACPAndGainsDisplayContent(t *testing.T) {
	data, err := os.ReadFile("testdata/antigravity-command.json")
	if err != nil {
		t.Fatal(err)
	}
	var updates []json.RawMessage
	if err := json.Unmarshal(data, &updates); err != nil {
		t.Fatal(err)
	}
	for i, raw := range updates {
		var standard acpsdk.SessionUpdate
		if err := json.Unmarshal(raw, &standard); err != nil {
			t.Fatal(err)
		}
		if err := standard.Validate(); err != nil {
			t.Fatalf("invalid ACP update %d: %v", i, err)
		}
		update, err := decodeUpdate(raw)
		if err != nil {
			t.Fatal(err)
		}
		normalized := NormalizeInboundUpdate(update)
		if i < 2 {
			if !reflect.DeepEqual(update, normalized) {
				t.Fatal("rewrote an unfinished tool call")
			}
			continue
		}
		got := normalized.(ToolCallUpdate)
		want := []ToolCallContent{{Type: "content", Content: map[string]any{"type": "text", "text": "AGY_OUTPUT_OK\n"}}}
		if !reflect.DeepEqual(got.Content, want) || !reflect.DeepEqual(got.RawOutput, update.(ToolCallUpdate).RawOutput) || got.Kind != nil || got.Name != nil {
			t.Fatalf("normalized result lost data or invented identity: %#v", got)
		}
		if !reflect.DeepEqual(got, NormalizeInboundUpdate(got)) {
			t.Fatal("normalization is not idempotent")
		}
		if update.(ToolCallUpdate).Content != nil {
			t.Fatal("mutated source update")
		}
	}
}

func TestAntigravityCommandContentRespectsExplicitContentAndTerminalStreams(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*ToolCallUpdate)
	}{
		{"running", func(u *ToolCallUpdate) { s := "in_progress"; u.Status = &s }},
		{"missing status", func(u *ToolCallUpdate) { u.Status = nil }},
		{"explicit empty", func(u *ToolCallUpdate) { u.Content = []ToolCallContent{} }},
		{"standard text", func(u *ToolCallUpdate) {
			u.Content = []ToolCallContent{{Type: "content", Content: map[string]any{"type": "text", "text": "standard output"}}}
		}},
		{"terminal content", func(u *ToolCallUpdate) { u.Content = []ToolCallContent{{Type: "terminal", TerminalID: "term-1"}} }},
		{"terminal metadata", func(u *ToolCallUpdate) { u.Meta = acpmeta.WithTerminalInfo(nil, "term-1") }},
		{"terminal delta", func(u *ToolCallUpdate) { u.Meta = acpmeta.WithTerminalOutput(nil, "term-1", "streamed\n") }},
		{"terminal exit", func(u *ToolCallUpdate) { code := 0; u.Meta = acpmeta.WithTerminalExit(nil, "term-1", &code, nil) }},
		{"other kind", func(u *ToolCallUpdate) { k := "read"; u.Kind = &k }},
		{"missing command", func(u *ToolCallUpdate) { delete(u.RawOutput.(map[string]any), "commandLine") }},
		{"missing directory", func(u *ToolCallUpdate) { delete(u.RawOutput.(map[string]any), "workingDir") }},
		{"invalid exit", func(u *ToolCallUpdate) { u.RawOutput.(map[string]any)["exitCode"] = "0" }},
		{"invalid output", func(u *ToolCallUpdate) { u.RawOutput.(map[string]any)["combinedOutput"] = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			status := "completed"
			u := ToolCallUpdate{Status: &status, RawOutput: map[string]any{"combinedOutput": "output\n", "commandLine": "echo output", "workingDir": "/workspace", "exitCode": 0}}
			test.change(&u)
			got := NormalizeInboundUpdate(u).(ToolCallUpdate)
			if !reflect.DeepEqual(got.Content, u.Content) {
				t.Fatalf("overrode content or expanded an unsupported shape: %#v", got.Content)
			}
		})
	}
	for _, status := range []string{"completed", "failed"} {
		u := ToolCall{Status: status, Kind: "execute", RawOutput: map[string]any{"combinedOutput": "  output\n\n", "commandLine": "echo output", "workingDir": "/workspace", "exitCode": 1}}
		got := NormalizeInboundUpdate(u).(ToolCall)
		if len(got.Content) != 1 || got.Content[0].Content.(map[string]any)["text"] != "  output\n\n" {
			t.Fatalf("one-shot result lost whitespace: %#v", got)
		}
	}
}
