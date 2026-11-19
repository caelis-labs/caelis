package chat

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func mcpToolForTest(name string) tool.Tool {
	return tool.NamedTool{
		Def: tool.Definition{
			Name:        name,
			Description: "Create calendar events",
			InputSchema: map[string]any{"type": "object"},
			Metadata: map[string]any{
				tool.MetadataToolKind:             tool.MetadataToolKindMCP,
				tool.MetadataPluginID:             "calendar",
				tool.MetadataMCPServer:            "demo",
				tool.MetadataMCPTool:              "create_event",
				tool.MetadataMCPSourceFingerprint: "fixture-v1",
			},
		},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{
				ID:      call.ID,
				Name:    call.Name,
				Content: []model.Part{model.NewJSONPart([]byte(`{"value":"created"}`))},
			}, nil
		},
	}
}

type readyMCPSource struct{ tools []tool.Tool }

func (s *readyMCPSource) Tools() []tool.Tool { return append([]tool.Tool(nil), s.tools...) }
