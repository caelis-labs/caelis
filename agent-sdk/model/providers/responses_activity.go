package providers

import (
	"encoding/json"
	"strings"
)

type responsesActivityEnvelope struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
}

// responsesSSEHasSemanticActivity rejects lifecycle and heartbeat frames that
// carry no model progress. Unknown non-heartbeat event types remain activity so
// newly introduced provider tool progress cannot be timed out accidentally.
func responsesSSEHasSemanticActivity(data []byte) bool {
	var event responsesActivityEnvelope
	if err := json.Unmarshal(data, &event); err != nil {
		return false
	}
	eventType := strings.ToLower(strings.TrimSpace(event.Type))
	switch eventType {
	case "", "heartbeat", "ping", "response.created", "response.metadata":
		return false
	}
	if strings.HasSuffix(eventType, ".delta") && event.Delta == "" {
		return false
	}
	return true
}
