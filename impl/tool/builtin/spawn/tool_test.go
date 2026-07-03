package spawn

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/ports/delegation"
	"github.com/caelis-labs/caelis/ports/tool"
)

func TestDefinitionDoesNotExposeYieldTimeMS(t *testing.T) {
	t.Parallel()

	def := New([]delegation.Agent{{Name: "codex"}}).Definition()
	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v, want object", def.InputSchema["properties"])
	}
	if _, ok := props["yield_time_ms"]; ok {
		t.Fatalf("SPAWN properties include yield_time_ms: %#v", props)
	}
	promptProp, _ := props["prompt"].(map[string]any)
	if got := promptProp["minLength"]; got != 1 {
		t.Fatalf("prompt minLength = %#v, want 1", got)
	}
}

func TestDefinitionGuidesWaitingWithTaskTool(t *testing.T) {
	t.Parallel()

	desc := New([]delegation.Agent{{Name: "codex"}}).Definition().Description
	for _, want := range []string{
		"To observe or wait for an existing child task, use TASK wait",
		"returned task_id",
		"do not call SPAWN again",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("SPAWN description missing %q:\n%s", want, desc)
		}
	}
}

func TestDefinitionExposesOpenWorldAnnotations(t *testing.T) {
	t.Parallel()

	def := New([]delegation.Agent{{Name: "codex"}}).Definition()
	annotations, _ := def.Metadata["annotations"].(map[string]any)
	for key, want := range map[string]bool{
		"readOnlyHint":    false,
		"destructiveHint": true,
		"idempotentHint":  false,
		"openWorldHint":   true,
	} {
		if got := annotations[key]; got != want {
			t.Fatalf("annotation %s = %#v, want %v; metadata=%#v", key, got, want, def.Metadata)
		}
	}
}

func TestCallRejectsUnknownArgsBeforeRuntimeWrapperError(t *testing.T) {
	t.Parallel()

	raw, _ := json.Marshal(map[string]any{
		"prompt":        "inspect this",
		"yield_time_ms": 1000,
	})
	_, err := New([]delegation.Agent{{Name: "self"}}).Call(context.Background(), tool.Call{
		Name:  ToolName,
		Input: raw,
	})
	if err == nil {
		t.Fatal("SPAWN Call() error = nil, want unknown arg rejection")
	}
	if strings.Contains(err.Error(), "runtime wrapper") || !strings.Contains(err.Error(), "yield_time_ms") {
		t.Fatalf("SPAWN Call() error = %v, want yield_time_ms rejection before runtime wrapper error", err)
	}
}

func TestDefinitionPreservesAgentDescriptions(t *testing.T) {
	t.Parallel()

	def := New([]delegation.Agent{
		{Name: " reviewer ", Description: "read-only code review"},
		{Name: "builder"},
	}).Definition()
	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v, want object", def.InputSchema["properties"])
	}
	agentProp, ok := props["agent"].(map[string]any)
	if !ok {
		t.Fatalf("agent property = %#v, want object", props["agent"])
	}
	description, ok := agentProp["description"].(string)
	if !ok {
		t.Fatalf("agent description = %#v, want string", agentProp["description"])
	}
	for _, required := range []string{
		"reviewer: read-only code review",
		"builder",
		"omit for self",
	} {
		if !strings.Contains(description, required) {
			t.Fatalf("agent description missing %q: %q", required, description)
		}
	}
	required, _ := def.InputSchema["required"].([]string)
	if hasString(required, "agent") {
		t.Fatalf("required = %#v, want agent optional when enum agents exist", required)
	}
}

func TestDefinitionKeepsSelfFallbackInEnumAndOptional(t *testing.T) {
	t.Parallel()

	def := New([]delegation.Agent{{Name: "self", Description: "Caelis self ACP agent"}}).Definition()
	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v, want object", def.InputSchema["properties"])
	}
	agentProp, ok := props["agent"].(map[string]any)
	if !ok {
		t.Fatalf("agent property = %#v, want object", props["agent"])
	}
	enum, _ := agentProp["enum"].([]string)
	if len(enum) != 1 || enum[0] != "self" {
		t.Fatalf("agent enum = %#v, want self only", agentProp["enum"])
	}
	description, _ := agentProp["description"].(string)
	if !strings.Contains(description, "omit for self") {
		t.Fatalf("agent description = %q, want self fallback guidance", description)
	}
	required, _ := def.InputSchema["required"].([]string)
	if hasString(required, "agent") {
		t.Fatalf("required = %#v, want agent optional for self fallback", required)
	}
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
