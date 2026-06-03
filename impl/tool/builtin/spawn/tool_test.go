package spawn

import (
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/delegation"
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
}
