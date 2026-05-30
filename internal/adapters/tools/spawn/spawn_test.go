package spawn

import (
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/plugin"
)

func TestDefinitionDoesNotExposeYieldTimeMS(t *testing.T) {
	t.Parallel()

	def := New([]Agent{{Name: "self"}}).Definition()
	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v, want object", def.InputSchema["properties"])
	}
	if _, ok := props["yield_time_ms"]; ok {
		t.Fatalf("SPAWN properties include yield_time_ms: %#v", props)
	}
	if def.Meta["caelis.kind"] != "spawn" {
		t.Fatalf("meta = %#v, want spawn kind", def.Meta)
	}
}

func TestDefinitionPreservesAgentDescriptions(t *testing.T) {
	t.Parallel()

	def := New([]Agent{
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

func TestAgentsFromPluginNormalizesDescriptors(t *testing.T) {
	t.Parallel()

	agents := AgentsFromPlugin([]plugin.ACPAgentDescriptor{{
		Name:        " helper ",
		Description: " external helper ",
	}})
	if len(agents) != 1 || agents[0].ID != "helper" || agents[0].Description != "external helper" {
		t.Fatalf("agents = %#v, want normalized helper", agents)
	}
}
