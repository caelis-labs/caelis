package chat

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

func mcpToolForTest(name string) tool.Tool {
	return tool.NamedTool{
		Def: tool.Definition{
			Name:        name,
			Description: "Create calendar events",
			InputSchema: map[string]any{"type": "object"},
			Metadata: map[string]any{
				mcp.MetadataKind:              mcp.KindTool,
				mcp.MetadataPluginID:          "calendar",
				mcp.MetadataServer:            "demo",
				mcp.MetadataTool:              "create_event",
				mcp.MetadataSourceFingerprint: "fixture-v1",
			},
		},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{
				ID:      call.ID,
				Name:    call.Name,
				Content: []tool.ContentBlock{tool.StructuredContent{Value: []byte(`{"value":"created"}`)}},
			}, nil
		},
	}
}

type readyMCPSource struct{ tools []tool.Tool }

func (s *readyMCPSource) Tools() []tool.Tool { return append([]tool.Tool(nil), s.tools...) }
