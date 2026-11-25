package mcp

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPContentOrderedImageLinkAndEmbeddedText(t *testing.T) {
	var encoded bytes.Buffer
	pixel := image.NewRGBA(image.Rect(0, 0, 1, 1))
	pixel.Set(0, 0, color.RGBA{B: 255, A: 255})
	if err := png.Encode(&encoded, pixel); err != nil {
		t.Fatal(err)
	}
	size := int64(123)
	parts, err := mcpContentBlocks([]mcpsdk.Content{
		&mcpsdk.TextContent{Text: "before"},
		&mcpsdk.ImageContent{Data: encoded.Bytes(), MIMEType: "image/png"},
		&mcpsdk.ResourceLink{URI: "https://example.test/resource", Name: "evidence", MIMEType: "text/plain", Title: "Evidence", Description: "An attachment", Size: &size},
		&mcpsdk.EmbeddedResource{Resource: &mcpsdk.ResourceContents{URI: "file:///memo", Text: "note", MIMEType: "text/plain"}},
		&mcpsdk.EmbeddedResource{Resource: &mcpsdk.ResourceContents{URI: "file:///empty", MIMEType: "text/plain"}},
		&mcpsdk.TextContent{Text: "after"},
	})
	if err != nil || len(parts) != 6 || parts[0] != tool.Text("before") || parts[5] != tool.Text("after") {
		t.Fatalf("ordered content = %+v, %v", parts, err)
	}
	image, ok := parts[1].(tool.Image)
	if !ok || image.MIMEType != "image/png" || !bytes.Equal(image.Data, encoded.Bytes()) {
		t.Fatalf("image block = %+v", parts[1])
	}
	link, ok := parts[2].(tool.Resource)
	if !ok || link.Text != nil || link.URI != "https://example.test/resource" || link.Name != "evidence" || link.MIMEType != "text/plain" || link.Title != "Evidence" || link.Description != "An attachment" || link.Size == nil || *link.Size != size {
		t.Fatalf("resource link = %+v", parts[2])
	}
	embedded, ok := parts[3].(tool.Resource)
	if !ok || embedded.Text == nil || *embedded.Text != "note" || embedded.URI != "file:///memo" || embedded.MIMEType != "text/plain" {
		t.Fatalf("embedded resource = %+v", parts[3])
	}
	empty, ok := parts[4].(tool.Resource)
	if !ok || empty.Text == nil || *empty.Text != "" || empty.URI != "file:///empty" {
		t.Fatalf("embedded resource with empty text = %+v", parts[4])
	}
}

func TestMCPUnsupportedAndOversizedContentIsExplicit(t *testing.T) {
	parts, err := mcpContentBlocks([]mcpsdk.Content{
		&mcpsdk.ImageContent{Data: []byte("not-png"), MIMEType: "image/png"},
		&mcpsdk.AudioContent{Data: []byte("audio"), MIMEType: "audio/wav"},
		&mcpsdk.EmbeddedResource{Resource: &mcpsdk.ResourceContents{URI: "file:///blob", Blob: []byte("binary")}},
		&mcpsdk.TextContent{Text: "survives"},
	})
	if err == nil || len(parts) != 4 || !strings.Contains(err.Error(), "audio content is not supported") || parts[3] != tool.Text("survives") {
		t.Fatalf("unsupported content = %+v, %v", parts, err)
	}
	if _, err := structuredContentBlock(map[string]string{"large": strings.Repeat("x", maxMCPStructuredBytes)}, false); err == nil {
		t.Fatal("oversized structured content accepted")
	}
}
