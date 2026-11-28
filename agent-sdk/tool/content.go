package tool

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// ContentType identifies one tool result block on the wire.
type ContentType string

const (
	ContentTypeText       ContentType = "text"
	ContentTypeImage      ContentType = "image"
	ContentTypeResource   ContentType = "resource"
	ContentTypeStructured ContentType = "structured"
)

// ContentBlock is one ordered result block. Results accept the SDK's Text,
// Image, Resource and StructuredContent value types.
type ContentBlock interface {
	ContentType() ContentType
}

// Text is plain text, including text received from an external tool.
type Text string

func (Text) ContentType() ContentType { return ContentTypeText }

// Image contains original image bytes or their immutable Session attachment reference.
type Image struct {
	Data       []byte              `json:"data,omitempty"`
	Attachment model.AttachmentRef `json:"attachment,omitzero"`
	MIMEType   string              `json:"mime_type"`
	Name       string              `json:"name,omitempty"`
}

func (Image) ContentType() ContentType { return ContentTypeImage }

// Resource describes a resource link or embedded text. A nil Text is a link;
// a non-nil Text can carry an empty embedded document.
type Resource struct {
	URI         string  `json:"uri"`
	Name        string  `json:"name,omitempty"`
	MIMEType    string  `json:"mime_type,omitempty"`
	Title       string  `json:"title,omitempty"`
	Description string  `json:"description,omitempty"`
	Size        *int64  `json:"size,omitempty"`
	Text        *string `json:"text,omitempty"`
}

func (Resource) ContentType() ContentType { return ContentTypeResource }

// StructuredContent holds the sole business JSON value of a result. A block
// containing JSON null differs from a result with no structured block.
type StructuredContent struct {
	Value json.RawMessage `json:"value"`
}

func (StructuredContent) ContentType() ContentType { return ContentTypeStructured }

// Structured returns the business JSON and whether this result contains it.
func (r Result) Structured() (json.RawMessage, bool) {
	for _, block := range r.Content {
		if data, ok := block.(StructuredContent); ok {
			return data.Value, true
		}
	}
	return nil, false
}

// ValidateContent validates the supported blocks and enforces one structured
// payload per result. It does not apply a tool's output schema.
func ValidateContent(blocks []ContentBlock) error {
	structured := false
	for i, block := range blocks {
		switch b := block.(type) {
		case Text, legacyResultBlock:
		case Image:
			if (len(b.Data) == 0) == (b.Attachment.Instance == "") || !strings.HasPrefix(b.MIMEType, "image/") {
				return fmt.Errorf("result block %d has invalid image content", i)
			}
		case Resource:
			if strings.TrimSpace(b.URI) == "" {
				return fmt.Errorf("result block %d has no resource URI", i)
			}
		case StructuredContent:
			if structured {
				return fmt.Errorf("result has multiple structured content blocks")
			}
			if !json.Valid(b.Value) {
				return fmt.Errorf("result block %d has invalid structured JSON", i)
			}
			structured = true
		default:
			return fmt.Errorf("result block %d has unsupported type %T", i, block)
		}
	}
	return nil
}

func cloneContent(blocks []ContentBlock) []ContentBlock {
	if blocks == nil {
		return nil
	}
	out := make([]ContentBlock, len(blocks))
	for i, block := range blocks {
		switch b := block.(type) {
		case Image:
			b.Data = bytes.Clone(b.Data)
			block = b
		case Resource:
			if b.Size != nil {
				b.Size = new(*b.Size)
			}
			if b.Text != nil {
				b.Text = new(*b.Text)
			}
			block = b
		case StructuredContent:
			b.Value = bytes.Clone(b.Value)
			block = b
		case legacyResultBlock:
			block = legacyResultBlock{part: model.CloneParts([]model.Part{b.part})[0]}
		}
		out[i] = block
	}
	return out
}

// ModelParts projects tool content onto the model vocabulary in block order.
// Call identity and outcome belong to the caller's model result envelope.
func ModelParts(blocks []ContentBlock) []model.Part {
	if len(blocks) == 0 {
		return nil
	}
	parts := make([]model.Part, 0, len(blocks))
	for _, block := range blocks {
		switch b := block.(type) {
		case Text:
			parts = append(parts, model.NewTextPart(string(b)))
		case Image:
			source := model.MediaSource{Kind: model.MediaSourceInline, Data: base64.StdEncoding.EncodeToString(b.Data)}
			if b.Attachment.Instance != "" {
				source = model.MediaSource{Kind: model.MediaSourceAttachment, Attachment: b.Attachment}
			}
			parts = append(parts, model.NewMediaPart(model.MediaModalityImage, source, b.MIMEType, b.Name))
		case Resource:
			parts = append(parts, model.NewTextPart(resourceText(b)))
		case StructuredContent:
			parts = append(parts, model.NewJSONPart(b.Value))
		case legacyResultBlock:
			parts = append(parts, model.CloneParts([]model.Part{b.part})[0])
		}
	}
	return parts
}

func resourceText(r Resource) string {
	label := r.Title
	if label == "" {
		label = r.Name
	}
	if label == "" {
		label = "Resource"
	}
	lines := []string{fmt.Sprintf("%s: %s", label, r.URI)}
	if r.MIMEType != "" {
		lines = append(lines, "MIME type: "+r.MIMEType)
	}
	if r.Size != nil {
		lines = append(lines, fmt.Sprintf("Size: %d bytes", *r.Size))
	}
	if r.Description != "" {
		lines = append(lines, r.Description)
	}
	if r.Text != nil {
		lines = append(lines, *r.Text)
	}
	return strings.Join(lines, "\n")
}
