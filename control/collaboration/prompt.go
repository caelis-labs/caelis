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
// assigned handle, reserved parent address, role, and reporting behavior.
// It never carries credentials, mailbox contents, Session or Task identifiers,
// or peer-authored claims.
type PromptSlice struct {
	Handle string
	Role   string
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

// DiscoveryInstruction supplies a stable MCP search key without listing schemas.
func DiscoveryInstruction() string {
	return `If collaboration tools are not visible, search for "caelis-collaboration".`
}

// CollaboratorInstructions defines reporting and turn completion for children.
func CollaboratorInstructions() string {
	return "Use SendMessage to report meaningful progress or blockers to parent; send brief updates during longer tasks. Process any messages returned by the tool. When finished or blocked, return your result and end the turn; new messages resume this Session."
}

// RenderPromptSlice returns the one-time setup appended to a child's initial
// ACP prompt. Follow-up messages carry only their body and sender.
func RenderPromptSlice(in PromptSlice) string {
	lines := []string{IdentityInstructions(in.Handle, in.Role), DiscoveryInstruction(), CollaboratorInstructions()}
	return SliceOpenTag + "\n" + strings.Join(lines, "\n") + "\n" + SliceCloseTag
}

func sanitizePromptToken(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	value = strings.ReplaceAll(value, "<", "")
	value = strings.ReplaceAll(value, ">", "")
	return value
}
