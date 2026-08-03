package acpagentbridge

import (
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

const (
	systemManagedSubagentSessionKind = "subagent"
)

// normalizedACPSessionMetadata promotes only the recognized built-in
// subagent classification from wire metadata into product Session metadata.
// Arbitrary ACP _meta values never become durable Session metadata.
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
