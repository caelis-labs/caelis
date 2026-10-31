package toolsearch

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type mutableDefinitionSource struct{ tools []tool.Tool }

func (s *mutableDefinitionSource) Tools() []tool.Tool { return s.tools }

func TestDefinitionStableAcrossCatalogChanges(t *testing.T) {
	source := &mutableDefinitionSource{}
	search := NewSource(source)
	before, err := json.Marshal(search.Definition())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "refreshed", ""} {
		source.tools = nil
		if name != "" {
			source.tools = []tool.Tool{mcpCandidate(name, name, name, name, name, map[string]any{"type": "object"})}
		}
		after, err := json.Marshal(search.Definition())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("catalog %q changed tool definition: %s -> %s", name, before, after)
		}
		def := search.Definition()
		def.InputSchema["type"] = "mutated"
	}
}
