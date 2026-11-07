package chat

import (
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"testing"
)

type bindingCatalogSource struct {
	tools        []tool.Tool
	enumerations int
}

func (s *bindingCatalogSource) Tools() []tool.Tool { s.enumerations++; return s.tools }

type bindingCountedTool struct {
	tool.Tool
	definitions *int
}

func (t bindingCountedTool) Definition() tool.Definition {
	*t.definitions++
	return t.Tool.Definition()
}

func TestBindMCPCallsBuildsCatalogOnlyForResolvedDeclaration(t *testing.T) {
	for _, kind := range []string{"ordinary", "impostor", "alias", "canonical", "malformed", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			definitions := 0
			base := mcpToolForTest("docs__read")
			if kind == "unsupported" {
				def := base.Definition()
				def.RequiredModelCapabilities = model.Capabilities{ImageInput: true}
				base = tool.NamedTool{Def: def}
			}
			target := bindingCountedTool{base, &definitions}
			source := &bindingCatalogSource{tools: []tool.Tool{target}}
			name := "mcp_call"
			declaration := tool.NewMCPCallTool()
			switch kind {
			case "ordinary":
				name = "Read"
				declaration = tool.NamedTool{Def: tool.Definition{Name: name}}
			case "impostor":
				declaration = tool.NamedTool{Def: tool.Definition{Name: name}}
			case "alias":
				name = "registered_alias"
			}
			a := &Agent{toolsByName: map[string]tool.Tool{name: declaration}, deferredTools: source, discoveries: map[string]tool.Binding{"docs__read": tool.BindingForDefinition(target.Tool.Definition())}}
			args := `{"tool_name":"docs__read","arguments":{}}`
			if kind == "malformed" {
				args = `{"tool_name":"docs__read","arguments":[]}`
			}
			a.bindMCPCalls([]model.ToolCall{{ID: "one", Name: name, Args: args}, {ID: "two", Name: name, Args: args}}, nil)
			if kind == "ordinary" || kind == "impostor" {
				if source.enumerations != 0 || len(a.boundCalls) != 0 || definitions != 0 {
					t.Fatalf("unrelated declaration enumerated catalog: %+v %d", source, definitions)
				}
				return
			}
			if source.enumerations != 1 {
				t.Fatalf("catalog enumerations=%d", source.enumerations)
			}
			for _, id := range []string{"one", "two"} {
				bound, ok := a.boundCalls[id]
				if !ok || bound.original.Name != name {
					t.Fatalf("lost resolved identity/original: %+v", bound)
				}
				if kind == "malformed" || kind == "unsupported" {
					if bound.err == nil || bound.target != nil {
						t.Fatalf("malformed envelope bound: %+v", bound)
					}
				} else if bound.err != nil || bound.target == nil {
					t.Fatalf("resolved MCP declaration failed: %+v", bound)
				}
			}
			if kind != "malformed" && kind != "unsupported" && definitions != 3 {
				t.Fatalf("same validation reread Definition: %d", definitions)
			}
		})
	}
}
