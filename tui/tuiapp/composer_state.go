package tuiapp

import (
	"charm.land/bubbles/v2/textarea"
)

// Composer groups all input-related state: textarea, rune buffer, cursor,
// attachments, input history, and the wizard runtime. It is embedded in
// Model so that field access (e.g. m.textarea) continues to work unchanged.
//
// The Composer never directly modifies Document blocks; it produces a
// Submission which is consumed by the event loop.
type Composer struct {
	textarea                textarea.Model
	input                   []rune
	cursor                  int
	attachmentCount         int
	attachmentNames         []string
	inputAttachments        []inputAttachment
	history                 []string
	historyAttachments      [][]inputAttachment
	historyIndex            int
	historyDraft            string
	historyDraftAttachments []inputAttachment
	pendingQueue            *pendingPrompt
	wizard                  *wizardRuntime
}
