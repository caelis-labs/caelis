package toolsearch

import (
	"context"
	"fmt"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"testing"
)

type exactNamedFixture struct {
	old, next       tool.Tool
	lookups, scopes int
	revoke          bool
}

func (s *exactNamedFixture) Tools() []tool.Tool { panic("exact named source enumerated directory") }
func (s *exactNamedFixture) Lookup(context.Context, string) (tool.Tool, bool, error) {
	s.lookups++
	if s.lookups == 2 && s.next != nil {
		return s.next, true, nil
	}
	return s.old, true, nil
}
func (s *exactNamedFixture) CheckSearchScope(context.Context) error {
	s.scopes++
	if s.revoke && s.scopes == 2 {
		return fmt.Errorf("scope revoked")
	}
	return nil
}

func TestExactNamedLookupRevalidatesScopeAndDefinition(t *testing.T) {
	for _, change := range []string{"stable", "schema", "source", "origin", "scope", "alias"} {
		t.Run(change, func(t *testing.T) {
			def := tool.Definition{Name: "fixture__read", InputSchema: map[string]any{"type": "object"}, Metadata: map[string]any{tool.MetadataToolKind: tool.MetadataToolKindMCP, tool.MetadataMCPServer: "fixture", tool.MetadataMCPTool: "read", tool.MetadataMCPSourceFingerprint: "v1"}}
			source := &exactNamedFixture{old: tool.NamedTool{Def: def}}
			next := tool.CloneDefinition(def)
			switch change {
			case "schema":
				next.InputSchema = map[string]any{"type": "object", "required": []string{"new"}}
				source.next = tool.NamedTool{Def: next}
			case "source":
				next.Metadata[tool.MetadataMCPSourceFingerprint] = "v2"
				source.next = tool.NamedTool{Def: next}
			case "origin":
				next.Metadata[tool.MetadataPluginID] = "other-source"
				source.next = tool.NamedTool{Def: next}
			case "scope":
				source.revoke = true
			case "alias":
				def.Name = "different__read"
				source.old = tool.NamedTool{Def: def}
			}
			result, err := NewSource(source).Call(t.Context(), tool.Call{Input: []byte(`{"name":"fixture__read"}`)})
			if change == "stable" {
				if err != nil || result.IsError || source.lookups != 2 || source.scopes != 2 {
					t.Fatalf("stable lookup failed: %+v %v %+v", result, err, source)
				}
			} else if err == nil {
				t.Fatalf("changed scope/schema/source or wrong name admitted: %+v", result)
			}
		})
	}
}
