package runtime

import (
	"encoding/json"
	"strings"
)

const (
	runtimeContinuityOpenTag  = `<caelis_background version="1">`
	runtimeContinuityCloseTag = `</caelis_background>`
)

type runtimeContinuityPayload struct {
	Plan                 map[string]any `json:"plan,omitempty"`
	ActiveSubagentHandle []string       `json:"active_subagent_handle,omitempty"`
}

func marshalRuntimeContinuity(payload runtimeContinuityPayload) (string, error) {
	if len(payload.Plan) == 0 && len(payload.ActiveSubagentHandle) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return runtimeContinuityOpenTag + "\n" + string(raw) + "\n" + runtimeContinuityCloseTag, nil
}

func stripRuntimeContinuity(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if runtimeContinuityPayloadFromBlock(text) != nil {
		return ""
	}
	if index := strings.LastIndex(text, "\n"+runtimeContinuityOpenTag); index >= 0 &&
		runtimeContinuityPayloadFromBlock(strings.TrimSpace(text[index+1:])) != nil {
		return strings.TrimSpace(text[:index])
	}
	return text
}

func runtimeContinuityPayloadFromBlock(block string) *runtimeContinuityPayload {
	block = strings.TrimSpace(block)
	prefix := runtimeContinuityOpenTag + "\n"
	suffix := "\n" + runtimeContinuityCloseTag
	if !strings.HasPrefix(block, prefix) || !strings.HasSuffix(block, suffix) {
		return nil
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(block, prefix), suffix)
	var payload runtimeContinuityPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil || len(payload.Plan) == 0 && len(payload.ActiveSubagentHandle) == 0 {
		return nil
	}
	return &payload
}

func appendRuntimeContinuity(checkpoint, appendix string) string {
	checkpoint = normalizeCompactMarkdown(stripRuntimeContinuity(checkpoint))
	appendix = strings.TrimSpace(appendix)
	if appendix == "" {
		return checkpoint
	}
	return strings.TrimSpace(checkpoint + "\n\n" + appendix)
}
