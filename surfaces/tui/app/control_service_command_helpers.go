package tuiapp

import (
	"strings"
	"unicode"

	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
	controlcommands "github.com/OnslaughtSnail/caelis/protocol/acp/control/commands"
)

func splitSlash(text string) (cmd, args string) {
	cmd, args, _ = splitSlashWithPromptSpan(text)
	return
}

func splitSlashWithPromptSpan(text string) (cmd, args string, argsStart int) {
	textRunes := []rune(strings.TrimSpace(text))
	idx := 0
	if idx < len(textRunes) && textRunes[idx] == '/' {
		idx++
	}
	cmdStart := idx
	for idx < len(textRunes) && !unicode.IsSpace(textRunes[idx]) {
		idx++
	}
	cmd = strings.TrimSpace(strings.ToLower(string(textRunes[cmdStart:idx])))
	for idx < len(textRunes) && unicode.IsSpace(textRunes[idx]) {
		idx++
	}
	argsStart = idx
	args = strings.TrimSpace(string(textRunes[idx:]))
	return
}

func splitFirst(text string) (first, rest string) {
	first, rest, _ = splitFirstWithPromptSpan(text)
	return
}

func splitFirstWithPromptSpan(text string) (first, rest string, restStart int) {
	first, _, _ = strings.Cut(strings.TrimSpace(text), " ")
	first = strings.TrimSpace(first)
	textRunes := []rune(strings.TrimSpace(text))
	idx := 0
	for idx < len(textRunes) && !unicode.IsSpace(textRunes[idx]) {
		idx++
	}
	for idx < len(textRunes) && unicode.IsSpace(textRunes[idx]) {
		idx++
	}
	restStart = idx
	rest = strings.TrimSpace(string(textRunes[idx:]))
	return
}

func attachmentsForPromptRange(items []Attachment, start int, end int) []Attachment {
	if len(items) == 0 {
		return nil
	}
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	out := make([]Attachment, 0, len(items))
	for _, item := range cloneAttachments(items) {
		if item.Offset < start || item.Offset > end {
			continue
		}
		out = append(out, Attachment{
			Name:   item.Name,
			Offset: item.Offset - start,
		})
	}
	return cloneAttachments(out)
}

type agentAddArgs = controlcommands.AgentAddArgs

func parseAgentAddArgs(args string) (agentAddArgs, bool) {
	return controlcommands.ParseAgentAddArgs(args)
}

func parseConnectArgs(args string) control.ConnectConfig {
	return controlcommands.ParseConnectArgs(args)
}

func agentHelpText() string {
	return controlcommands.AgentHelpText()
}

func formatAgentCatalog(agents []control.AgentCandidate) string {
	return controlcommands.FormatAgentCatalog(agents)
}

func formatAgentList(agents []control.AgentCandidate, status control.AgentStatusSnapshot) string {
	return controlcommands.FormatAgentList(agents, status)
}

func formatAgentReadyNotice(agent string) string {
	return controlcommands.FormatAgentReadyNotice(agent)
}

func formatAgentRemovedNotice(agent string) string {
	return controlcommands.FormatAgentRemovedNotice(agent)
}

func formatAgentUseNotice(target string, status control.AgentStatusSnapshot) string {
	return controlcommands.FormatAgentUseNotice(target, status)
}

func formatAgentStatusSnapshot(status control.AgentStatusSnapshot) string {
	return controlcommands.FormatAgentStatusSnapshot(status)
}

func modeToggleHint(status control.StatusSnapshot) string {
	return controlcommands.ModeToggleHint(status)
}

func friendlyCommandError(action string, err error) error {
	return controlcommands.FriendlyCommandError(action, err)
}

func convertAttachments(items []Attachment) []control.Attachment {
	if len(items) == 0 {
		return nil
	}
	out := make([]control.Attachment, len(items))
	for i, item := range items {
		out[i] = control.Attachment{
			Name:   item.Name,
			Offset: item.Offset,
		}
	}
	return out
}
