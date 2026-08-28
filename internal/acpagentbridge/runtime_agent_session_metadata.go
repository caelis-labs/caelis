package acpagentbridge

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acputil"
)

const (
	systemManagedSubagentSessionKind = "subagent"
)

func acpRawMeta(meta map[string]json.RawMessage) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	out := make(map[string]any, len(meta))
	for key, raw := range meta {
		var value any
		if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizedACPSessionMetadata promotes only the recognized built-in
// subagent classification emitted by the Host-owned ACP bridge into product
// Session metadata. Arbitrary ACP _meta values never become durable Session
// metadata, and this classification alone never authorizes an existing Session
// target.
func normalizedACPSessionMetadata(meta map[string]any) map[string]any {
	claim, ok := acputil.ParseSubagentSessionMeta(meta)
	if !ok {
		return nil
	}
	out := map[string]any{sessionvisibility.MetadataSystemManagedAgent: systemManagedSubagentSessionKind}
	if claim.ParentSessionID != "" {
		out[sessionvisibility.MetadataSystemManagedParent] = claim.ParentSessionID
	}
	if claim.TaskID != "" {
		out[sessionvisibility.MetadataSystemManagedTask] = claim.TaskID
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
	claim, ok := acputil.ParseSubagentSessionMeta(meta)
	return ok && claim.ParentSessionID != "" && claim.TaskID != "" &&
		claim.ParentSessionID == strings.TrimSpace(sessionMetadataString(active.Metadata, sessionvisibility.MetadataSystemManagedParent)) &&
		claim.TaskID == strings.TrimSpace(sessionMetadataString(active.Metadata, sessionvisibility.MetadataSystemManagedTask))
}

func sessionMetadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return value
}
