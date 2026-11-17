package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type MCPTool struct {
	client     *Client
	manager    *Manager
	bindingID  uint64
	pluginID   string
	serverName string
	origName   string
	def        tool.Definition
}

func (t *MCPTool) Definition() tool.Definition {
	return tool.CloneDefinition(t.def)
}

// AdmitSource validates the callable's frozen source and capability revisions
// against the Manager's current directory. The returned guard orders permission
// consumption with directory replacement and invalidation. Release it before
// persistence or tool I/O; it does not make a Policy decision.
func (t *MCPTool) AdmitSource(ctx context.Context) (func(), error) {
	if t.manager == nil {
		return nil, nil
	}
	return t.manager.guardCatalogTool(ctx, t)
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
	if err := tool.ValidateMCPArguments(t.def, call.Input); err != nil {
		return tool.Result{}, err
	}
	if t.manager != nil {
		if err := t.manager.admitCatalogTool(ctx, t); err != nil {
			return tool.Result{}, err
		}
	}
	var args map[string]any
	if len(call.Input) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(call.Input))
		decoder.UseNumber()
		if err := decoder.Decode(&args); err != nil {
			return tool.Result{}, fmt.Errorf("invalid MCP tool arguments: %w", err)
		}
	}

	resp, err := t.client.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      t.origName,
		Arguments: args,
	})
	if err != nil {
		result := tool.Result{
			ID:      call.ID,
			Name:    call.Name,
			IsError: true,
			Content: []model.Part{
				model.NewTextPart(fmt.Sprintf("MCP execution error: %s", err.Error())),
			},
		}
		if tool.EffectClassOf(t.def) != tool.EffectReadOnly {
			return result, tool.WrapError(errorcode.UnknownOutcome, err, "MCP execution outcome is unknown: "+err.Error())
		}
		return result, nil
	}

	if resp == nil {
		result := tool.Result{ID: call.ID, Name: call.Name, IsError: true,
			Content: []model.Part{model.NewTextPart("MCP server returned no tool result")}}
		if tool.EffectClassOf(t.def) != tool.EffectReadOnly {
			return result, tool.NewError(errorcode.UnknownOutcome, "MCP server returned no tool execution receipt")
		}
		return result, nil
	}
	parts, contentErr := mcpContentParts(resp.Content)
	var source *tool.StructuredContent
	if resp.StructuredContent != nil {
		structured, err := structuredContentPart(resp.StructuredContent)
		if err != nil {
			contentErr = errors.Join(contentErr, err)
		} else {
			source = &tool.StructuredContent{Value: structured.JSON.Value, ContentIndex: len(parts)}
			parts = append(parts, structured)
			if t.def.OutputSchema != nil && !resp.IsError {
				if err := tool.ValidateMCPArguments(tool.Definition{InputSchema: t.def.OutputSchema}, source.Value); err != nil {
					contentErr = errors.Join(contentErr, fmt.Errorf("structuredContent does not match outputSchema: %w", err))
				}
			}
		}
	}
	if t.def.OutputSchema != nil && source == nil && !resp.IsError {
		contentErr = errors.Join(contentErr, errors.New("outputSchema requires structuredContent"))
	}
	if contentErr != nil {
		parts = append(parts, model.NewTextPart("MCP content not delivered: "+contentErr.Error()))
	}
	if len(parts) == 0 {
		parts = append(parts, model.NewTextPart(""))
	}

	return tool.Result{
		ID:                call.ID,
		Name:              call.Name,
		Content:           parts,
		StructuredContent: source,
		IsError:           resp.IsError || contentErr != nil,
	}, nil
}

var _ tool.Tool = (*MCPTool)(nil)
