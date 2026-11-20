package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxMCPImageBytes      = 8 << 20
	maxMCPImagePixels     = 20_000_000
	maxMCPEmbeddedText    = 64 << 10
	maxMCPStructuredBytes = 256 << 10
	maxMCPLinkField       = 2 << 10
)

// mcpContentBlocks keeps the server's ordered content. Unsupported content is
// explicit and never turned into an arbitrary byte string or fetched URI.
func mcpContentBlocks(contents []mcpsdk.Content) ([]tool.ContentBlock, error) {
	var parts []tool.ContentBlock
	var problems []error
	for _, content := range contents {
		if content == nil {
			continue
		}
		switch c := content.(type) {
		case *mcpsdk.TextContent:
			parts = append(parts, tool.Text(c.Text))
		case *mcpsdk.ImageContent:
			part, err := mcpImageBlock(c)
			if err != nil {
				problems = append(problems, err)
				parts = append(parts, tool.Text("[MCP image omitted: "+err.Error()+"]"))
			} else {
				parts = append(parts, part)
			}
		case *mcpsdk.AudioContent:
			problems = append(problems, errors.New("audio content is not supported"))
			parts = append(parts, tool.Text("[MCP audio omitted: unsupported]"))
		case *mcpsdk.ResourceLink:
			part, err := mcpLinkBlock(c)
			if err != nil {
				problems = append(problems, err)
				parts = append(parts, tool.Text("[MCP resource link omitted: "+err.Error()+"]"))
			} else {
				parts = append(parts, part)
			}
		case *mcpsdk.EmbeddedResource:
			part, err := mcpEmbeddedTextBlock(c)
			if err != nil {
				problems = append(problems, err)
				parts = append(parts, tool.Text("[MCP embedded resource omitted: "+err.Error()+"]"))
			} else {
				parts = append(parts, part)
			}
		default:
			problems = append(problems, fmt.Errorf("unsupported MCP content type %T", content))
			parts = append(parts, tool.Text("[MCP content omitted: unsupported type]"))
		}
	}
	return parts, errors.Join(problems...)
}

func mcpImageBlock(content *mcpsdk.ImageContent) (tool.Image, error) {
	if content == nil || len(content.Data) == 0 || len(content.Data) > maxMCPImageBytes {
		return tool.Image{}, fmt.Errorf("empty or oversized image")
	}
	mime := strings.ToLower(strings.TrimSpace(content.MIMEType))
	if mime != "image/png" && mime != "image/jpeg" {
		return tool.Image{}, fmt.Errorf("unsupported image MIME type")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(content.Data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxMCPImagePixels {
		return tool.Image{}, fmt.Errorf("invalid or oversized image dimensions")
	}
	if (mime == "image/png" && format != "png") || (mime == "image/jpeg" && format != "jpeg") {
		return tool.Image{}, fmt.Errorf("image bytes do not match MIME type")
	}
	if _, _, err := image.Decode(bytes.NewReader(content.Data)); err != nil {
		return tool.Image{}, fmt.Errorf("invalid image encoding")
	}
	return tool.Image{Data: content.Data, MIMEType: mime}, nil
}

func mcpLinkBlock(link *mcpsdk.ResourceLink) (tool.Resource, error) {
	if link == nil || strings.TrimSpace(link.URI) == "" {
		return tool.Resource{}, fmt.Errorf("missing URI")
	}
	fields := []string{link.URI, link.Name, link.Title, link.Description, link.MIMEType}
	for _, field := range fields {
		if len(field) > maxMCPLinkField {
			return tool.Resource{}, fmt.Errorf("metadata exceeds limit")
		}
	}
	return tool.Resource{
		URI: link.URI, Name: link.Name, MIMEType: link.MIMEType,
		Title: link.Title, Description: link.Description, Size: link.Size,
	}, nil
}

func mcpEmbeddedTextBlock(content *mcpsdk.EmbeddedResource) (tool.Resource, error) {
	if content == nil || content.Resource == nil {
		return tool.Resource{}, fmt.Errorf("missing resource")
	}
	r := content.Resource
	if len(r.Blob) > 0 {
		return tool.Resource{}, fmt.Errorf("embedded blob is not supported")
	}
	if len(r.Text) > maxMCPEmbeddedText || len(r.URI) > maxMCPLinkField || len(r.MIMEType) > maxMCPLinkField {
		return tool.Resource{}, fmt.Errorf("embedded text exceeds limit")
	}
	text := r.Text
	return tool.Resource{URI: r.URI, MIMEType: r.MIMEType, Text: &text}, nil
}

func structuredContentBlock(value any) (tool.StructuredContent, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > maxMCPStructuredBytes {
		return tool.StructuredContent{}, fmt.Errorf("invalid or oversized structuredContent")
	}
	return tool.StructuredContent{Value: raw}, nil
}
