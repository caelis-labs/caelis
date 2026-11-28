package runtime

import (
	"encoding/json"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
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

// Failed replies are the only durable-result bodies held by the Host past an
// individual call. Keep compact disposition and receipt identity at capacity.
func (h *codeModeHost) retainFailureReply(failure *codeModeFailure, reply codemode.Reply) {
	size := 128 + len(reply.CallID) + len(reply.BodyText) + len(reply.Value)
	for _, part := range reply.Parts {
		size += 64 + len(part.Text) + len(part.JSON) + len(part.Handle) + len(part.MimeType)
	}
	if size > h.limits.RetainedBytes-h.failureBytes-h.retained {
		return
	}
	reply.Origin = ""
	h.failureBytes += size
	failure.Reply = &reply
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
func (h *codeModeHost) project(id string, result tool.Result, outcome string, def tool.Definition, events ...*session.Event) (codemode.Reply, error) {
	if h.codeModeResults == nil {
		h.codeModeResults = newCodeModeResults()
	}
	reply := codemode.Reply{CallID: id, Outcome: outcome, IsError: outcome != codemode.OutcomeSucceeded}
	succeeded := outcome == codemode.OutcomeSucceeded
	var receipt codeModeReplyReceipt
	if len(events) > 0 && events[0] != nil {
		receipt.id, receipt.seq = events[0].ID, events[0].Seq
	}
	retained, projected := 0, len(id)*6+128
	textEntries := 0
	if receipt.seq != 0 {
		if len(receipt.id) > maxCodeModeReceiptID {
			return reply, codemode.NewReplyBudgetError("code mode receipt identity exceeds transfer budget; full receipt retained")
		}
		projected += 32 + 6*(codeModeOriginBytes(receipt.id))
	}
	if projected > h.limits.ReplyBytes {
		return reply, codemode.NewReplyBudgetError("code mode reply identity exceeds transfer budget; full receipt retained")
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
			if receipt.seq == 0 && part.Attachment.Instance == "" {
				retained += len(part.Data) + len(part.MIMEType) + len(part.Name)
			}
			projected += codeModeMediaReplyBytes(receipt, len(part.MIMEType))
		case tool.Resource:
			retained += len(part.URI) + len(part.Name) + len(part.MIMEType) + len(part.Title) + len(part.Description)
			if part.Size != nil {
				retained += 8
			}
			if part.Text != nil {
				retained += len(*part.Text)
			}
			projected += codeModeMediaReplyBytes(receipt, len(part.MIMEType))
		default:
			return reply, fmt.Errorf("unsupported Code Mode result block %T", part)
		}
		if receipt.seq == 0 && retained > h.limits.RetainedBytes-h.retained-h.failureBytes {
			return reply, codemode.NewReplyBudgetError("code mode result exceeds cumulative non-image retention budget; full receipt retained")
		}
		if projected > h.limits.ReplyBytes {
			return reply, codemode.NewReplyBudgetError("code mode result exceeds reply transfer budget; full receipt retained")
		}
	}
	projected += max(0, textEntries-1) * 6
	if projected > h.limits.ReplyBytes {
		return reply, codemode.NewReplyBudgetError("code mode text exceeds reply transfer budget; full receipt retained")
	}
	if succeeded && structured != nil && def.OutputSchema != nil {
		if err := tool.ValidateOutput(def.OutputSchema, structured); err != nil {
			return reply, fmt.Errorf("structured result does not match output schema: %w", err)
		}
	}
	// The bridge owns its Reply bytes. A live VM handle keeps only the original
	// receipt position and block ordinal, including for inline legacy results.
	if receipt.seq != 0 {
		reply.Origin = h.sealCodeModeOrigin(receipt, 0)
	}
	for i, block := range result.Content {
		value := codeModePart(block)
		switch value.Kind {
		case "json":
			if succeeded {
				reply.Value = value.JSON
			}
		case "media", "file_ref":
			if receipt.seq != 0 {
				value.Handle = h.sealCodeModeOrigin(receipt, i+1)
			} else {
				value.Handle = uuid.NewString()
				h.handles[value.Handle] = block
			}
		}
		reply.Parts = append(reply.Parts, value)
	}
	if succeeded {
		reply.BodyText = reply.PlainText()
	} else {
		reply.BodyText = reply.Text()
	}
	if receipt.seq == 0 {
		receipt.inMemory = reply.Parts
		h.retained += retained
	}
	// Actual call IDs support the trusted Executor contract within this script.
	// Persistent Body objects use reply.Origin instead.
	h.replyParts[id] = receipt
	return reply, nil
}

func codeModeMediaReplyBytes(receipt codeModeReplyReceipt, mimeBytes int) int {
	handleBytes := 36
	if receipt.seq != 0 {
		handleBytes = codeModeOriginBytes(receipt.id)
	}
	return 40 + 6*(handleBytes+mimeBytes)
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
	var parts []tool.ContentBlock
	if output.CallID != "" {
		original, ordinal, ok := h.openCodeModeOrigin(output.CallID)
		if !ok {
			original, ok = h.replyParts[output.CallID]
		}
		if !ok || ordinal != 0 || !output.Reply || len(output.Value) != 0 || len(output.Parts) != 0 {
			return nil, fmt.Errorf("returned reply does not belong to this invocation")
		}
		if original.seq != 0 {
			result, err := h.readCodeModeResult(original)
			if err != nil {
				return nil, err
			}
			return h.wholeCodeModeResult(result)
		}
		output = codemode.Output{Reply: true, Parts: original.inMemory}
		if !validCodeModeOutput(output, h.limits.OutputBytes) {
			return nil, fmt.Errorf("complete original result exceeds output budget")
		}
	}
	if !output.Reply {
		parts = append(parts, tool.StructuredContent{Value: output.Value})
	}
	selected := map[string]bool{}
	receipts := map[uint64]tool.Result{}
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
			if !selected[part.Handle] {
				if len(selected) >= h.limits.Calls {
					return nil, fmt.Errorf("returned media budget exceeded")
				}
				original := h.handles[part.Handle]
				receipt, ordinal, authenticated := h.openCodeModeOrigin(part.Handle)
				if authenticated && ordinal > 0 {
					result, ok := receipts[receipt.seq]
					if !ok {
						var err error
						result, err = h.readCodeModeResult(receipt)
						if err != nil {
							return nil, err
						}
						receipts[receipt.seq] = result
					}
					if ordinal > len(result.Content) {
						return nil, fmt.Errorf("original media receipt block is unavailable")
					}
					original = result.Content[ordinal-1]
				}
				if codeModePart(original).Kind != part.Kind {
					return nil, fmt.Errorf("returned media handle does not belong to this invocation")
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
