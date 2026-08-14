package kernel

import (
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// UsageSnapshotFromSessionEvent projects provider token usage from a durable
// session event into the canonical gateway usage contract.
func UsageSnapshotFromSessionEvent(event *session.Event) *UsageSnapshot {
	return session.UsageSnapshotFromSessionEvent(event)
}

// UsageSnapshotFromMap projects one provider-style usage payload into the
// canonical gateway usage contract.
func UsageSnapshotFromMap(payload map[string]any) *UsageSnapshot {
	return session.UsageSnapshotFromMap(payload)
}

// UsageSnapshotFromMapForProvider projects usage while applying
// provider-specific parsing rules that are unavailable from the payload alone.
func UsageSnapshotFromMapForProvider(payload map[string]any, provider string) *UsageSnapshot {
	return session.UsageSnapshotFromMapForProvider(payload, provider)
}

// UsageProviderFromSessionEvent extracts the provider used for usage accounting
// from invocation metadata, sdk metadata, or provider-style usage payloads.
func UsageProviderFromSessionEvent(event *session.Event) string {
	return session.UsageProviderFromSessionEvent(event)
}

// ProviderSeparatesCachedInput reports providers whose prompt/input token count
// excludes cache-read tokens.
func ProviderSeparatesCachedInput(provider string) bool {
	return session.ProviderSeparatesCachedInput(provider)
}

// ProviderSeparatesCachedInputForUsage reports whether this specific usage
// payload follows Anthropic-style cache accounting where cache reads are
// separate from prompt/input tokens.
func ProviderSeparatesCachedInputForUsage(provider string, payload map[string]any) bool {
	return session.ProviderSeparatesCachedInputForUsage(provider, payload)
}

// NormalizeUsageForDisplay converts provider raw usage into the status-table
// display contract. Reasoning is treated as an output-token breakdown, so it is
// not added to Total unless CompletionTokens is absent.
func NormalizeUsageForDisplay(usage UsageSnapshot, provider string) UsageSnapshot {
	return session.NormalizeUsageForDisplay(usage, provider)
}
