package acpagentbridge

import (
	"context"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

type normalizingPromptCallbacks struct {
	inner acp.PromptCallbacks
}

func (c normalizingPromptCallbacks) SessionUpdate(ctx context.Context, notification acp.SessionNotification) error {
	if c.inner == nil {
		return nil
	}
	return c.inner.SessionUpdate(ctx, normalizeACPStdioTerminalExtension(notification))
}

func (c normalizingPromptCallbacks) RequestPermission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	if c.inner == nil {
		return acp.RequestPermissionResponse{}, nil
	}
	return c.inner.RequestPermission(ctx, req)
}

const acpNarrativeReplayMinRunes = 4

type acpNarrativeKey struct {
	Source     acpFilterSource
	UpdateType string
	MessageID  string
}

type acpTerminalOutputKey struct {
	Source     acpFilterSource
	ToolCallID string
	TerminalID string
}

type acpNarrativeFilter struct {
	mu               sync.Mutex
	sent             map[acpNarrativeKey]string
	terminalSent     map[acpTerminalOutputKey]acpTerminalOutputState
	suppressUserEcho bool
}

func newACPNarrativeFilter(suppressUserEcho bool) *acpNarrativeFilter {
	return &acpNarrativeFilter{
		sent:             map[acpNarrativeKey]string{},
		terminalSent:     map[acpTerminalOutputKey]acpTerminalOutputState{},
		suppressUserEcho: suppressUserEcho,
	}
}

func (f *acpNarrativeFilter) FilterNotification(notification acp.SessionNotification) (acp.SessionNotification, bool) {
	return f.FilterNotificationWithFinal(notification, false)
}

func (f *acpNarrativeFilter) FilterNotificationWithFinal(notification acp.SessionNotification, final bool) (acp.SessionNotification, bool) {
	return f.filterNotificationWithFinal(notification, final, acpFilterSource{SessionID: strings.TrimSpace(notification.SessionID)})
}

func (f *acpNarrativeFilter) filterNotificationWithFinal(notification acp.SessionNotification, final bool, source acpFilterSource) (acp.SessionNotification, bool) {
	if f == nil {
		return notification, true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	notification = normalizeACPStdioTerminalExtension(notification)
	notification = f.filterTerminalOutput(notification, source, final)
	updateType, messageID, text, ok := acpContentChunkText(notification.Update)
	if !ok {
		if acpNarrativeBarrier(notification.Update) {
			f.clearNarrativeSource(source)
		}
		return notification, true
	}
	if text == "" {
		return notification, true
	}
	if updateType == acp.UpdateUserMessage {
		if f.suppressUserEcho {
			return acp.SessionNotification{}, false
		}
		return notification, true
	}
	key := acpNarrativeKey{Source: source, UpdateType: updateType, MessageID: messageID}
	previous := f.sent[key]
	if final && previous != "" {
		return acp.SessionNotification{}, false
	}
	if replacement, cumulative := acpNarrativeUnsentSuffix(previous, text); cumulative {
		if replacement == "" {
			return acp.SessionNotification{}, false
		}
		f.sent[key] = previous + replacement
		return cloneContentChunkNotificationWithText(notification, replacement), true
	}
	f.sent[key] = previous + text
	return notification, true
}

func (f *acpNarrativeFilter) clearNarrativeSource(source acpFilterSource) {
	for key := range f.sent {
		if key.Source == source {
			delete(f.sent, key)
		}
	}
}

func (f *acpNarrativeFilter) resetSource(source acpFilterSource) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clearNarrativeSource(source)
}

func (f *acpNarrativeFilter) filterTerminalOutput(notification acp.SessionNotification, source acpFilterSource, final bool) acp.SessionNotification {
	if f == nil {
		return notification
	}
	switch update := notification.Update.(type) {
	case acp.ToolCall:
		update.Meta = f.filterTerminalOutputMeta(source, update.ToolCallID, update.Status, final, update.Meta)
		notification.Update = update
	case acp.ToolCallUpdate:
		status := ""
		if update.Status != nil {
			status = *update.Status
		}
		update.Meta = f.filterTerminalOutputMeta(source, update.ToolCallID, status, final, update.Meta)
		notification.Update = update
	}
	return notification
}

func (f *acpNarrativeFilter) filterTerminalOutputMeta(source acpFilterSource, toolCallID string, status string, final bool, meta map[string]any) map[string]any {
	output, ok := metautil.TerminalOutput(meta)
	if !ok {
		return meta
	}
	key := acpTerminalOutputFilterKey(source, toolCallID, output.TerminalID)
	state := f.terminalSent[key]
	isFinal := final || acpToolStatusFinalString(status)
	if state.Text != "" && (isFinal || state.SawFinal) {
		if replacement, cumulative := acpTerminalUnsentSuffix(state.Text, output.Data); cumulative {
			if replacement == "" {
				return withoutACPTerminalOutput(meta)
			}
			state.Text += replacement
			state.observeFinal(isFinal)
			f.terminalSent[key] = state
			return metautil.WithTerminalOutput(meta, output.TerminalID, replacement)
		}
	}
	state.Text += output.Data
	state.observeFinal(isFinal)
	f.terminalSent[key] = state
	return meta
}

type acpTerminalOutputState struct {
	Text     string
	SawFinal bool
}

func (s *acpTerminalOutputState) observeFinal(final bool) {
	if final {
		s.SawFinal = true
	}
}

func acpTerminalOutputFilterKey(source acpFilterSource, toolCallID string, terminalID string) acpTerminalOutputKey {
	return acpTerminalOutputKey{
		Source:     source,
		ToolCallID: strings.TrimSpace(toolCallID),
		TerminalID: strings.TrimSpace(terminalID),
	}
}

func acpTerminalUnsentSuffix(previous string, incoming string) (string, bool) {
	if previous == "" || incoming == "" {
		return "", false
	}
	if incoming == previous || strings.HasSuffix(previous, incoming) {
		return "", true
	}
	if strings.HasPrefix(incoming, previous) {
		return strings.TrimPrefix(incoming, previous), true
	}
	return "", false
}

func withoutACPTerminalOutput(meta map[string]any) map[string]any {
	out := metautil.CloneMap(meta)
	if len(out) == 0 {
		return nil
	}
	delete(out, metautil.TerminalOutputKey)
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeACPStdioTerminalExtension(notification acp.SessionNotification) acp.SessionNotification {
	switch update := notification.Update.(type) {
	case acp.ToolCall:
		update.Meta, update.Content = terminalExtensionMetaFromACPContent(update.Meta, update.ToolCallID, update.Content)
		notification.Update = update
	case acp.ToolCallUpdate:
		update.Meta, update.Content = terminalExtensionMetaFromACPContent(update.Meta, update.ToolCallID, update.Content)
		if terminalID := terminalIDFromMeta(update.Meta); terminalID != "" && acpToolUpdateStatusFinal(update.Status) {
			update.Meta = metautil.WithTerminalExit(update.Meta, terminalID, terminalExitCodeFromRawOutput(update.RawOutput), nil)
		}
		notification.Update = update
	}
	return notification
}

func terminalExtensionMetaFromACPContent(meta map[string]any, terminalID string, content []acp.ToolCallContent) (map[string]any, []acp.ToolCallContent) {
	defaultTerminalID := strings.TrimSpace(terminalID)
	terminalID = terminalIDFromMeta(meta)
	out := make([]acp.ToolCallContent, 0, len(content))
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
		text.WriteString(schema.ExtractTextValue(item.Content))
	}
	if !hasTerminalContent && terminalID == "" {
		return meta, content
	}
	if terminalID != "" {
		meta = metautil.WithTerminalInfo(meta, terminalID)
		if text.Len() > 0 {
			meta = metautil.WithTerminalOutput(meta, terminalID, text.String())
		}
		out = append(out, acp.ToolCallContent{
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
	case acp.ToolStatusCompleted, acp.ToolStatusFailed, "interrupted", "cancelled", "canceled", "terminated", "timed_out", "timeout":
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

func acpContentChunkText(update acp.Update) (string, string, string, bool) {
	chunk, ok := update.(acp.ContentChunk)
	if !ok {
		return "", "", "", false
	}
	updateType := strings.TrimSpace(chunk.SessionUpdate)
	switch updateType {
	case acp.UpdateUserMessage, acp.UpdateAgentMessage, acp.UpdateAgentThought:
	default:
		return "", "", "", false
	}
	return updateType, strings.TrimSpace(chunk.MessageID), acpTextContentText(chunk.Content), true
}

func acpTextContentText(content any) string {
	switch typed := content.(type) {
	case acp.TextContent:
		return typed.Text
	case map[string]any:
		text, _ := typed["text"].(string)
		return text
	default:
		return ""
	}
}

func cloneContentChunkNotificationWithText(notification acp.SessionNotification, text string) acp.SessionNotification {
	chunk, ok := notification.Update.(acp.ContentChunk)
	if !ok {
		return notification
	}
	chunk.Content = acp.TextContent{Type: "text", Text: text}
	notification.Update = chunk
	return notification
}

func acpNarrativeBarrier(update acp.Update) bool {
	switch update.(type) {
	case acp.ToolCallUpdate, *acp.ToolCallUpdate, acp.PlanUpdate, *acp.PlanUpdate:
		return true
	default:
		return false
	}
}

func acpNarrativeUnsentSuffix(previous string, incoming string) (string, bool) {
	if previous == "" || incoming == "" {
		return "", false
	}
	if strings.HasSuffix(previous, incoming) && len([]rune(incoming)) >= acpNarrativeReplayMinRunes {
		return "", true
	}
	if strings.HasPrefix(previous, incoming) && len([]rune(incoming)) >= acpNarrativeReplayMinRunes {
		return "", true
	}
	if strings.HasPrefix(incoming, previous) && len([]rune(previous)) >= acpNarrativeReplayMinRunes {
		return strings.TrimPrefix(incoming, previous), true
	}
	if overlap := acpNarrativeSuffixPrefixOverlap(previous, incoming); overlap >= acpNarrativeReplayMinRunes {
		return string([]rune(incoming)[overlap:]), true
	}
	return "", false
}

func acpNarrativeSuffixPrefixOverlap(previous string, incoming string) int {
	left := []rune(previous)
	right := []rune(incoming)
	max := min(len(left), len(right))
	for n := max; n >= acpNarrativeReplayMinRunes; n-- {
		if string(left[len(left)-n:]) == string(right[:n]) {
			return n
		}
	}
	return 0
}
