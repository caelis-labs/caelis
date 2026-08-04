package subagent

import (
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/client"
)

const (
	maxSubagentPreviewRunes = 160
	maxIntentBufferRunes    = maxSubagentPreviewRunes * 4
)

// subagentActionSummary is the bounded, transient projection used by Task
// read/wait. It remembers only the latest intent and action, never the child
// transcript or raw tool payloads.
type subagentActionSummary struct {
	intent          string
	action          string
	intentBuffer    string
	intentMessageID string
	intentOpen      bool
	observed        bool
}

func (s *subagentActionSummary) reset() {
	if s == nil {
		return
	}
	*s = subagentActionSummary{}
}

func (s *subagentActionSummary) observeAssistant(text string) {
	if s == nil {
		return
	}
	s.intent = compactPreview(text)
	s.action = ""
	s.observed = s.intent != ""
	s.intentBuffer = ""
	s.intentMessageID = ""
	s.intentOpen = false
}

func (s *subagentActionSummary) observeThought(messageID string, delta string) {
	if s == nil {
		return
	}
	messageID = strings.TrimSpace(messageID)
	if !s.intentOpen || (messageID != "" && s.intentMessageID != "" && messageID != s.intentMessageID) {
		s.intentBuffer = ""
	}
	s.intentOpen = true
	if messageID != "" {
		s.intentMessageID = messageID
	}
	s.intentBuffer = appendBoundedIntent(s.intentBuffer, delta)
	if intent := compactPreview(s.intentBuffer); intent != "" {
		s.intent = intent
		s.observed = true
	}
}

func (s *subagentActionSummary) observeAction(action string) {
	if s == nil {
		return
	}
	s.intentOpen = false
	action = compactPreview(action)
	if action == "" {
		return
	}
	s.observed = true
	if strings.EqualFold(action, "working") {
		return
	}
	// An informative action starts a new phase. Retaining an older intent here
	// would make a finished "preparing summary" thought look current while the
	// child has already resumed investigation.
	s.intent = ""
	s.intentBuffer = ""
	s.intentMessageID = ""
	s.action = action
}

func (s subagentActionSummary) previewOrEmpty() string {
	intent := compactPreview(s.intent)
	action := compactPreview(s.action)
	switch {
	case intent != "" && action != "" && !strings.EqualFold(intent, action):
		return compactPreview(intent + " · " + action)
	case intent != "":
		return intent
	case action != "":
		return action
	case s.observed:
		return "working"
	default:
		return ""
	}
}

func appendBoundedIntent(current string, delta string) string {
	if delta == "" {
		return current
	}
	combined := []rune(current + delta)
	if len(combined) <= maxIntentBufferRunes {
		return string(combined)
	}
	return string(combined[len(combined)-maxIntentBufferRunes:])
}

func compactPreview(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	lines := strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' })
	if len(lines) == 0 {
		return ""
	}
	last := strings.Join(strings.Fields(lines[len(lines)-1]), " ")
	if last == "" {
		return ""
	}
	runes := []rune(last)
	if len(runes) <= maxSubagentPreviewRunes {
		return last
	}
	const headRunes = 80
	const tailRunes = 48
	return strings.TrimSpace(string(runes[:headRunes])) + " ...[truncated]... " + strings.TrimSpace(string(runes[len(runes)-tailRunes:]))
}

func planActivity(entries []client.PlanEntry) string {
	for _, entry := range entries {
		if strings.EqualFold(strings.TrimSpace(entry.Status), "in_progress") {
			if content := compactPreview(entry.Content); content != "" {
				return "plan: " + content
			}
		}
	}
	for _, entry := range entries {
		if strings.EqualFold(strings.TrimSpace(entry.Status), "pending") {
			if content := compactPreview(entry.Content); content != "" {
				return "next: " + content
			}
		}
	}
	for index := len(entries) - 1; index >= 0; index-- {
		if content := compactPreview(entries[index].Content); content != "" {
			return "plan: " + content
		}
	}
	return "updating plan"
}
