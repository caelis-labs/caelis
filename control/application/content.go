package application

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg" // Register the content-v1 image decoders.
	_ "image/png"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// ResultFormatContentV1 selects the bounded MCP-style text, image and owned
// resource_link union. It is independent of the callback's effect Outcome.
const ResultFormatContentV1 = "content-v1"

// Content-v1 limits bound both encoded callbacks and decoded media. Larger
// images use immutable application resources, never arbitrary paths or URLs.
const (
	MaxContentBlocks = 64
	// MaxResultTextBytes keeps accepted text and the serialized effect receipt
	// below Runtime's canonical tool truncation budget, without truncating data.
	MaxResultTextBytes  = 32 << 10
	MaxInlineImageBytes = 256 << 10
	MaxImagePixels      = 16_000_000
	MaxResultImageBytes = 16 << 20
)

// ResourceURIPrefix identifies a resource in the authenticated call's exact
// application, connection and Session scope. A URI alone grants no access.
const ResourceURIPrefix = "application-resource:"

// ContentBlock is the content-v1 tagged union. Text requires Text (including an
// empty string), image requires Data and MIMEType, and resource_link requires
// URI, Name, MIMEType and SHA256. Fields from other variants are rejected.
// Only PNG and JPEG are admitted; resource links must name expiring resources.
type ContentBlock struct {
	Type     string  `json:"type"`
	Text     *string `json:"text,omitempty"`
	Data     string  `json:"data,omitempty"`
	MIMEType string  `json:"mimeType,omitempty"`
	URI      string  `json:"uri,omitempty"`
	Name     string  `json:"name,omitempty"`
	SHA256   string  `json:"sha256,omitempty"`
}

// UnmarshalJSON rejects null format/structured data rather than allowing an
// explicit null to downgrade the negotiated content contract.
func (r *CallResult) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return ErrInvalid
	}
	for _, key := range []string{"result_format", "structuredContent"} {
		if bytes.Equal(bytes.TrimSpace(fields[key]), []byte("null")) {
			return ErrInvalid
		}
	}
	type plain CallResult
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return ErrInvalid
	}
	*r = CallResult(value)
	return nil
}

func decodeContent(raw json.RawMessage) ([]ContentBlock, error) {
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil || blocks == nil || len(blocks) > MaxContentBlocks {
		return nil, fmt.Errorf("%w: content-v1 requires an array of at most %d blocks", ErrInvalid, MaxContentBlocks)
	}
	out := make([]ContentBlock, 0, len(blocks))
	for _, rawBlock := range blocks {
		var block ContentBlock
		decoder := json.NewDecoder(bytes.NewReader(rawBlock))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&block); err != nil {
			return nil, fmt.Errorf("%w: invalid content block", ErrInvalid)
		}
		// Check field presence as well as values: null and empty fields from a
		// different union arm must not become an alternate interpretation.
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawBlock, &fields); err != nil {
			return nil, ErrInvalid
		}
		allowed := "type"
		switch block.Type {
		case "text":
			allowed += ",text"
			if block.Text == nil {
				return nil, fmt.Errorf("%w: text block requires text", ErrInvalid)
			}
		case "image":
			allowed += ",data,mimeType"
			if block.Data == "" || !imageMIME(block.MIMEType) {
				return nil, fmt.Errorf("%w: image requires data and PNG or JPEG MIME", ErrInvalid)
			}
		case "resource_link":
			allowed += ",uri,name,mimeType,sha256"
			id, ok := strings.CutPrefix(block.URI, ResourceURIPrefix)
			if !ok || !strings.HasPrefix(id, "app-resource-") || !validSHA256(strings.TrimPrefix(id, "app-resource-")) || !validID(block.Name) || !imageMIME(block.MIMEType) || !validSHA256(block.SHA256) {
				return nil, fmt.Errorf("%w: resource_link requires an application resource URI, name, image MIME and SHA256", ErrInvalid)
			}
		default:
			return nil, fmt.Errorf("%w: unsupported content block type", ErrUnsupported)
		}
		for key, value := range fields {
			if !strings.Contains(","+allowed+",", ","+key+",") || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, fmt.Errorf("%w: unexpected or null content block field", ErrInvalid)
			}
		}
		out = append(out, block)
	}
	return out, nil
}

func imageMIME(mime string) bool { return mime == "image/png" || mime == "image/jpeg" }

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

// validateImage checks allocation bounds before full decode. Header-only
// validation would accept truncated or otherwise undecodable screenshots.
func validateImage(data []byte, mime string) error {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || "image/"+format != mime || config.Width <= 0 || config.Height <= 0 || int64(config.Width) > MaxImagePixels/int64(config.Height) {
		return fmt.Errorf("%w: invalid image MIME, dimensions or pixel limit", ErrInvalid)
	}
	decoded, actual, err := image.Decode(bytes.NewReader(data))
	if err != nil || actual != format || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return fmt.Errorf("%w: image cannot be decoded", ErrInvalid)
	}
	return nil
}

// contentParts resolves under the Store lock. It performs no filesystem or
// network I/O; the result is a historical byte snapshot, not a live image URL.
func (s *Store) contentParts(ctx context.Context, c CallContext, raw json.RawMessage) ([]model.Part, []ContentBlock, error) {
	blocks, err := decodeContent(raw)
	if err != nil {
		return nil, nil, err
	}
	parts := make([]model.Part, 0, len(blocks)+1)
	var references []ContentBlock
	total := 0
	for _, block := range blocks {
		if block.Type == "text" {
			parts = append(parts, model.NewTextPart(*block.Text))
			continue
		}
		var data []byte
		if block.Type == "image" {
			if len(block.Data) > base64.StdEncoding.EncodedLen(MaxInlineImageBytes) {
				return nil, nil, fmt.Errorf("%w: inline image exceeds byte limit; upload a resource", ErrInvalid)
			}
			data, err = base64.StdEncoding.Strict().DecodeString(block.Data)
			if err != nil || len(data) > MaxInlineImageBytes || base64.StdEncoding.EncodeToString(data) != block.Data {
				return nil, nil, fmt.Errorf("%w: invalid inline image base64 or byte limit", ErrInvalid)
			}
		} else {
			var resource Resource
			resource, data, err = s.resource(ctx, c.Scope, c.SessionID, strings.TrimPrefix(block.URI, ResourceURIPrefix))
			if err != nil {
				return nil, nil, err
			}
			if resource.ExpiresAt == nil || resource.MediaType != block.MIMEType || resource.SHA256 != block.SHA256 || resource.Name != block.Name {
				return nil, nil, fmt.Errorf("%w: media resource requires matching MIME, name, digest and expiration", ErrInvalid)
			}
			references = append(references, block)
		}
		total += len(data)
		if total > MaxResultImageBytes {
			return nil, nil, fmt.Errorf("%w: result image byte limit exceeded", ErrInvalid)
		}
		if err := validateImage(data, block.MIMEType); err != nil {
			return nil, nil, err
		}
		parts = append(parts, model.Part{Kind: model.PartKindMedia, Media: &model.MediaPart{
			Modality: model.MediaModalityImage, MimeType: block.MIMEType, Name: block.Name,
			Source: model.MediaSource{Kind: model.MediaSourceInline, Data: base64.StdEncoding.EncodeToString(data)},
		}})
	}
	return parts, references, nil
}

func (s *Store) validateCallResult(ctx context.Context, call Call, result CallResult) error {
	revision := call.ConfigurationRevision
	if revision == 0 {
		revision = 1
	}
	configuration, err := s.configuration(ctx, call.SessionID, revision)
	if err != nil {
		return err
	}
	var definition *ToolDefinition
	for i := range configuration.Profile.Tools {
		if configuration.Profile.Tools[i].Name == call.Name {
			definition = &configuration.Profile.Tools[i]
			break
		}
	}
	if definition == nil || result.ResultFormat != definition.ResultFormat {
		return fmt.Errorf("%w: result_format does not match the claimed tool catalog", ErrInvalid)
	}
	if result.ResultFormat == "" {
		if result.StructuredContent != nil {
			return fmt.Errorf("%w: structuredContent requires content-v1", ErrInvalid)
		}
		return nil
	}
	if result.ResultFormat != ResultFormatContentV1 {
		return ErrUnsupported
	}
	parts, references, err := s.contentParts(ctx, call.CallContext, result.Content)
	if err != nil {
		return err
	}
	receipt, err := json.Marshal(resultReceipt(call.CallContext, result, references))
	if err != nil {
		return ErrInvalid
	}
	textBytes := len(receipt)
	for _, part := range parts {
		if part.Text != nil {
			textBytes += len(part.Text.Text)
		}
	}
	if textBytes > MaxResultTextBytes {
		return fmt.Errorf("%w: text and structured receipt exceed %d bytes", ErrInvalid, MaxResultTextBytes)
	}
	if definition.OutputSchema != nil && result.Outcome == "succeeded" {
		schema, err := resolveSchema(definition.OutputSchema)
		if err != nil {
			return ErrInvalid
		}
		value, err := schemaValue(result.StructuredContent)
		if err != nil || result.StructuredContent == nil || schema.Validate(value) != nil {
			// Validation errors may include screen text or structured values.
			return fmt.Errorf("%w: structuredContent does not match output_schema", ErrInvalid)
		}
	}
	return nil
}
