package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type MCPTool struct {
	client    *Client
	manager   *Manager
	server    *managedServer
	origName  string
	def       tool.Definition
	bindingID uint64 // Manager-local admission identity; not a durable binding version.
}

func (t *MCPTool) Definition() tool.Definition {
	return tool.CloneDefinition(t.def)
}

func (t *MCPTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := tool.ValidateMCPArguments(t.def, call.Input); err != nil {
		return tool.Result{}, err
	}
	var args map[string]any
	if len(call.Input) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(call.Input))
		decoder.UseNumber()
		if err := decoder.Decode(&args); err != nil {
			return tool.Result{}, fmt.Errorf("invalid MCP tool arguments: %w", err)
		}
	}

	if t.manager != nil {
		release, err := t.manager.borrowClient(ctx, t)
		if err != nil {
			return tool.Result{}, err
		}
		defer release()
	}
	resp, err := t.client.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      t.origName,
		Arguments: args,
	})
	if err != nil {
		result := tool.Result{
			Content: []tool.ContentBlock{
				tool.Text(fmt.Sprintf("MCP execution error: %s", err.Error())),
			},
		}
		return result, tool.WrapError(errorcode.UnknownOutcome, err, "MCP execution outcome is unknown: "+err.Error())
	}

	if resp == nil {
		result := tool.Result{
			Content: []tool.ContentBlock{tool.Text("MCP server returned no tool result")}}
		return result, tool.NewError(errorcode.UnknownOutcome, "MCP server returned no tool execution receipt")
	}
	blocks, contentErr := mcpContentBlocks(resp.Content)
	if resp.StructuredContent != nil {
		structured, err := structuredContentBlock(resp.StructuredContent)
		if err != nil {
			contentErr = errors.Join(contentErr, err)
		} else {
			// MCP may send serialized structuredContent as a compatibility text
			// block. Only drop exact echoes; other text belongs to the server.
			echo := tool.Text(structured.Value)
			blocks = slices.DeleteFunc(blocks, func(block tool.ContentBlock) bool {
				text, ok := block.(tool.Text)
				return ok && text == echo
			})
			blocks = append(blocks, structured)
			if t.def.OutputSchema != nil && !resp.IsError {
				if err := tool.ValidateOutput(t.def.OutputSchema, structured.Value); err != nil {
					contentErr = errors.Join(contentErr, fmt.Errorf("structuredContent does not match outputSchema: %w", err))
				}
			}
		}
	} else if t.def.OutputSchema != nil && !resp.IsError {
		contentErr = errors.Join(contentErr, errors.New("outputSchema requires structuredContent"))
	}
	result := tool.Result{Content: blocks}
	var contentMessage string
	if contentErr != nil {
		contentMessage = "MCP content not delivered: " + contentErr.Error()
		result.Content = append(result.Content, tool.Text(contentMessage))
	}
	if len(result.Content) == 0 {
		result.Content = append(result.Content, tool.Text(""))
	}
	if resp.IsError {
		for _, block := range resp.Content {
			if text, ok := block.(*mcpsdk.TextContent); ok && strings.TrimSpace(text.Text) != "" {
				return result, tool.FailedReply(text.Text)
			}
		}
		return result, tool.FailedReply("MCP server reported a tool failure")
	}
	if contentErr != nil {
		return result, tool.FailedReply(contentMessage)
	}
	return result, nil
}

var _ tool.Tool = (*MCPTool)(nil)
