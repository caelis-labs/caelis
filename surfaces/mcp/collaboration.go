// Package mcp adapts the source-bound collaboration client to MCP stdio.
package mcp

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Run presents Host-selected tools; the authenticated Control endpoint
// enforces authority. This Surface owns no Session, grant or message state.
func Run(ctx context.Context, invoke collaboration.Invoke, transport mcp.Transport, definitions []tool.Definition) error {
	server := mcp.NewServer(&mcp.Implementation{Name: "caelis-collaboration", Version: "1"}, nil)
	for _, definition := range definitions {
		server.AddTool(&mcp.Tool{Name: definition.Name, Description: definition.Description, InputSchema: definition.InputSchema}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			out, err := invoke(ctx, collaboration.Request{Tool: definition.Name, Arguments: req.Params.Arguments})
			result := &mcp.CallToolResult{}
			if err != nil {
				result.IsError = true
				out = []byte(err.Error())
			}
			result.Content = []mcp.Content{&mcp.TextContent{Text: string(out)}}
			return result, nil
		})
	}
	return server.Run(ctx, transport)
}
