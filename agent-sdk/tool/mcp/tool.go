package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type MCPTool struct {
	client     *Client
	pluginID   string
	serverName string
	origName   string
	def        tool.Definition
}

func (t *MCPTool) Definition() tool.Definition {
	return tool.CloneDefinition(t.def)
}

func (t *MCPTool) addReplayAliases(in []string) {
	if t == nil || len(in) == 0 {
		return
	}
	if t.def.Metadata == nil {
		t.def.Metadata = map[string]any{}
	}
	aliases, _ := t.def.Metadata[tool.MetadataReplayAliases].([]string)
	for _, alias := range in {
		if alias == "" {
			continue
		}
		found := false
		for _, existing := range aliases {
			if existing == alias {
				found = true
				break
			}
		}
		if !found {
			aliases = append(aliases, alias)
		}
	}
	t.def.Metadata[tool.MetadataReplayAliases] = aliases
}

func (t *MCPTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args map[string]any
	if len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, &args); err != nil {
			return tool.Result{}, fmt.Errorf("invalid MCP tool arguments: %w", err)
		}
	}

	resp, err := t.client.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      t.origName,
		Arguments: args,
	})
	if err != nil {
		return tool.Result{
			ID:      call.ID,
			Name:    call.Name,
			IsError: true,
			Content: []model.Part{
				model.NewTextPart(fmt.Sprintf("MCP execution error: %s", err.Error())),
			},
		}, nil
	}

	if resp == nil {
		return tool.Result{ID: call.ID, Name: call.Name, IsError: true,
			Content: []model.Part{model.NewTextPart("MCP server returned no tool result")}}, nil
	}
	parts, contentErr := mcpContentParts(resp.Content)
	if resp.StructuredContent != nil {
		structured, err := structuredContentPart(resp.StructuredContent)
		if err != nil {
			contentErr = errors.Join(contentErr, err)
		} else {
			parts = append(parts, structured)
		}
	}
	if contentErr != nil {
		parts = append(parts, model.NewTextPart("MCP content not delivered: "+contentErr.Error()))
	}
	if len(parts) == 0 {
		parts = append(parts, model.NewTextPart(""))
	}

	return tool.Result{
		ID:      call.ID,
		Name:    call.Name,
		Content: parts,
		IsError: resp.IsError || contentErr != nil,
	}, nil
}

var _ tool.Tool = (*MCPTool)(nil)
