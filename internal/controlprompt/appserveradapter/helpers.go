package appserveradapter

import (
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	controlagents "github.com/caelis-labs/caelis/control/agents"
)

const (
	defaultCompletionLimit = 8
	maxCompletionLimit     = 1000
)

type participantAddress struct {
	ID        string
	Kind      session.ParticipantKind
	Role      session.ParticipantRole
	Label     string
	SessionID string
	Source    string
}

func resolveParticipantID(participants []participantAddress, input string) (string, error) {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return "", fmt.Errorf("app/gatewayapp/controladapter: participant id is required")
	}
	runAgent, runHandle, directRun := controlagents.ParseRunName(input)
	prefixMatches := make([]string, 0, 2)
	for _, participant := range participants {
		if participant.Kind != session.ParticipantKindACP || participant.Role != session.ParticipantRoleSidecar {
			continue
		}
		id := strings.TrimSpace(participant.ID)
		label := strings.TrimSpace(participant.Label)
		handle := strings.TrimPrefix(label, "@")
		sessionID := strings.TrimSpace(participant.SessionID)
		if id == "" {
			continue
		}
		if directRun {
			directHandle, profileRun := controlagents.DirectRunHandleFromSource(participant.Source)
			if profileRun && strings.EqualFold(string(directHandle), runAgent) &&
				strings.EqualFold(taskapi.NormalizeHandle(label), runHandle) {
				return id, nil
			}
			continue
		}
		if strings.EqualFold(id, input) || strings.EqualFold(label, input) || strings.EqualFold(handle, input) || strings.EqualFold(sessionID, input) {
			return id, nil
		}
		for _, candidate := range []string{id, label, handle, sessionID} {
			candidate = strings.ToLower(strings.TrimSpace(candidate))
			if candidate != "" && strings.HasPrefix(candidate, input) {
				prefixMatches = append(prefixMatches, id)
				break
			}
		}
	}
	matches := dedupeNonEmptyStrings(prefixMatches)
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("app/gatewayapp/controladapter: participant %q is not attached", input)
	default:
		return "", fmt.Errorf("app/gatewayapp/controladapter: participant %q is ambiguous", input)
	}
}

func dedupeNonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func normalizeCompletionLimit(limit int) int {
	if limit <= 0 {
		limit = defaultCompletionLimit
	}
	if limit > maxCompletionLimit {
		limit = maxCompletionLimit
	}
	return limit
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func noActiveTurnSubmissionError() error {
	// Preserve the existing user-visible message while keeping this client-side
	// adapter independent of the private Runtime/Kernel error implementation.
	return errors.New("gateway: no active run is available for this session")
}
