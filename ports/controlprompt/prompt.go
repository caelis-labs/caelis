package controlprompt

import (
	"context"
	"strings"
	"unicode"

	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// Router routes one prompt submission through the app-control prompt layer.
type Router interface {
	Route(context.Context, Request) (Result, error)
}

// StreamSubscriberProvider exposes optional stream fan-out for router results
// that carry live turns.
type StreamSubscriberProvider interface {
	StreamSubscriber() (control.StreamSubscriber, bool)
}

// RouterFactory builds a prompt router from a shared configuration.
type RouterFactory func(RouterConfig) Router

// RouterConfig configures a prompt router implementation.
type RouterConfig struct {
	Service control.Service
	// CommandNames controls /help rendering. When nil, shared command names and
	// registered ACP agent commands are used.
	CommandNames func(context.Context, control.Service) []string
	// CoreCommandAllowed optionally narrows which shared core slash commands this
	// router may execute. Dynamic ACP agent slashes are checked separately.
	CoreCommandAllowed func(context.Context, string) bool
	// DynamicCommandAllowed optionally narrows which registered agent slash
	// commands this router may execute. When nil, all registered agents from the
	// control service are accepted.
	DynamicCommandAllowed func(context.Context, string) bool
	// PrivateSlashHandler handles surface-owned slash commands after ParseSlash
	// and before shared/dynamic routing. It keeps private commands on the same
	// parse path without making protocol/control depend on a surface type.
	PrivateSlashHandler PrivateSlashHandler
}

// Request carries one prompt submission into a control prompt router.
type Request struct {
	Submission control.Submission
}

// PrivateSlashRequest carries a surface-owned slash command after shared
// parsing and before app-control routing.
type PrivateSlashRequest struct {
	Command     string
	Args        string
	ArgsStart   int
	FullText    string
	Attachments []control.Attachment
}

// PrivateSlashHandler handles slash commands owned by one presentation
// surface without adding surface types to the shared router contract.
type PrivateSlashHandler func(context.Context, PrivateSlashRequest) (Result, bool, error)

// Result is the surface-neutral outcome of routing one prompt submission.
//
// Events and ReplayEvents are already ACP/eventstream-shaped and should be
// forwarded by every surface that can display them. Turn carries live streaming
// work and remains owned by the caller until it is closed.
//
// The boolean fields are semantic side effects, not UI instructions. TUI maps
// ClearHistory to transcript clearing and StatusUpdate to its status bar; ACP
// maps those same intents to standard session/update state refreshes.
// SlashResult carries structured command data; surfaces decide how to render it
// or serialize it with control.FormatSlashResult for plain text. Events remain
// available for additional non-redundant notices. Do not add wizard/modal
// rendering state here; interactive workflows stay owned by their surface.
// PrivateResult is only populated by a PrivateSlashHandler and must be
// interpreted by the surface that installed that handler.
type Result struct {
	Handled             bool
	Turn                control.Turn
	Events              []eventstream.Envelope
	SlashResult         *control.SlashCommandResult
	ClearHistory        bool
	ReplayEvents        []eventstream.Envelope
	RefreshCommands     bool
	StatusUpdate        *control.StatusSnapshot
	SuppressTurnDivider bool
	ContinueRunning     bool
	PrivateResult       any
}

// ParseSlash parses a slash command into command and argument text.
func ParseSlash(text string) (cmd, args string, argsStart int, ok bool) {
	textRunes := []rune(strings.TrimSpace(text))
	idx := 0
	if idx >= len(textRunes) || textRunes[idx] != '/' {
		return "", "", 0, false
	}
	idx++
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
	return cmd, args, argsStart, cmd != ""
}

// ParseFirst splits a command argument string into first token and remainder.
func ParseFirst(text string) (first, rest string, restStart int) {
	textRunes := []rune(strings.TrimSpace(text))
	idx := 0
	for idx < len(textRunes) && !unicode.IsSpace(textRunes[idx]) {
		idx++
	}
	first = strings.TrimSpace(string(textRunes[:idx]))
	for idx < len(textRunes) && unicode.IsSpace(textRunes[idx]) {
		idx++
	}
	restStart = idx
	rest = strings.TrimSpace(string(textRunes[idx:]))
	return
}

// AttachmentsForPromptRange selects attachments whose offsets fall inside one
// prompt segment and rewrites their offsets relative to that segment.
func AttachmentsForPromptRange(items []control.Attachment, start int, end int) []control.Attachment {
	if len(items) == 0 {
		return nil
	}
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	out := make([]control.Attachment, 0, len(items))
	for _, item := range items {
		if item.Offset < start || item.Offset > end {
			continue
		}
		out = append(out, control.Attachment{
			Name:     item.Name,
			Offset:   item.Offset - start,
			MimeType: item.MimeType,
			Data:     item.Data,
		})
	}
	return out
}
