package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/tool"
)

const (
	ContentKindText    = "text"
	ContentKindJSON    = "json"
	ContentKindFileRef = "file_ref"
	ContentKindMedia   = "media"
)

// Client is the transport-neutral MCP client contract used by the adapter.
type Client interface {
	ListTools(context.Context) ([]RemoteTool, error)
	CallTool(context.Context, string, map[string]any) (CallResult, error)
	Close() error
}

// RemoteTool is one MCP tool declaration normalized into Caelis schema types.
type RemoteTool struct {
	Name        string
	Description string
	InputSchema tool.Schema
	Metadata    map[string]any
}

// CallResult is the normalized MCP tool call result.
type CallResult struct {
	Content  []ContentPart
	IsError  bool
	Metadata map[string]any
}

// ContentPart is one normalized MCP content part.
type ContentPart struct {
	Kind     string
	Text     string
	JSON     any
	MIMEType string
	Data     []byte
	URI      string
}

// Config configures one MCP-backed toolset.
type Config struct {
	Name           string
	Client         Client
	ToolNamePrefix string
}

// NewToolset returns a toolset backed by one MCP client.
func NewToolset(cfg Config) tool.Toolset {
	return &toolset{cfg: cfg}
}

type toolset struct {
	cfg Config
}

func (s *toolset) Name() string {
	return strings.TrimSpace(s.cfg.Name)
}

func (s *toolset) Tools(ctx context.Context) ([]tool.Tool, error) {
	if s == nil || s.cfg.Client == nil {
		return nil, fmt.Errorf("tool/mcp: client is required")
	}
	remoteTools, err := s.cfg.Client.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tool.Tool, 0, len(remoteTools))
	for _, remote := range remoteTools {
		name := strings.TrimSpace(remote.Name)
		if name == "" {
			continue
		}
		remote.Name = name
		out = append(out, &remoteTool{
			serverName: strings.TrimSpace(s.cfg.Name),
			prefix:     s.cfg.ToolNamePrefix,
			client:     s.cfg.Client,
			remote:     remote,
		})
	}
	return out, nil
}

type remoteTool struct {
	serverName string
	prefix     string
	client     Client
	remote     RemoteTool
}

func (t *remoteTool) Definition() tool.Definition {
	metadata := cloneMetadata(t.remote.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["mcp_server"] = t.serverName
	metadata["mcp_tool"] = t.remote.Name
	return tool.Definition{
		Name:        t.prefixedName(),
		Description: t.remote.Description,
		Schema:      t.remote.InputSchema,
		Metadata:    metadata,
	}
}

func (t *remoteTool) Run(ctx tool.Context, call tool.Call) (tool.Result, error) {
	result, err := t.client.CallTool(ctx, t.remote.Name, call.Args)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("MCP tool %s error: %v", t.remote.Name, err), IsError: true}, nil
	}
	parts := make([]tool.ResultPart, 0, len(result.Content))
	var texts []string
	for _, part := range result.Content {
		mapped := mapContentPart(part)
		if mapped.Kind != "" {
			parts = append(parts, mapped)
		}
		if mapped.Kind == ContentKindText && mapped.Text != "" {
			texts = append(texts, mapped.Text)
		}
	}
	return tool.Result{
		Output:   strings.Join(texts, "\n"),
		Parts:    parts,
		IsError:  result.IsError,
		Metadata: cloneMetadata(result.Metadata),
	}, nil
}

func (t *remoteTool) prefixedName() string {
	prefix := strings.TrimSpace(t.prefix)
	if prefix == "" {
		return t.remote.Name
	}
	return prefix + t.remote.Name
}

func mapContentPart(part ContentPart) tool.ResultPart {
	switch part.Kind {
	case ContentKindText:
		return tool.ResultPart{Kind: ContentKindText, Text: part.Text, MIMEType: part.MIMEType}
	case ContentKindJSON:
		data, _ := json.Marshal(part.JSON)
		return tool.ResultPart{Kind: ContentKindJSON, Data: data, MIMEType: "application/json"}
	case ContentKindFileRef:
		return tool.ResultPart{Kind: ContentKindFileRef, URI: part.URI, MIMEType: part.MIMEType}
	case ContentKindMedia:
		return tool.ResultPart{Kind: ContentKindMedia, Data: append([]byte(nil), part.Data...), MIMEType: part.MIMEType, URI: part.URI}
	default:
		return tool.ResultPart{}
	}
}

func cloneMetadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
