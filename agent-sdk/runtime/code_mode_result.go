package runtime

import (
	"encoding/json"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/google/uuid"
)

// project runs under host.mu. Check sizes before copying JSON or serializing
// transport data; original results have already crossed the durable receipt owner.
func (h *codeModeHost) project(id string, result tool.Result, outcome string) (codemode.Reply, error) {
	reply := codemode.Reply{CallID: id, Outcome: outcome, IsError: result.IsError, TurnComplete: result.TurnComplete && !result.IsError}
	retained, projected := 0, len(id)*6+128
	if projected > h.limits.ReplyBytes {
		return reply, fmt.Errorf("code mode reply identity exceeds transfer budget")
	}
	for _, part := range result.Content {
		switch {
		case part.Kind == model.PartKindText && part.Text != nil:
			retained += len(part.Text.Text)
			projected += len(part.Text.Text)*6 + 64
		case part.Kind == model.PartKindJSON && part.JSON != nil:
			retained += len(part.JSON.Value)
			projected += len(part.JSON.Value)*6 + 64
		case (part.Kind == model.PartKindMedia && part.Media != nil) || (part.Kind == model.PartKindFileRef && part.FileRef != nil):
			retained += opaquePartBytes(part)
			projected += 256
			if part.Kind == model.PartKindMedia {
				projected += len(part.Media.MimeType) * 6
			}
			if part.Kind == model.PartKindFileRef {
				projected += len(part.FileRef.MimeType) * 6
			}
		default:
			return reply, fmt.Errorf("unsupported Code Mode result part %q", part.Kind)
		}
		if retained > h.limits.RetainedBytes-h.retained || projected > h.limits.ReplyBytes {
			return reply, fmt.Errorf("code mode result exceeds projection/retention budget; full receipt retained")
		}
	}
	for _, part := range result.Content {
		value := codemode.Part{Kind: string(part.Kind)}
		switch part.Kind {
		case model.PartKindText:
			value.Text = part.Text.Text
		case model.PartKindJSON:
			value.JSON = append(json.RawMessage(nil), part.JSON.Value...)
		default:
			value.Handle = uuid.NewString()
			if part.Kind == model.PartKindMedia {
				value.MimeType = part.Media.MimeType
				h.handles[value.Handle] = model.Part{Kind: model.PartKindMedia, Media: part.Media}
			}
			if part.Kind == model.PartKindFileRef {
				value.MimeType = part.FileRef.MimeType
				h.handles[value.Handle] = model.Part{Kind: model.PartKindFileRef, FileRef: part.FileRef}
			}
		}
		reply.Parts = append(reply.Parts, value)
	}
	h.retained += retained
	return reply, nil
}

func opaquePartBytes(part model.Part) int {
	if p := part.Media; p != nil {
		return len(p.Source.Data) + len(p.Source.URI) + len(p.Source.FileID) + len(p.Source.LocalRef) + len(p.MimeType) + len(p.Name)
	}
	if p := part.FileRef; p != nil {
		return len(p.URI) + len(p.FileID) + len(p.LocalRef) + len(p.MimeType) + len(p.Name)
	}
	return 0
}

// returnedParts runs once under host.mu after all producers have drained. Only
// media explicitly present in this invocation's final return value is selected;
// JSON payloads remain opaque data, not a source of implicit media references.
func (h *codeModeHost) returnedParts(raw json.RawMessage) ([]model.Part, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	var media []model.Part
	seen := map[string]bool{}
	var selectMedia func(any, int) error
	selectMedia = func(value any, depth int) error {
		if depth > 128 {
			return fmt.Errorf("returned value exceeds nesting budget")
		}
		switch v := value.(type) {
		case []any:
			for _, entry := range v {
				if err := selectMedia(entry, depth+1); err != nil {
					return err
				}
			}
		case map[string]any:
			kind, _ := v["kind"].(string)
			if kind == string(model.PartKindMedia) || kind == string(model.PartKindFileRef) {
				handle, _ := v["handle"].(string)
				part, ok := h.handles[handle]
				if !ok || string(part.Kind) != kind {
					return fmt.Errorf("returned media handle does not belong to this invocation")
				}
				if !seen[handle] {
					if len(media) >= h.limits.Calls {
						return fmt.Errorf("returned media budget exceeded")
					}
					seen[handle] = true
					media = append(media, part)
				}
				return nil
			}
			for name, entry := range v {
				if kind == "json" && name == "json" {
					continue
				}
				if err := selectMedia(entry, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := selectMedia(value, 0); err != nil {
		return nil, err
	}
	// Direct tool replies expose original model parts without a second JSON
	// envelope. Arbitrary computations and combinations remain ordinary JSON.
	if object, ok := value.(map[string]any); ok && len(object) == 1 && object["parts"] != nil {
		var reply struct {
			Parts []codemode.Part `json:"parts"`
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			return nil, err
		}
		var parts []model.Part
		selected := map[string]bool{}
		for _, part := range reply.Parts {
			switch part.Kind {
			case "text":
				parts = append(parts, model.NewTextPart(part.Text))
			case "json":
				if !json.Valid(part.JSON) {
					return nil, fmt.Errorf("invalid returned JSON part")
				}
				parts = append(parts, model.NewJSONPart(part.JSON))
			case string(model.PartKindMedia), string(model.PartKindFileRef):
				if !selected[part.Handle] {
					parts = append(parts, h.handles[part.Handle])
					selected[part.Handle] = true
				}
			default:
				return nil, fmt.Errorf("unsupported returned part kind %q", part.Kind)
			}
		}
		return parts, nil
	}
	return append([]model.Part{model.NewJSONPart(raw)}, media...), nil
}
