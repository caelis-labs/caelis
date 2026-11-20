package runtime

import (
	"encoding/json"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/google/uuid"
)

// codeModeFailure retains the original Host projection, independently of any
// exception properties a script can mutate. Replies are emitted only when the
// script fails; caught failures keep their compact disposition record.
type codeModeFailure struct {
	Tool    string          `json:"tool"`
	CallID  string          `json:"call_id,omitempty"`
	Outcome string          `json:"outcome"`
	Code    string          `json:"code,omitempty"`
	Reply   *codemode.Reply `json:"reply,omitempty"`
}

func (h *codeModeHost) failurePayload(detailed bool) map[string]any {
	failures := make([]codeModeFailure, len(h.failures))
	for i, failure := range h.failures {
		failures[i] = failure
		if !detailed {
			failures[i].Reply = nil
		}
	}
	payload := map[string]any{"errors": failures}
	for _, failure := range failures {
		if failure.Outcome == codemode.OutcomeUnknown {
			payload["instruction"] = "Do not replay unknown effects; inspect the original receipt."
			break
		}
		if failure.Code == "stale_revision" {
			payload["instruction"] = "Search the stale tool again before revising arguments."
		}
	}
	return payload
}

// project runs under host.mu. Check sizes before copying JSON or serializing
// transport data; original results have already crossed the durable receipt owner.
func (h *codeModeHost) project(id string, result tool.Result, outcome string, def tool.Definition) (codemode.Reply, error) {
	if h.codeModeResults == nil {
		h.codeModeResults = newCodeModeResults()
	}
	h.codeModeResults.mu.Lock()
	defer h.codeModeResults.mu.Unlock()
	reply := codemode.Reply{CallID: id, Outcome: outcome, IsError: outcome != codemode.OutcomeSucceeded}
	succeeded := outcome == codemode.OutcomeSucceeded
	retained, projected := 0, len(id)*6+128
	textEntries := 0
	if projected > h.limits.ReplyBytes {
		return reply, fmt.Errorf("code mode reply identity exceeds transfer budget")
	}
	var structured json.RawMessage
	for _, block := range result.Content {
		switch part := block.(type) {
		case tool.Text:
			retained += len(part)
			projected += len(part)*12 + 64
			textEntries++
		case tool.StructuredContent:
			if structured != nil || !json.Valid(part.Value) {
				return reply, fmt.Errorf("invalid structured result: expected one valid JSON block")
			}
			structured = part.Value
			retained += len(part.Value)
			// JSON is projected both as a part and as Value on success, or
			// in the diagnostic text on failure.
			projected += len(part.Value)*12 + 64
			if !succeeded {
				textEntries++
			}
		case tool.Image:
			retained += len(part.Data) + len(part.MIMEType) + len(part.Name)
			projected += 256 + len(part.MIMEType)*6
		case tool.Resource:
			retained += len(part.URI) + len(part.Name) + len(part.MIMEType) + len(part.Title) + len(part.Description)
			if part.Size != nil {
				retained += 8
			}
			if part.Text != nil {
				retained += len(*part.Text)
			}
			projected += 256 + len(part.MIMEType)*6
		default:
			return reply, fmt.Errorf("unsupported Code Mode result block %T", part)
		}
		if retained > h.limits.RetainedBytes-h.retained || projected > h.limits.ReplyBytes {
			return reply, fmt.Errorf("code mode result exceeds projection/retention budget; full receipt retained")
		}
	}
	projected += max(0, textEntries-1) * 6
	if projected > h.limits.ReplyBytes {
		return reply, fmt.Errorf("code mode text exceeds projection budget")
	}
	if succeeded && structured != nil && def.OutputSchema != nil {
		if err := tool.ValidateOutput(def.OutputSchema, structured); err != nil {
			return reply, fmt.Errorf("structured result does not match output schema: %w", err)
		}
	}
	// No fallible validation remains when invocation-owned state is committed.
	for _, block := range result.Content {
		var value codemode.Part
		switch part := block.(type) {
		case tool.Text:
			value = codemode.Part{Kind: "text", Text: string(part)}
		case tool.StructuredContent:
			value = codemode.Part{Kind: "json", JSON: append(json.RawMessage(nil), part.Value...)}
			if succeeded {
				reply.Value = value.JSON
			}
		case tool.Image:
			value = codemode.Part{Kind: "media", Handle: uuid.NewString(), MimeType: part.MIMEType}
			h.handles[value.Handle] = block
		case tool.Resource:
			value = codemode.Part{Kind: "file_ref", Handle: uuid.NewString(), MimeType: part.MIMEType}
			h.handles[value.Handle] = block
		}
		reply.Parts = append(reply.Parts, value)
	}
	if succeeded {
		reply.BodyText = reply.PlainText()
	} else {
		reply.BodyText = reply.Text()
	}
	if h.replies == nil {
		h.replies = map[string]codemode.Reply{}
	}
	h.replies[id] = codemode.Reply{Parts: reply.Parts}
	h.retained += retained
	return reply, nil
}

// validCodeModeOutput bounds trusted adapter metadata separately from business
// JSON. The scalar check precedes marshaling so an adapter cannot request an
// unbounded copy at this common boundary.
func validCodeModeOutput(output codemode.Output, budget int) bool {
	size := len(output.Value) + len(output.CallID)
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
func (h *codeModeHost) returnedParts(output codemode.Output) ([]tool.ContentBlock, error) {
	h.codeModeResults.mu.Lock()
	defer h.codeModeResults.mu.Unlock()
	var parts []tool.ContentBlock
	if output.CallID != "" {
		original, ok := h.replies[output.CallID]
		if !ok || !output.Reply || len(output.Value) != 0 || len(output.Parts) != 0 {
			return nil, fmt.Errorf("returned reply does not belong to this invocation")
		}
		output = codemode.Output{Reply: true, Parts: original.Parts}
		if !validCodeModeOutput(output, h.limits.OutputBytes) {
			return nil, fmt.Errorf("complete original result exceeds output budget")
		}
		// Use the same authenticated JSON/media selection path as other returns;
		// retaining raw tokens must not bypass attachment or output limits.
	}
	if !output.Reply {
		parts = append(parts, tool.StructuredContent{Value: output.Value})
	}
	selected := map[string]bool{}
	var structured bool
	for _, part := range output.Parts {
		switch part.Kind {
		case "text":
			parts = append(parts, tool.Text(part.Text))
		case "json":
			if !output.Reply || structured || !json.Valid(part.JSON) {
				return nil, fmt.Errorf("invalid authenticated JSON part")
			}
			structured = true
			parts = append(parts, tool.StructuredContent{Value: part.JSON})
		case "media", "file_ref":
			original := h.handles[part.Handle]
			_, image := original.(tool.Image)
			_, resource := original.(tool.Resource)
			if (part.Kind == "media" && !image) || (part.Kind == "file_ref" && !resource) {
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
