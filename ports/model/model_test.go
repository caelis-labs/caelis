package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageFromToolCallsNormalizesInvalidToolInputForJSONPersistence(t *testing.T) {
	rawArgs := `* SEARCH "gm_license"`
	msg := MessageFromToolCalls(RoleAssistant, []ToolCall{{
		ID:   "call-search",
		Name: "SEARCH",
		Args: rawArgs,
	}}, "")

	if len(msg.Parts) != 1 || msg.Parts[0].ToolUse == nil {
		t.Fatalf("message parts = %#v, want one tool-use part", msg.Parts)
	}
	if !json.Valid(msg.Parts[0].ToolUse.Input) {
		t.Fatalf("tool input = %q, want valid JSON for persistence", string(msg.Parts[0].ToolUse.Input))
	}
	if _, err := json.Marshal(msg); err != nil {
		t.Fatalf("json.Marshal(Message) returned error: %v", err)
	}
	if got := msg.ToolCalls()[0].Args; got != rawArgs {
		t.Fatalf("ToolCalls()[0].Args = %q, want original raw args %q", got, rawArgs)
	}

	encoded := string(msg.Parts[0].ToolUse.Input)
	if !strings.Contains(encoded, rawToolUseInputKey) || !strings.Contains(encoded, "gm_license") {
		t.Fatalf("tool input = %q, want wrapped raw args", encoded)
	}
}

func TestToolSpecsFromDefinitionsPreservesStrictCapability(t *testing.T) {
	t.Parallel()

	specs := ToolSpecsFromDefinitions([]ToolDefinition{{
		Name:        "closed",
		Description: "closed strict tool",
		Strict:      true,
		Parameters:  map[string]any{"type": "object"},
	}})
	if len(specs) != 1 || specs[0].Function == nil {
		t.Fatalf("specs = %#v, want one function spec", specs)
	}
	if !specs[0].Function.Strict {
		t.Fatalf("Function.Strict = false, want true")
	}

	defs := FunctionToolDefinitions(specs)
	if len(defs) != 1 || !defs[0].Strict {
		t.Fatalf("FunctionToolDefinitions() = %#v, want strict preserved", defs)
	}
}

func TestMessageFromToolCallsKeepsValidJSONToolInput(t *testing.T) {
	rawArgs := `{"query":"gm_license"}`
	msg := MessageFromToolCalls(RoleAssistant, []ToolCall{{
		ID:   "call-search",
		Name: "SEARCH",
		Args: rawArgs,
	}}, "")

	if len(msg.Parts) != 1 || msg.Parts[0].ToolUse == nil {
		t.Fatalf("message parts = %#v, want one tool-use part", msg.Parts)
	}
	if got := string(msg.Parts[0].ToolUse.Input); got != rawArgs {
		t.Fatalf("tool input = %q, want valid JSON preserved as-is", got)
	}
	if got := msg.ToolCalls()[0].Args; got != rawArgs {
		t.Fatalf("ToolCalls()[0].Args = %q, want %q", got, rawArgs)
	}
	if strings.Contains(string(msg.Parts[0].ToolUse.Input), rawToolUseInputKey) {
		t.Fatalf("valid tool input should not be wrapped: %q", string(msg.Parts[0].ToolUse.Input))
	}
}

func TestMessageFromToolCallsPreservesValidJSONUsingRawInputKey(t *testing.T) {
	rawArgs := `{"__caelis_raw_tool_input":"valid external field"}`
	msg := MessageFromToolCalls(RoleAssistant, []ToolCall{{
		ID:   "call-external",
		Name: "EXTERNAL",
		Args: rawArgs,
	}}, "")

	if len(msg.Parts) != 1 || msg.Parts[0].ToolUse == nil {
		t.Fatalf("message parts = %#v, want one tool-use part", msg.Parts)
	}
	if got := string(msg.Parts[0].ToolUse.Input); got != rawArgs {
		t.Fatalf("tool input = %q, want valid JSON preserved as-is", got)
	}
	if got := msg.ToolCalls()[0].Args; got != rawArgs {
		t.Fatalf("ToolCalls()[0].Args = %q, want %q", got, rawArgs)
	}
}

func TestToolResponseUsesTextToolResultContent(t *testing.T) {
	msg := Message{
		Role: RoleTool,
		Parts: []Part{{
			Kind: PartKindToolResult,
			ToolResult: &ToolResultPart{
				ToolUseID: "call-1",
				Name:      "mcp__plugin__server__read_fixture",
				Content: []Part{
					NewTextPart("first line"),
					NewTextPart("second line"),
				},
			},
		}},
	}

	resp := msg.ToolResponse()
	if resp == nil {
		t.Fatal("ToolResponse() = nil, want response")
	}
	if resp.ID != "call-1" || resp.Name != "mcp__plugin__server__read_fixture" {
		t.Fatalf("ToolResponse() id/name = %q/%q", resp.ID, resp.Name)
	}
	if got, _ := resp.Result["result"].(string); got != "first line\nsecond line" {
		t.Fatalf("ToolResponse().Result[result] = %#v, want joined text", resp.Result["result"])
	}
}

func TestParseToolCallArgsRawPreservesNumericLexemes(t *testing.T) {
	rawArgs := `{"id":9007199254740993,"amount":0.12345678901234567890}`

	got, err := ParseToolCallArgsRaw(rawArgs)
	if err != nil {
		t.Fatalf("ParseToolCallArgsRaw() error = %v", err)
	}
	if string(got) != rawArgs {
		t.Fatalf("ParseToolCallArgsRaw() = %q, want original numeric lexemes %q", string(got), rawArgs)
	}
}

func TestParseToolCallArgsRawUnwrapsFencedJSON(t *testing.T) {
	rawArgs := "```json\n{\"id\":9007199254740993}\n```"

	got, err := ParseToolCallArgsRaw(rawArgs)
	if err != nil {
		t.Fatalf("ParseToolCallArgsRaw() error = %v", err)
	}
	if want := `{"id":9007199254740993}`; string(got) != want {
		t.Fatalf("ParseToolCallArgsRaw() = %q, want %q", string(got), want)
	}
}
