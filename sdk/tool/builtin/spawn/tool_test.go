package spawn

import (
	"testing"

	sdkdelegation "github.com/OnslaughtSnail/caelis/sdk/delegation"
)

func TestDefinitionDoesNotExposeYieldTimeMS(t *testing.T) {
	t.Parallel()

	def := New([]sdkdelegation.Agent{{Name: "codex"}}).Definition()
	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v, want object", def.InputSchema["properties"])
	}
	if _, ok := props["yield_time_ms"]; ok {
		t.Fatalf("SPAWN properties include yield_time_ms: %#v", props)
	}
}
