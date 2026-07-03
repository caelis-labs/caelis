// Package userdisplay resolves the model-visible user message and the
// user-visible display text for one submitted prompt.
package userdisplay

import (
	"maps"
	"strings"

	"github.com/caelis-labs/caelis/ports/model"
)

const MetaDisplayInput = "display_input"

const legacyMetaDisplayText = "display_text"

func Resolve(input string, displayInput string, parts []model.ContentPart, meta map[string]any) (model.Message, string, map[string]any) {
	message := model.MessageFromTextAndContentParts(model.RoleUser, strings.TrimSpace(input), parts)
	modelText := strings.TrimSpace(message.TextContent())
	displayText := ResolveDisplayInput(displayInput, meta)
	if displayText == "" {
		displayText = message.TextContent()
	}

	outMeta := maps.Clone(meta)
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

func ResolveDisplayInput(displayInput string, meta map[string]any) string {
	return firstNonEmpty(
		strings.TrimSpace(displayInput),
		stringMeta(meta, MetaDisplayInput),
		stringMeta(meta, legacyMetaDisplayText),
	)
}

func stringMeta(meta map[string]any, key string) string {
	if value, ok := meta[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
