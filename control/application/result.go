package application

import (
	"context"
	"encoding/json"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// projectResult preserves the application's effect receipt independently of
// media availability. Once resolved, media bytes are historical evidence in
// canonical Session content; replay never fetches resources or repeats effects.
func (s *Store) projectResult(ctx context.Context, c CallContext, name, format string, result CallResult) (tool.Result, error) {
	out := tool.Result{ID: c.CallID, Name: name, IsError: result.Outcome != "succeeded", TurnComplete: result.TurnComplete}
	provenance, err := json.Marshal(c)
	if err != nil {
		return tool.Result{}, err
	}
	var metadata map[string]any
	if err = json.Unmarshal(provenance, &metadata); err != nil {
		return tool.Result{}, err
	}
	metadata["receipt_id"] = callID(c)
	metadata["outcome"] = result.Outcome
	if result.TurnComplete {
		metadata["turn_complete"] = true
	}
	out.Metadata = map[string]any{"application_call": metadata}
	if format == "" {
		// The baseline application-runtime-v1 format is opaque JSON text.
		// Keep it until that public capability is retired; never infer images
		// from image-shaped JSON returned by an unnegotiated legacy tool.
		raw, err := json.Marshal(result)
		if err != nil {
			return tool.Result{}, err
		}
		out.Content = []model.Part{model.NewTextPart(string(raw))}
		return out, nil
	}

	var references []ContentBlock
	blocks, _ := decodeContent(result.Content)
	for _, block := range blocks {
		if block.Type == "resource_link" {
			references = append(references, block)
		}
	}
	receipt := resultReceipt(c, result, references)
	// Store-generated unknown outcomes have no application content. They must
	// remain machine-readable unknown, not failed execution or permission to retry.
	if result.ResultFormat != "" {
		s.mu.Lock()
		parts, _, contentErr := s.contentParts(ctx, c, result.Content)
		s.mu.Unlock()
		if contentErr != nil {
			// Expiry may race successful completion. Preserve the true outcome,
			// explicitly report unavailable content and never claim to see it.
			out.IsError = true
			receipt["content_error"] = map[string]any{"code": errorcode.CodeOf(contentErr), "message": contentErr.Error()}
		} else {
			out.Content = parts
		}
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return tool.Result{}, err
	}
	out.Content = append(out.Content, model.NewJSONPart(raw))
	return out, nil
}

func resultReceipt(c CallContext, result CallResult, references []ContentBlock) map[string]any {
	receipt := map[string]any{"outcome": result.Outcome, "receipt_id": callID(c)}
	if result.StructuredContent != nil {
		receipt["structuredContent"] = result.StructuredContent
	}
	if len(references) > 0 {
		receipt["resources"] = references
	}
	return receipt
}
