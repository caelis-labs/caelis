package subagent

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acputil"
)

const (
	userPromptOpen  = "<caelis_user_input version=\"1\">\n"
	userPromptClose = "\n</caelis_user_input>"
)

// User text is quoted inside a product-owned prompt envelope because external
// ACP peers need not retain content metadata in session/load. JSON escaping
// keeps arbitrary user text (including mail syntax and these delimiters) from
// acquiring display provenance. This does not encode an authenticated identity.
func quoteUserPrompt(prompt []json.RawMessage) []json.RawMessage {
	for i, raw := range prompt {
		var part client.TextContent
		if json.Unmarshal(raw, &part) != nil || part.Type != "text" {
			continue
		}
		quoted, _ := json.Marshal(part.Text)
		prompt[i] = acputil.BuildPromptParts(userPromptOpen+string(quoted)+userPromptClose, nil)[0]
	}
	return prompt
}

func unquoteUserPrompt(text string) (string, bool) {
	var body strings.Builder
	for text != "" {
		if !strings.HasPrefix(text, userPromptOpen) {
			return "", false
		}
		text = text[len(userPromptOpen):]
		end := strings.Index(text, userPromptClose)
		if end < 0 {
			return "", false
		}
		var part string
		if json.Unmarshal([]byte(text[:end]), &part) != nil {
			return "", false
		}
		body.WriteString(part)
		text = strings.TrimSpace(text[end+len(userPromptClose):])
	}
	return body.String(), true
}
