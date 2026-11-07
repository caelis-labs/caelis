package runtime

import (
	"context"
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

func (h *codeModeHost) Emit(ctx context.Context, handle string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// TurnComplete stops effects, but its own result can still emit its media.
	if h.ctx.Err() != nil {
		return h.ctx.Err()
	}
	part, ok := h.handles[handle]
	if !ok {
		return fmt.Errorf("unknown Code Mode media handle")
	}
	if len(h.emitted) >= h.limits.Calls {
		return fmt.Errorf("code mode emitted part budget exceeded")
	}
	h.emitted = append(h.emitted, part)
	delete(h.handles, handle)
	return nil
}
