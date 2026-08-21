package acpagentbridge

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

const (
	systemManagedSubagentSessionKind = "subagent"
)

// normalizedACPSessionMetadata promotes only the recognized built-in
// subagent classification emitted by the Host-owned ACP bridge into product
// Session metadata. Arbitrary ACP _meta values never become durable Session
// metadata, and this classification alone never authorizes an existing Session
// target.
func normalizedACPSessionMetadata(meta map[string]any) map[string]any {
	kind := metautil.String(
		meta,
		metautil.Root,
		metautil.Runtime,
		metautil.RuntimeSession,
		metautil.RuntimeSessionKind,
	)
	if kind != metautil.RuntimeSessionKindSubagent {
		return nil
	}
	out := map[string]any{sessionvisibility.MetadataSystemManagedAgent: systemManagedSubagentSessionKind}
	if parentSessionID := metautil.String(
		meta,
		metautil.Root,
		metautil.Runtime,
		metautil.RuntimeSession,
		metautil.RuntimeSessionParentID,
	); parentSessionID != "" {
		out[sessionvisibility.MetadataSystemManagedParent] = parentSessionID
	}
	if taskID := metautil.String(
		meta,
		metautil.Root,
		metautil.Runtime,
		metautil.RuntimeSession,
		metautil.RuntimeTaskID,
	); taskID != "" {
		out[sessionvisibility.MetadataSystemManagedTask] = taskID
	}
	return out
}

// matchesManagedSubagentRelationClaim recognizes the exact durable relation
// emitted by Host-owned child execution and history bridges. It is never an
// authorization by itself: callers additionally require either an execution
// bridge without the history token or the exact read-only history capability.
func matchesManagedSubagentRelationClaim(active session.Session, meta map[string]any) bool {
	if !sessionvisibility.IsSystemManagedSession(active) ||
		!strings.EqualFold(
			strings.TrimSpace(sessionMetadataString(active.Metadata, sessionvisibility.MetadataSystemManagedAgent)),
			systemManagedSubagentSessionKind,
		) {
		return false
	}
	path := []string{metautil.Root, metautil.Runtime, metautil.RuntimeSession}
	kind := metautil.String(meta, append(path, metautil.RuntimeSessionKind)...)
	parentSessionID := metautil.String(meta, append(path, metautil.RuntimeSessionParentID)...)
	taskID := metautil.String(meta, append(path, metautil.RuntimeTaskID)...)
	return kind == metautil.RuntimeSessionKindSubagent && parentSessionID != "" && taskID != "" &&
		parentSessionID == strings.TrimSpace(sessionMetadataString(active.Metadata, sessionvisibility.MetadataSystemManagedParent)) &&
		taskID == strings.TrimSpace(sessionMetadataString(active.Metadata, sessionvisibility.MetadataSystemManagedTask))
}

func sessionMetadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return value
}
