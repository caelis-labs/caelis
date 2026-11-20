package tool

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// legacyResultBlock preserves historical parts without promoting unmarked JSON
// to structured business data. Only the result reader constructs these blocks.
type legacyResultBlock struct{ part model.Part }

func (b legacyResultBlock) ContentType() ContentType { return ContentType(b.part.Kind) }

// MarshalJSON serializes each block with its content type discriminator.
func (r Result) MarshalJSON() ([]byte, error) {
	if err := ValidateContent(r.Content); err != nil {
		return nil, err
	}
	blocks := make([]json.RawMessage, 0, len(r.Content))
	for _, block := range r.Content {
		var wire any
		switch b := block.(type) {
		case Text:
			wire = struct {
				Type ContentType `json:"type"`
				Text Text        `json:"text"`
			}{b.ContentType(), b}
		case Image:
			wire = struct {
				Type ContentType `json:"type"`
				Image
			}{b.ContentType(), b}
		case Resource:
			wire = struct {
				Type ContentType `json:"type"`
				Resource
			}{b.ContentType(), b}
		case StructuredContent:
			wire = struct {
				Type ContentType `json:"type"`
				StructuredContent
			}{b.ContentType(), b}
		case legacyResultBlock:
			wire = b.part
		}
		raw, err := json.Marshal(wire)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, raw)
	}
	return json.Marshal(struct {
		Content []json.RawMessage `json:"content,omitempty"`
	}{blocks})
}

// UnmarshalJSON accepts typed blocks and the historical model-part encoding
// shared by Session receipts and Code Mode task snapshots.
func (r *Result) UnmarshalJSON(data []byte) error {
	var wire struct {
		Content    []json.RawMessage `json:"content"`
		Structured *struct {
			Value        json.RawMessage `json:"value"`
			ContentIndex int             `json:"content_index"`
		} `json:"structured_content"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if wire.Structured != nil {
		index := wire.Structured.ContentIndex
		if index < 0 || index >= len(wire.Content) || !json.Valid(wire.Structured.Value) {
			return fmt.Errorf("invalid historical structured content")
		}
	}
	var decoded Result
	if wire.Content != nil {
		decoded.Content = make([]ContentBlock, 0, len(wire.Content))
	}
	for i, raw := range wire.Content {
		if wire.Structured != nil && i == wire.Structured.ContentIndex {
			var part model.Part
			if err := json.Unmarshal(raw, &part); err != nil {
				return err
			}
			if part.Kind != model.PartKindJSON || part.JSON == nil || !bytes.Equal(part.JSON.Value, wire.Structured.Value) {
				return fmt.Errorf("historical structured content differs from its JSON part")
			}
			decoded.Content = append(decoded.Content, StructuredContent{Value: wire.Structured.Value})
			continue
		}
		block, err := decodeContentBlock(raw)
		if err != nil {
			return fmt.Errorf("result block %d: %w", i, err)
		}
		decoded.Content = append(decoded.Content, block)
	}
	if err := ValidateContent(decoded.Content); err != nil {
		return err
	}
	*r = decoded
	return nil
}

func decodeContentBlock(raw json.RawMessage) (ContentBlock, error) {
	var header struct {
		Type ContentType    `json:"type"`
		Kind model.PartKind `json:"kind"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil, err
	}
	switch header.Type {
	case ContentTypeText:
		var block struct {
			Text *string `json:"text"`
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, err
		}
		if block.Text == nil {
			return nil, fmt.Errorf("missing text payload")
		}
		return Text(*block.Text), nil
	case ContentTypeImage:
		var block Image
		err := json.Unmarshal(raw, &block)
		return block, err
	case ContentTypeResource:
		var block Resource
		err := json.Unmarshal(raw, &block)
		return block, err
	case ContentTypeStructured:
		var block StructuredContent
		err := json.Unmarshal(raw, &block)
		return block, err
	case "":
		if header.Kind == "" {
			return nil, fmt.Errorf("missing content type")
		}
		return decodeLegacyResultBlock(raw)
	default:
		return nil, fmt.Errorf("unsupported content type %q", header.Type)
	}
}

func decodeLegacyResultBlock(raw json.RawMessage) (ContentBlock, error) {
	var part model.Part
	if err := json.Unmarshal(raw, &part); err != nil {
		return nil, err
	}
	switch part.Kind {
	case model.PartKindText:
		if part.Text != nil && len(part.Text.Citations) == 0 {
			return Text(part.Text.Text), nil
		}
	case model.PartKindMedia:
		if p := part.Media; p != nil && p.Modality == model.MediaModalityImage && p.Source.Kind == model.MediaSourceInline && p.MimeType != "" {
			if data, err := base64.StdEncoding.DecodeString(p.Source.Data); err == nil && len(data) != 0 {
				return Image{Data: data, MIMEType: p.MimeType, Name: p.Name}, nil
			}
		}
	}
	return legacyResultBlock{part}, nil
}
