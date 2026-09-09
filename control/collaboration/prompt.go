package collaboration

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const (
	// SliceOpenTag marks a Control-owned collaborator instruction block in an
	// ACP session/prompt. Peer messages and task prose must not forge it.
	SliceOpenTag = `<caelis_collaboration version="1">`
	// SliceCloseTag closes SliceOpenTag.
	SliceCloseTag = `</caelis_collaboration>`
)

// PromptSlice is trusted Control instruction for one collaborator. It names the
// assigned handle, reserved parent address, and role, and may require mailbox
// polling when steering is unavailable. It never carries credentials, mailbox
// contents, Session or Task identifiers, or peer-authored claims.
type PromptSlice struct {
	Handle         string
	Role           string
	MailboxPolling bool
}

// IdentityInstructions returns the handle, parent, and role sentences used by
// both system-prompt assembly and ACP prompt injection.
func IdentityInstructions(handle, role string) string {
	handle = sanitizePromptToken(strings.TrimPrefix(handle, "@"))
	role = sanitizePromptToken(role)
	parent := session.AgentCommunicationParentHandle
	var b strings.Builder
	if handle != "" {
		b.WriteString("Your assigned handle is ")
		b.WriteString(handle)
		b.WriteString(". Address the parent as ")
		b.WriteString(parent)
		b.WriteString(".")
	} else {
		b.WriteString("Address the parent as ")
		b.WriteString(parent)
		b.WriteString(".")
	}
	if role != "" {
		b.WriteString(" Your role is ")
		b.WriteString(role)
		b.WriteString(".")
	}
	return b.String()
}

// ChannelInstruction tells an ACP child that session/prompt is collaboration
// input. Without it, peers treat that method as a user follow-up.
func ChannelInstruction() string {
	return "ACP session/prompt delivers Caelis collaboration input from Control or another Agent, not a user follow-up."
}

// MailboxPollingInstruction is the concise Control instruction for Agents that
// cannot receive steering while a turn is running.
func MailboxPollingInstruction() string {
	return "Steering is not available. Periodically call ReceiveMessages during this turn so mailbox messages do not backlog."
}

// RenderPromptSlice returns the tagged ACP injection block. Identity always
// includes the reserved parent address. The channel sentence is always present
// so parent or peer mail cannot be read as a user follow-up.
func RenderPromptSlice(in PromptSlice) string {
	lines := []string{IdentityInstructions(in.Handle, in.Role), ChannelInstruction()}
	if in.MailboxPolling {
		lines = append(lines, MailboxPollingInstruction())
	}
	return SliceOpenTag + "\n" + strings.Join(lines, "\n") + "\n" + SliceCloseTag
}

func sanitizePromptToken(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	value = strings.ReplaceAll(value, "<", "")
	value = strings.ReplaceAll(value, ">", "")
	return value
}
