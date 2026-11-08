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

// validCodeModeOutput bounds trusted adapter metadata separately from business
// JSON. The scalar check precedes marshaling so an adapter cannot request an
// unbounded copy at this common boundary.
func validCodeModeOutput(output codemode.Output, budget int) bool {
	size := len(output.Value)
	for _, part := range output.Parts {
		size += len(part.Kind) + len(part.Text) + len(part.JSON) + len(part.Handle) + len(part.MimeType) + 16
		if size > budget {
			return false
		}
	}
	if size > budget || output.Reply && len(output.Value) != 0 || !output.Reply && !json.Valid(output.Value) {
		return false
	}
	raw, err := json.Marshal(output)
	return err == nil && len(raw) <= budget
}

// returnedParts consumes only the adapter's separate origin metadata. Ordinary
// JSON is never inspected for kind/parts/handle fields. Handle ownership remains
// authoritative here, independently of the worker's object-identity tracking.
func (h *codeModeHost) returnedParts(output codemode.Output) ([]model.Part, error) {
	var parts []model.Part
	if !output.Reply {
		parts = append(parts, model.NewJSONPart(output.Value))
	}
	selected := map[string]bool{}
	for _, part := range output.Parts {
		switch part.Kind {
		case "text":
			if !output.Reply {
				return nil, fmt.Errorf("text selection requires an authenticated reply")
			}
			parts = append(parts, model.NewTextPart(part.Text))
		case "json":
			if !output.Reply || !json.Valid(part.JSON) {
				return nil, fmt.Errorf("invalid authenticated JSON part")
			}
			parts = append(parts, model.NewJSONPart(part.JSON))
		case string(model.PartKindMedia), string(model.PartKindFileRef):
			original, ok := h.handles[part.Handle]
			if !ok || string(original.Kind) != part.Kind {
				return nil, fmt.Errorf("returned media handle does not belong to this invocation")
			}
			if !selected[part.Handle] {
				if len(selected) >= h.limits.Calls {
					return nil, fmt.Errorf("returned media budget exceeded")
				}
				selected[part.Handle] = true
				parts = append(parts, original)
			}
		default:
			return nil, fmt.Errorf("unsupported authenticated part kind %q", part.Kind)
		}
	}
	return parts, nil
}
