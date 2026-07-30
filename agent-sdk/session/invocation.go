package session

import "strings"

// StableInvocationIdentity returns the provider and requested-model identity
// used to attribute durable invocation usage. Provider response implementation
// labels remain available in raw response metadata.
func StableInvocationIdentity(provider, modelName string) (string, string) {
	provider = strings.TrimSpace(provider)
	modelName = strings.TrimSpace(modelName)
	if strings.EqualFold(provider, "xai") && strings.EqualFold(modelName, "grok-4.5-build") {
		// Sessions written before requested-model identity became authoritative
		// persisted xAI's response label. Keep this mapping until a Session
		// schema migration rewrites those histories.
		return "xai", "grok-4.5"
	}
	return provider, modelName
}
