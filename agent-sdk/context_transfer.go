package agentsdk

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// ContextTransfer is one recipient-specific offset of public Session context.
// Summary is an opaque compaction baseline. Turns retain only public user input
// and the final assistant summary; endpoint-private tools and reasoning are not
// part of this contract.
type ContextTransfer struct {
	Summary string        `json:"summary,omitempty"`
	Turns   []ContextTurn `json:"turns,omitempty"`
}

// ContextTurn is one completed public exchange. UserMessages preserves every
// user message received before the final assistant summary, including steering
// submitted while the Agent was already running. Executor names the Agent that
// handled the exchange so another Agent does not mistake the history for its
// own work.
type ContextTurn struct {
	Executor         session.ActorRef `json:"executor,omitempty"`
	UserMessages     []string         `json:"user_messages,omitempty"`
	AssistantSummary string           `json:"assistant_summary,omitempty"`
}

// CloneContextTransfer returns one normalized deep copy.
func CloneContextTransfer(in ContextTransfer) ContextTransfer {
	out := ContextTransfer{Summary: strings.TrimSpace(in.Summary)}
	if len(in.Turns) == 0 {
		return out
	}
	out.Turns = make([]ContextTurn, 0, len(in.Turns))
	for _, turn := range in.Turns {
		userMessages := make([]string, 0, len(turn.UserMessages))
		for _, message := range turn.UserMessages {
			if message = strings.TrimSpace(message); message != "" {
				userMessages = append(userMessages, message)
			}
		}
		assistantSummary := strings.TrimSpace(turn.AssistantSummary)
		if len(userMessages) == 0 || assistantSummary == "" {
			continue
		}
		out.Turns = append(out.Turns, ContextTurn{
			Executor:         session.CloneActorRef(turn.Executor),
			UserMessages:     userMessages,
			AssistantSummary: assistantSummary,
		})
	}
	if len(out.Turns) == 0 {
		out.Turns = nil
	}
	return out
}

// ContextTransferEmpty reports whether a transfer has no model-visible
// background. Empty transfers must not add prompt scaffolding.
func ContextTransferEmpty(in ContextTransfer) bool {
	in = CloneContextTransfer(in)
	return in.Summary == "" && len(in.Turns) == 0
}

// MergeContextTransfers appends a later recipient offset to an earlier one. A
// later opaque summary supersedes the earlier offset because it is the newer
// compaction baseline.
func MergeContextTransfers(earlier, later ContextTransfer) ContextTransfer {
	earlier = CloneContextTransfer(earlier)
	later = CloneContextTransfer(later)
	if ContextTransferEmpty(earlier) {
		return later
	}
	if ContextTransferEmpty(later) {
		return earlier
	}
	if later.Summary != "" {
		return later
	}
	earlier.Turns = append(earlier.Turns, later.Turns...)
	return CloneContextTransfer(earlier)
}
