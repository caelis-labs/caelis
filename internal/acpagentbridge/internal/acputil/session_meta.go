package acputil

import "strings"

const (
	metaRootKey                    = "caelis"
	metaVersionKey                 = "version"
	metaRuntimeKey                 = "runtime"
	metaRuntimeSessionKey          = "session"
	metaRuntimeSessionKindKey      = "kind"
	metaRuntimeSessionParentIDKey  = "parent_session_id"
	metaRuntimeSessionTaskIDKey    = "task_id"
	metaRuntimeSessionHistoryToken = "history_token"
	metaRuntimeSessionSubagentKind = "subagent"
)

// SubagentSessionMetadata is the Host-private relation claim carried by the
// built-in ACP child and managed-history bridges.
type SubagentSessionMetadata struct {
	ParentSessionID string
	TaskID          string
	HistoryToken    string
}

// NewSubagentSessionMeta encodes the Host-private managed-child relation for
// one built-in ACP session/new or session/load request.
func NewSubagentSessionMeta(parentSessionID, taskID, historyToken string) map[string]any {
	sessionMeta := map[string]any{
		metaRuntimeSessionKindKey: metaRuntimeSessionSubagentKind,
	}
	if parentSessionID = strings.TrimSpace(parentSessionID); parentSessionID != "" {
		sessionMeta[metaRuntimeSessionParentIDKey] = parentSessionID
	}
	if taskID = strings.TrimSpace(taskID); taskID != "" {
		sessionMeta[metaRuntimeSessionTaskIDKey] = taskID
	}
	if historyToken = strings.TrimSpace(historyToken); historyToken != "" {
		sessionMeta[metaRuntimeSessionHistoryToken] = historyToken
	}
	return map[string]any{
		metaRootKey: map[string]any{
			metaVersionKey: 1,
			metaRuntimeKey: map[string]any{
				metaRuntimeSessionKey: sessionMeta,
			},
		},
	}
}

// ParseSubagentSessionMeta reads only the exact Host-private managed-child
// classification. The returned relation may still be incomplete; callers that
// authorize execution or history must separately require its exact fields.
func ParseSubagentSessionMeta(meta map[string]any) (SubagentSessionMetadata, bool) {
	sessionMeta := nestedMap(meta, metaRootKey, metaRuntimeKey, metaRuntimeSessionKey)
	if strings.TrimSpace(stringValue(sessionMeta[metaRuntimeSessionKindKey])) != metaRuntimeSessionSubagentKind {
		return SubagentSessionMetadata{}, false
	}
	return SubagentSessionMetadata{
		ParentSessionID: strings.TrimSpace(stringValue(sessionMeta[metaRuntimeSessionParentIDKey])),
		TaskID:          strings.TrimSpace(stringValue(sessionMeta[metaRuntimeSessionTaskIDKey])),
		HistoryToken:    strings.TrimSpace(stringValue(sessionMeta[metaRuntimeSessionHistoryToken])),
	}, true
}

func nestedMap(values map[string]any, path ...string) map[string]any {
	current := values
	for _, key := range path {
		next, _ := current[key].(map[string]any)
		if next == nil {
			return nil
		}
		current = next
	}
	return current
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
