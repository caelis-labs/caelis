package acpagentbridge

import (
	"context"
	"strings"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

type normalizingPromptCallbacks struct {
	inner PromptCallbacks
}

func (c normalizingPromptCallbacks) SessionUpdate(ctx context.Context, notification eventstream.SessionNotification) error {
	if c.inner == nil {
		return nil
	}
	return c.inner.SessionUpdate(ctx, normalizeACPStdioTerminalExtension(notification))
}

func (c normalizingPromptCallbacks) RequestPermission(ctx context.Context, req acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	if c.inner == nil {
		return acpsdk.RequestPermissionResponse{}, nil
	}
	return c.inner.RequestPermission(ctx, req)
}

type acpNarrativeFilter struct {
	childTerminal    *acpChildTerminalProjector
	suppressUserEcho bool
}

func newACPNarrativeFilter(suppressUserEcho bool) *acpNarrativeFilter {
	return &acpNarrativeFilter{
		childTerminal:    newACPChildTerminalProjector(),
		suppressUserEcho: suppressUserEcho,
	}
}

func (f *acpNarrativeFilter) FilterNotification(notification eventstream.SessionNotification) (eventstream.SessionNotification, bool) {
	if f == nil {
		return notification, true
	}
	notification = normalizeACPStdioTerminalExtension(notification)
	if chunk, ok := notification.Update.(eventstream.ContentChunk); ok &&
		strings.TrimSpace(chunk.SessionUpdate) == eventstream.UpdateUserMessage && f.suppressUserEcho {
		return eventstream.SessionNotification{}, false
	}
	return notification, true
}

func normalizeACPStdioTerminalExtension(notification eventstream.SessionNotification) eventstream.SessionNotification {
	switch update := notification.Update.(type) {
	case eventstream.ToolCall:
		if acpStdioFinalOnlySpawn(update.Meta) {
			update.Meta, update.Content = withoutACPStdioTerminalMount(update.Meta, update.Content)
		} else {
			update.Meta, update.Content = terminalExtensionMetaFromACPContent(update.Meta, update.ToolCallID, update.Content)
		}
		notification.Update = update
	case eventstream.ToolCallUpdate:
		if acpStdioFinalOnlySpawn(update.Meta) {
			update.Meta, update.Content = withoutACPStdioTerminalMount(update.Meta, update.Content)
		} else {
			update.Meta, update.Content = terminalExtensionMetaFromACPContent(update.Meta, update.ToolCallID, update.Content)
			if terminalID := terminalIDFromMeta(update.Meta); terminalID != "" && acpToolUpdateStatusFinal(update.Status) {
				update.Meta = metautil.WithTerminalExit(update.Meta, terminalID, terminalExitCodeFromRawOutput(update.RawOutput), nil)
			}
		}
		notification.Update = update
	}
	return notification
}

// acpStdioFinalOnlySpawn applies only at RuntimeAgent's ACP output boundary.
// Envelope owner scope is no longer representable there, so nested Spawn uses
// the final-only wire profile. The product Main feed and its typed Task stream
// do not pass through this normalizer.
func acpStdioFinalOnlySpawn(meta map[string]any) bool {
	toolName := metautil.String(meta, metautil.Root, metautil.Runtime, metautil.RuntimeTool, metautil.RuntimeToolName)
	return toolName == spawn.ToolName
}

func withoutACPStdioTerminalMount(meta map[string]any, content []eventstream.ToolCallContent) (map[string]any, []eventstream.ToolCallContent) {
	meta = metautil.WithoutTerminalOutput(meta)
	delete(meta, metautil.TerminalInfoKey)
	delete(meta, metautil.TerminalExitKey)
	if len(meta) == 0 {
		meta = nil
	}
	out := make([]eventstream.ToolCallContent, 0, len(content))
	for _, item := range content {
		if strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			continue
		}
		out = append(out, item)
	}
	return meta, out
}

func terminalExtensionMetaFromACPContent(meta map[string]any, terminalID string, content []eventstream.ToolCallContent) (map[string]any, []eventstream.ToolCallContent) {
	defaultTerminalID := strings.TrimSpace(terminalID)
	terminalID = terminalIDFromMeta(meta)
	out := make([]eventstream.ToolCallContent, 0, len(content))
	var text strings.Builder
	hasTerminalContent := false
	for _, item := range content {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			out = append(out, item)
			continue
		}
		hasTerminalContent = true
		if id := strings.TrimSpace(item.TerminalID); id != "" {
			terminalID = id
		} else if terminalID == "" {
			terminalID = defaultTerminalID
		}
		text.WriteString(session.ExtractProtocolText(item.Content))
	}
	if !hasTerminalContent && terminalID == "" {
		return meta, content
	}
	if terminalID != "" {
		meta = metautil.WithTerminalInfo(meta, terminalID)
		if text.Len() > 0 {
			meta = metautil.WithTerminalOutput(meta, terminalID, text.String())
		}
		out = append(out, eventstream.ToolCallContent{
			Type:       "terminal",
			TerminalID: terminalID,
		})
	}
	return meta, out
}

func terminalIDFromMeta(meta map[string]any) string {
	if output, ok := metautil.TerminalOutput(meta); ok {
		return strings.TrimSpace(output.TerminalID)
	}
	if info, ok := metautil.TerminalInfo(meta); ok {
		return strings.TrimSpace(info.TerminalID)
	}
	if exit, ok := metautil.TerminalExit(meta); ok {
		return strings.TrimSpace(exit.TerminalID)
	}
	return ""
}

func acpToolUpdateStatusFinal(status *string) bool {
	if status == nil {
		return false
	}
	return acpToolStatusFinalString(*status)
}

func acpToolStatusFinalString(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case eventstream.ToolStatusCompleted, eventstream.ToolStatusFailed, "interrupted", "cancelled", "canceled", "terminated", "timed_out", "timeout":
		return true
	default:
		return false
	}
}

func terminalExitCodeFromRawOutput(raw any) *int {
	values, ok := raw.(map[string]any)
	if !ok || len(values) == 0 {
		return nil
	}
	switch typed := values["exit_code"].(type) {
	case int:
		code := typed
		return &code
	case int64:
		code := int(typed)
		return &code
	case float64:
		code := int(typed)
		return &code
	default:
		return nil
	}
}

func acpContentChunkText(update eventstream.Update) (string, string, string, bool) {
	chunk, ok := update.(eventstream.ContentChunk)
	if !ok {
		return "", "", "", false
	}
	updateType := strings.TrimSpace(chunk.SessionUpdate)
	switch updateType {
	case eventstream.UpdateUserMessage, eventstream.UpdateAgentMessage, eventstream.UpdateAgentThought:
	default:
		return "", "", "", false
	}
	return updateType, strings.TrimSpace(chunk.MessageID), acpTextContentText(chunk.Content), true
}

func acpTextContentText(content any) string {
	switch typed := content.(type) {
	case eventstream.TextContent:
		return typed.Text
	case map[string]any:
		text, _ := typed["text"].(string)
		return text
	default:
		return ""
	}
}
