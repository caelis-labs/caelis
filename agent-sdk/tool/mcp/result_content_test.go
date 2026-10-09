package mcp

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPContentOrderedImageLinkAndEmbeddedText(t *testing.T) {
	var encoded bytes.Buffer
	pixel := image.NewRGBA(image.Rect(0, 0, 1, 1))
	pixel.Set(0, 0, color.RGBA{B: 255, A: 255})
	if err := png.Encode(&encoded, pixel); err != nil {
		t.Fatal(err)
	}
	parts, err := mcpContentParts([]mcpsdk.Content{
		&mcpsdk.TextContent{Text: "before"},
		&mcpsdk.ImageContent{Data: encoded.Bytes(), MIMEType: "image/png"},
		&mcpsdk.ResourceLink{URI: "https://example.test/resource", Name: "evidence", MIMEType: "text/plain"},
		&mcpsdk.EmbeddedResource{Resource: &mcpsdk.ResourceContents{URI: "file:///memo", Text: "note", MIMEType: "text/plain"}},
		&mcpsdk.TextContent{Text: "after"},
	})
	if err != nil || len(parts) != 5 || parts[0].Text == nil || parts[0].Text.Text != "before" || parts[4].Text == nil || parts[4].Text.Text != "after" {
		t.Fatalf("ordered content = %+v, %v", parts, err)
	}
	if parts[1].Kind != model.PartKindMedia || parts[1].Media == nil || parts[1].Media.MimeType != "image/png" {
		t.Fatalf("image part = %+v", parts[1])
	}
	decoded, err := base64.StdEncoding.DecodeString(parts[1].Media.Source.Data)
	if err != nil || !bytes.Equal(decoded, encoded.Bytes()) {
		t.Fatalf("PNG bytes changed: %v", err)
	}
	if parts[2].JSON == nil || !bytes.Contains(parts[2].JSON.Value, []byte(`"type":"resource_link"`)) || parts[3].JSON == nil || !bytes.Contains(parts[3].JSON.Value, []byte(`"text":"note"`)) {
		t.Fatalf("resource content = %+v", parts)
	}
}

func TestMCPUnsupportedAndOversizedContentIsExplicit(t *testing.T) {
	parts, err := mcpContentParts([]mcpsdk.Content{
		&mcpsdk.ImageContent{Data: []byte("not-png"), MIMEType: "image/png"},
		&mcpsdk.AudioContent{Data: []byte("audio"), MIMEType: "audio/wav"},
		&mcpsdk.EmbeddedResource{Resource: &mcpsdk.ResourceContents{URI: "file:///blob", Blob: []byte("binary")}},
		&mcpsdk.TextContent{Text: "survives"},
	})
	if err == nil || len(parts) != 4 || !strings.Contains(err.Error(), "audio content is not supported") || parts[3].Text == nil || parts[3].Text.Text != "survives" {
		t.Fatalf("unsupported content = %+v, %v", parts, err)
	}
	if _, err := structuredContentPart(map[string]string{"large": strings.Repeat("x", maxMCPStructuredBytes)}); err == nil {
		t.Fatal("oversized structured content accepted")
	}
}
