package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxMCPImageBytes      = 8 << 20
	maxMCPImagePixels     = 20_000_000
	maxMCPEmbeddedText    = 64 << 10
	maxMCPStructuredBytes = 256 << 10
	maxMCPLinkField       = 2 << 10
)

// mcpContentParts keeps the server's ordered content. SDK media Data is
// decoded []byte; model inline media requires base64 text. Unsupported content
// is explicit and never turned into an arbitrary byte string or fetched URI.
func mcpContentParts(contents []mcpsdk.Content) ([]model.Part, error) {
	var parts []model.Part
	var problems []error
	for _, content := range contents {
		if content == nil {
			continue
		}
		switch c := content.(type) {
		case *mcpsdk.TextContent:
			parts = append(parts, model.NewTextPart(c.Text))
		case *mcpsdk.ImageContent:
			part, err := mcpImagePart(c)
			if err != nil {
				problems = append(problems, err)
				parts = append(parts, model.NewTextPart("[MCP image omitted: "+err.Error()+"]"))
			} else {
				parts = append(parts, part)
			}
		case *mcpsdk.AudioContent:
			problems = append(problems, errors.New("audio content is not supported"))
			parts = append(parts, model.NewTextPart("[MCP audio omitted: unsupported]"))
		case *mcpsdk.ResourceLink:
			part, err := mcpLinkPart(c)
			if err != nil {
				problems = append(problems, err)
				parts = append(parts, model.NewTextPart("[MCP resource link omitted: "+err.Error()+"]"))
			} else {
				parts = append(parts, part)
			}
		case *mcpsdk.EmbeddedResource:
			part, err := mcpEmbeddedTextPart(c)
			if err != nil {
				problems = append(problems, err)
				parts = append(parts, model.NewTextPart("[MCP embedded resource omitted: "+err.Error()+"]"))
			} else {
				parts = append(parts, part)
			}
		default:
			problems = append(problems, fmt.Errorf("unsupported MCP content type %T", content))
			parts = append(parts, model.NewTextPart("[MCP content omitted: unsupported type]"))
		}
	}
	return parts, errors.Join(problems...)
}

func mcpImagePart(content *mcpsdk.ImageContent) (model.Part, error) {
	if content == nil || len(content.Data) == 0 || len(content.Data) > maxMCPImageBytes {
		return model.Part{}, fmt.Errorf("empty or oversized image")
	}
	mime := strings.ToLower(strings.TrimSpace(content.MIMEType))
	if mime != "image/png" && mime != "image/jpeg" {
		return model.Part{}, fmt.Errorf("unsupported image MIME type")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(content.Data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxMCPImagePixels {
		return model.Part{}, fmt.Errorf("invalid or oversized image dimensions")
	}
	if (mime == "image/png" && format != "png") || (mime == "image/jpeg" && format != "jpeg") {
		return model.Part{}, fmt.Errorf("image bytes do not match MIME type")
	}
	if _, _, err := image.Decode(bytes.NewReader(content.Data)); err != nil {
		return model.Part{}, fmt.Errorf("invalid image encoding")
	}
	return model.NewMediaPart(model.MediaModalityImage, model.MediaSource{
		Kind: model.MediaSourceInline,
		Data: base64.StdEncoding.EncodeToString(content.Data),
	}, mime, ""), nil
}

func mcpLinkPart(link *mcpsdk.ResourceLink) (model.Part, error) {
	if link == nil || strings.TrimSpace(link.URI) == "" {
		return model.Part{}, fmt.Errorf("missing URI")
	}
	fields := []string{link.URI, link.Name, link.Title, link.Description, link.MIMEType}
	for _, field := range fields {
		if len(field) > maxMCPLinkField {
			return model.Part{}, fmt.Errorf("metadata exceeds limit")
		}
	}
	metadata := map[string]any{
		"type": "resource_link", "uri": link.URI,
		"name": link.Name, "title": link.Title, "mimeType": link.MIMEType,
	}
	if link.Description != "" {
		metadata["description"] = link.Description
	}
	if link.Size != nil {
		metadata["size"] = *link.Size
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return model.Part{}, err
	}
	return model.NewJSONPart(raw), nil
}

func mcpEmbeddedTextPart(content *mcpsdk.EmbeddedResource) (model.Part, error) {
	if content == nil || content.Resource == nil {
		return model.Part{}, fmt.Errorf("missing resource")
	}
	r := content.Resource
	if len(r.Blob) > 0 {
		return model.Part{}, fmt.Errorf("embedded blob is not supported")
	}
	if len(r.Text) > maxMCPEmbeddedText || len(r.URI) > maxMCPLinkField || len(r.MIMEType) > maxMCPLinkField {
		return model.Part{}, fmt.Errorf("embedded text exceeds limit")
	}
	raw, err := json.Marshal(map[string]any{
		"type":     "resource",
		"resource": map[string]any{"uri": r.URI, "mimeType": r.MIMEType, "text": r.Text},
	})
	if err != nil {
		return model.Part{}, err
	}
	return model.NewJSONPart(raw), nil
}

func structuredContentPart(value any) (model.Part, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > maxMCPStructuredBytes {
		return model.Part{}, fmt.Errorf("invalid or oversized structuredContent")
	}
	return model.NewJSONPart(raw), nil
}
