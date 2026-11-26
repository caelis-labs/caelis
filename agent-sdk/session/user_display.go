package session

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

const (
	MetaDisplayInput   = "display_input"
	MetaDisplayAddress = "display_address"
)

const legacyMetaDisplayText = "display_text"

// ResolveUserDisplay resolves the canonical model-visible user message and the
// user-visible display text for one submitted prompt.
func ResolveUserDisplay(input string, displayInput string, parts []model.ContentPart, meta map[string]any) (model.Message, string, map[string]any) {
	message := model.MessageFromTextAndContentParts(model.RoleUser, strings.TrimSpace(input), parts)
	modelText := strings.TrimSpace(message.TextContent())
	displayText := ResolveDisplayInput(displayInput, meta)
	if displayText == "" {
		displayText = message.TextContent()
	}

	outMeta := jsonvalue.CloneMap(meta)
	delete(outMeta, MetaDisplayInput)
	delete(outMeta, legacyMetaDisplayText)
	if strings.TrimSpace(displayText) != "" && strings.TrimSpace(displayText) != modelText {
		if outMeta == nil {
			outMeta = map[string]any{}
		}
		outMeta[MetaDisplayInput] = strings.TrimSpace(displayText)
	}
	if len(outMeta) == 0 {
		outMeta = nil
	}
	return message, displayText, outMeta
}

// ResolveDisplayInput returns the explicit user-facing display text if set on
// the prompt or legacy metadata fields.
func ResolveDisplayInput(displayInput string, meta map[string]any) string {
	return firstNonEmptyString(
		strings.TrimSpace(displayInput),
		displayStringMeta(meta, MetaDisplayInput),
		displayStringMeta(meta, legacyMetaDisplayText),
	)
}

func displayStringMeta(meta map[string]any, key string) string {
	if value, ok := meta[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}
