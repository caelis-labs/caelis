package collaboration

import (
	"slices"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
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
// Tools contains the definitions installed by Control for this recipient.
type PromptSlice struct {
	Handle string
	Role   string
	Tools  []tool.Definition
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

// ControllerInstructions describes how to use the installed collaboration tools.
func ControllerInstructions(tools []tool.Definition) string {
	var lines []string
	if hasTool(tools, "StartThread") {
		lines = append(lines, "Create a collaborator with StartThread for independent work that benefits from parallelism or focused expertise.")
	}
	if hasTool(tools, "ReadThread") {
		lines = append(lines, "After a completion notice, use ReadThread with the participant handle to read its public result.")
	}
	if hasTool(tools, "WaitThread") {
		lines = append(lines, "Use WaitThread to wait for participant results or incoming messages.")
	}
	if hasTool(tools, "SendMessage") {
		lines = append(lines, "Use SendMessage with a participant handle to continue the conversation.")
	}
	if len(lines) > 0 {
		lines = append(lines, "Own integration, validation, and the user-facing result. Verify findings that affect the next action.")
	}
	return strings.Join(lines, "\n")
}

// CollaboratorInstructions describes reporting with the installed tools and how
// a child should finish its current turn.
func CollaboratorInstructions(tools []tool.Definition) string {
	var lines []string
	if hasTool(tools, "SendMessage") {
		lines = append(lines, "Use SendMessage to report meaningful progress or blockers to parent. Send brief updates during longer tasks. Process any messages returned by the tool.")
	}
	if hasTool(tools, "ReadMessages") {
		lines = append(lines, "Use ReadMessages for an unread page of the shared collaboration conversation.")
	}
	return strings.Join(append(lines, "When finished or blocked, return your result and end the turn. New input resumes this Session."), "\n")
}

func hasTool(tools []tool.Definition, name string) bool {
	return slices.ContainsFunc(tools, func(def tool.Definition) bool { return def.Name == name })
}

// RenderPromptSlice returns the one-time setup appended to a child's initial
// ACP prompt. Follow-up messages carry only their body and sender.
func RenderPromptSlice(in PromptSlice) string {
	lines := []string{IdentityInstructions(in.Handle, in.Role)}
	if len(in.Tools) > 0 {
		lines = append(lines, DiscoveryInstruction())
	}
	lines = append(lines, CollaboratorInstructions(in.Tools))
	return SliceOpenTag + "\n" + strings.Join(lines, "\n") + "\n" + SliceCloseTag
}

func sanitizePromptToken(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	value = strings.ReplaceAll(value, "<", "")
	value = strings.ReplaceAll(value, ">", "")
	return value
}
