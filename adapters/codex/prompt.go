package codex

import (
	"encoding/base64"
	"fmt"
	"strings"

	acp "github.com/caelis-labs/acp-go-sdk"
)

func promptInput(blocks []acp.ContentBlock) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		switch {
		case block.Text != nil:
			out = append(out, map[string]any{"type": "text", "text": block.Text.Text})
		case block.Image != nil:
			mime := strings.TrimSpace(block.Image.MimeType)
			if mime == "" {
				mime = "image/png"
			}
			out = append(out, map[string]any{
				"type": "image", "url": "data:" + mime + ";base64," + block.Image.Data,
			})
		case block.Resource != nil:
			if block.Resource.Resource.TextResourceContents != nil {
				resource := block.Resource.Resource.TextResourceContents
				out = append(out, map[string]any{
					"type": "text", "text": fmt.Sprintf("Context from %s:\n%s", resource.Uri, resource.Text),
				})
				continue
			}
			return nil, fmt.Errorf("binary embedded resources are not supported")
		case block.ResourceLink != nil:
			out = append(out, map[string]any{
				"type": "text", "text": fmt.Sprintf("Referenced resource: %s", block.ResourceLink.Uri),
			})
		case block.Audio != nil:
			if _, err := base64.StdEncoding.DecodeString(block.Audio.Data); err != nil {
				return nil, fmt.Errorf("invalid audio data: %w", err)
			}
			return nil, fmt.Errorf("audio prompts are not supported")
		default:
			return nil, fmt.Errorf("unsupported ACP content block")
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("prompt has no supported content")
	}
	return out, nil
}
