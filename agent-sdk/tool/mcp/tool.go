package mcp

import (
	"context"
	"encoding/json"
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
	return t.def
}

func (t *MCPTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args map[string]any
	if len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, &args); err != nil {
			return tool.Result{}, fmt.Errorf("mcp: failed to parse tool input arguments: %w", err)
		}
	}

	resp, err := t.client.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      t.origName,
		Arguments: args,
	})
	if err != nil {
		return tool.Result{
			Name:    call.Name,
			IsError: true,
			Content: []model.Part{
				model.NewTextPart(fmt.Sprintf("MCP execution error: %s", err.Error())),
			},
		}, nil
	}

	parts := mcpContentParts(resp.Content)
	if len(parts) == 0 && resp.StructuredContent != nil {
		parts = append(parts, structuredContentPart(resp.StructuredContent))
	}
	if len(parts) == 0 {
		parts = append(parts, model.NewTextPart(""))
	}

	return tool.Result{
		Name:    call.Name,
		Content: parts,
		IsError: resp.IsError,
	}, nil
}

func mcpContentParts(contents []mcpsdk.Content) []model.Part {
	var parts []model.Part
	for _, content := range contents {
		if content == nil {
			continue
		}
		switch c := content.(type) {
		case *mcpsdk.TextContent:
			parts = append(parts, model.NewTextPart(c.Text))
		case *mcpsdk.ImageContent:
			parts = append(parts, model.NewMediaPart(model.MediaModalityImage, model.MediaSource{
				Kind: model.MediaSourceInline,
				Data: string(c.Data),
			}, c.MIMEType, ""))
		case *mcpsdk.AudioContent:
			parts = append(parts, model.NewMediaPart(model.MediaModalityAudio, model.MediaSource{
				Kind: model.MediaSourceInline,
				Data: string(c.Data),
			}, c.MIMEType, ""))
		case *mcpsdk.ResourceLink:
			parts = append(parts, model.NewTextPart(formatResourceLink(c)))
		case *mcpsdk.EmbeddedResource:
			parts = append(parts, marshalJSONPart(c))
		default:
			parts = append(parts, marshalJSONPart(content))
		}
	}
	return parts
}

func structuredContentPart(value any) model.Part {
	return marshalJSONPart(value)
}

func marshalJSONPart(value any) model.Part {
	raw, err := json.Marshal(value)
	if err != nil || !json.Valid(raw) {
		return model.NewTextPart(fmt.Sprintf("%v", value))
	}
	return model.NewJSONPart(raw)
}

func formatResourceLink(link *mcpsdk.ResourceLink) string {
	if link == nil {
		return ""
	}
	label := firstNonEmpty(link.Title, link.Name, link.URI)
	if label == link.URI {
		return fmt.Sprintf("MCP resource: %s", link.URI)
	}
	return fmt.Sprintf("MCP resource: %s (%s)", label, link.URI)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

var _ tool.Tool = (*MCPTool)(nil)
