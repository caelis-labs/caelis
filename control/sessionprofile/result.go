package sessionprofile

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// projectResult preserves the application's effect receipt independently of
// media availability. Once resolved, media bytes are historical evidence in
// canonical Session content; replay never fetches resources or repeats effects.
func (s *Store) projectResult(ctx context.Context, c CallContext, receiptID, format string, result CallResult) (tool.Result, error) {
	var out tool.Result
	if format == "" {
		// The baseline callback format is opaque JSON text.
		// Keep it until that public capability is retired; never infer images
		// from image-shaped JSON returned by an unnegotiated legacy tool.
		raw, err := json.Marshal(result)
		if err != nil {
			return tool.Result{}, err
		}
		out.Content = []tool.ContentBlock{tool.Text(raw)}
		if result.Outcome != "succeeded" {
			return out, callbackFailure(result.Outcome, legacyCallbackMessage(result.Content))
		}
		return out, nil
	}

	var references []ContentBlock
	var message string
	blocks, _ := decodeContent(result.Content)
	for _, block := range blocks {
		if block.Type == "resource_link" {
			references = append(references, block)
		}
		if message == "" && result.Outcome != "succeeded" && block.Type == "text" && strings.TrimSpace(*block.Text) != "" {
			message = *block.Text
		}
	}
	receipt := resultReceipt(receiptID, result.Outcome, references)
	// Store-generated unknown outcomes have no application content. They must
	// remain machine-readable unknown, not failed execution or permission to retry.
	var contentErr error
	if result.ResultFormat != "" {
		s.mu.Lock()
		var parts []tool.ContentBlock
		parts, _, contentErr = s.contentBlocks(ctx, c, result.Content)
		s.mu.Unlock()
		if contentErr != nil {
			// Expiry may race successful completion. Preserve the true outcome,
			// explicitly report unavailable content and never claim to see it.
			receipt["content_error"] = map[string]any{"code": errorcode.CodeOf(contentErr), "message": contentErr.Error()}
		} else {
			out.Content = parts
		}
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return tool.Result{}, err
	}
	// Keep the receipt visible in model context and replay without making its
	// bookkeeping fields part of the application's structured output.
	out.Content = append(out.Content, tool.Text(raw))
	if result.StructuredContent != nil {
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return tool.Result{}, err
		}
		out.Content = append(out.Content, tool.StructuredContent{Value: data})
	}
	if result.Outcome != "succeeded" {
		return out, callbackFailure(result.Outcome, message)
	}
	if contentErr != nil {
		return out, tool.FailedReply(contentErr.Error())
	}
	return out, nil
}

func callbackFailure(outcome, message string) error {
	if strings.TrimSpace(message) == "" {
		message = "Application callback outcome: " + outcome
	}
	reply := tool.FailedReply(message)
	if outcome == "unknown" {
		return tool.WrapError(errorcode.UnknownOutcome, reply, message)
	}
	return reply
}

func legacyCallbackMessage(content json.RawMessage) string {
	var message string
	if json.Unmarshal(content, &message) == nil {
		return message
	}
	var diagnostic struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(content, &diagnostic) == nil {
		return diagnostic.Message
	}
	return ""
}

func resultReceipt(receiptID, outcome string, references []ContentBlock) map[string]any {
	receipt := map[string]any{"outcome": outcome, "receipt_id": receiptID}
	if len(references) > 0 {
		receipt["resources"] = references
	}
	return receipt
}
