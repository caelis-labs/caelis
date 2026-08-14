package sessionvisibility

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const (
	// MetadataSystemManagedAgent marks a product-owned Session that must not be
	// exposed as a user resume target.
	MetadataSystemManagedAgent = "system_managed_agent"
	// MetadataSystemManagedParent preserves the owning parent Session.
	MetadataSystemManagedParent = "system_managed_parent_session_id"
	// MetadataSystemManagedTask preserves the owning Task identity.
	MetadataSystemManagedTask = "system_managed_task_id"
	// SystemManagedAgentSubagent is the recognized product-owned Session class
	// created by Spawn. These Sessions remain addressable participants but must
	// not receive nested Spawn authority.
	SystemManagedAgentSubagent = "subagent"
)

// IsSystemManagedMetadata reports whether product Session metadata marks a
// Session as system-managed.
func IsSystemManagedMetadata(metadata map[string]any) bool {
	value, _ := metadata[MetadataSystemManagedAgent].(string)
	return strings.TrimSpace(value) != ""
}

// IsSystemManagedSession reports whether a durable Session is product-owned
// and therefore unavailable as a user resume target.
func IsSystemManagedSession(active session.Session) bool {
	return IsSystemManagedMetadata(active.Metadata)
}

// IsSpawnedSubagentSession reports whether a Session is the stable child
// identity created by Spawn rather than an ordinary or other system Session.
func IsSpawnedSubagentSession(active session.Session) bool {
	value, _ := active.Metadata[MetadataSystemManagedAgent].(string)
	return strings.EqualFold(strings.TrimSpace(value), SystemManagedAgentSubagent)
}

// IsSystemManagedSummary reports whether a Session directory entry is
// product-owned and therefore excluded from user-facing resume candidates.
func IsSystemManagedSummary(summary session.SessionSummary) bool {
	return IsSystemManagedMetadata(summary.Metadata)
}
