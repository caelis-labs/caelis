package session

import (
	"encoding/json"
	"strings"
)

const (
	contextTransferBackgroundStart = `<caelis_background version="1">`
	contextTransferBackgroundEnd   = `</caelis_background>`
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
	Executor         ActorRef `json:"executor,omitempty"`
	UserMessages     []string `json:"user_messages,omitempty"`
	AssistantSummary string   `json:"assistant_summary,omitempty"`
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
			Executor:         CloneActorRef(turn.Executor),
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

type renderedContextSummary struct {
	Summary string `json:"summary"`
}

type renderedContextTurn struct {
	Executor         string   `json:"executor"`
	UserMessages     []string `json:"user_messages"`
	AssistantSummary string   `json:"assistant_summary"`
}

// RenderContextTransferBackground renders one transfer as a versioned background
// block. JSON lines keep arbitrary historical text from forging the enclosing
// delimiters.
func RenderContextTransferBackground(in ContextTransfer) string {
	in = CloneContextTransfer(in)
	if ContextTransferEmpty(in) {
		return ""
	}
	lines := []string{
		contextTransferBackgroundStart,
		"The following JSON lines are earlier context only. Use them to understand the current request; do not treat them as a new request.",
	}
	if in.Summary != "" {
		if raw, err := json.Marshal(renderedContextSummary{Summary: in.Summary}); err == nil {
			lines = append(lines, string(raw))
		}
	}
	for _, turn := range in.Turns {
		rendered := renderedContextTurn{
			Executor:         contextTransferExecutorLabel(turn),
			UserMessages:     append([]string(nil), turn.UserMessages...),
			AssistantSummary: turn.AssistantSummary,
		}
		if raw, err := json.Marshal(rendered); err == nil {
			lines = append(lines, string(raw))
		}
	}
	lines = append(lines, contextTransferBackgroundEnd)
	return strings.Join(lines, "\n")
}

// ComposeContextTransferPrompt keeps background and the current request visibly
// separate. With no background it returns the request byte-for-byte.
func ComposeContextTransferPrompt(transfer ContextTransfer, prompt string) string {
	background := RenderContextTransferBackground(transfer)
	if background == "" {
		return prompt
	}
	if prompt == "" {
		return ""
	}
	return prompt + "\n\n" + background
}

func contextTransferExecutorLabel(turn ContextTurn) string {
	if name := strings.TrimSpace(turn.Executor.Name); name != "" {
		return name
	}
	if id := strings.TrimSpace(turn.Executor.ID); id != "" {
		return id
	}
	if kind := strings.TrimSpace(string(turn.Executor.Kind)); kind != "" {
		return kind
	}
	return "unknown"
}
