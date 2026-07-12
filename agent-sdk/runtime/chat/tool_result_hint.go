package chat

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const toolResultArtifactHintPrefix = "Full tool result saved to "

// toolResultWithArtifactHint handles the single text-or-JSON payload accepted
// by the artifact store. Truncation owns the system_hint budget and protection.
func toolResultWithArtifactHint(result tool.Result, path string) (tool.Result, bool) {
	path = strings.TrimSpace(path)
	if path == "" || len(result.Content) != 1 {
		return tool.Result{}, false
	}
	out, _ := tool.CloneResult(result, nil)
	// CloneResult copies the part slice; hint injection also needs isolated part
	// payload pointers so the pre-truncation result remains the artifact source.
	out.Content = model.CloneParts(result.Content)
	part := &out.Content[0]
	hint := toolResultArtifactHintPrefix + path
	switch {
	case part.Text != nil:
		if part.Text.Text != "" {
			part.Text.Text += "\n\n"
		}
		part.Text.Text += "System hint: " + hint
		return out, true
	case part.JSON != nil:
		var payload any
		if json.Unmarshal(part.JSON.Value, &payload) != nil {
			return tool.Result{}, false
		}
		if object, ok := payload.(map[string]any); ok {
			if existing, _ := object["system_hint"].(string); strings.TrimSpace(existing) != "" {
				hint = existing + "\n" + hint
			}
			object["system_hint"] = hint
			part.JSON.Value = mustJSON(object)
			return out, true
		}
		part.JSON.Value = mustJSON(map[string]any{
			"result":      payload,
			"system_hint": hint,
		})
		return out, true
	default:
		return tool.Result{}, false
	}
}
