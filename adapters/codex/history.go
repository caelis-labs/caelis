package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
)

const (
	maxStableHistoryReads = 4
	maxLoadReplayBytes    = 64 << 20
)

type historyThread struct {
	ID    string `json:"id"`
	CWD   string `json:"cwd"`
	Name  string `json:"name"`
	Turns []struct {
		ID     string            `json:"id"`
		Status string            `json:"status"`
		Items  []json.RawMessage `json:"items"`
	} `json:"turns"`
}

func (a *agent) loadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	if len(request.McpServers) != 0 {
		return acp.LoadSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": "Codex built-in adapter does not support ACP MCP server injection yet"})
	}
	roots, err := a.options.Workspace.validate(request.Cwd, request.AdditionalDirectories)
	if err != nil {
		return acp.LoadSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	threadID := strings.TrimSpace(string(request.SessionId))
	if threadID == "" {
		return acp.LoadSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": "sessionId is required"})
	}
	if err := a.authorizeStoredThread(ctx, threadID, request.Cwd); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	route, err := a.reserveSession(threadID, request.Cwd, roots, routeBuffering)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	fail := func(loadErr error, replayStarted bool) (acp.LoadSessionResponse, error) {
		a.removeSession(threadID, route)
		if replayStarted && a.connection != nil {
			_ = a.connection.Close()
		}
		return acp.LoadSessionResponse{}, loadErr
	}

	var opened threadOpenResponse
	if err := a.backend.rpc.Request(ctx, "thread/resume", map[string]any{
		"threadId": threadID, "cwd": request.Cwd,
		"runtimeWorkspaceRoots": roots, "excludeTurns": true,
		"approvalPolicy": "on-request", "sandbox": "workspace-write",
	}, &opened); err != nil {
		return fail(err, false)
	}
	route.state.applyOpenResponse(opened)
	if err := a.loadModels(ctx, route.state); err != nil {
		return fail(err, false)
	}

	loadCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	thread, barrier, err := a.readStableHistory(loadCtx, route, threadID)
	if err != nil {
		return fail(err, false)
	}
	stored, storedErr := cleanAbsolute(thread.CWD)
	requested, requestedErr := cleanAbsolute(request.Cwd)
	if storedErr != nil || requestedErr != nil || stored != requested {
		return fail(acp.NewInvalidRequest(map[string]any{"error": "loaded Codex Thread cwd changed during authorization"}), false)
	}
	updates, err := historyUpdates(thread, a.negotiatedTerminalOutputMode())
	if err != nil {
		return fail(err, false)
	}
	encoded, err := json.Marshal(updates)
	if err != nil || len(encoded) > maxLoadReplayBytes {
		return fail(acp.NewInvalidRequest(map[string]any{"error": "Codex history exceeds the 64 MiB replay limit"}), false)
	}

	post := route.acceptStableBarrier(barrier)
	replayStarted := false
	for _, update := range updates {
		replayStarted = true
		if err := a.connection.SessionUpdate(ctx, acp.SessionNotification{
			SessionId: request.SessionId, Update: update,
		}); err != nil {
			return fail(fmt.Errorf("codex adapter: replay history: %w", err), replayStarted)
		}
	}
	for {
		for _, notification := range post {
			if err := route.publish(notification); err != nil {
				return fail(err, replayStarted)
			}
		}
		var live bool
		post, live, err = route.takeBufferedOrSwitchLive()
		if err != nil {
			return fail(err, replayStarted)
		}
		if live {
			break
		}
	}
	return acp.LoadSessionResponse{ConfigOptions: route.state.configOptions()}, nil
}

func (a *agent) readStableHistory(ctx context.Context, route *sessionRoute, threadID string) (historyThread, uint64, error) {
	var previous []byte
	for range maxStableHistoryReads {
		before := route.lastSequence()
		var response struct {
			Thread json.RawMessage `json:"thread"`
		}
		if err := a.backend.rpc.Request(ctx, "thread/read", map[string]any{
			"threadId": threadID, "includeTurns": true,
		}, &response); err != nil {
			return historyThread{}, 0, err
		}
		after := route.lastSequence()
		canonical, err := canonicalJSON(response.Thread)
		if err != nil {
			return historyThread{}, 0, fmt.Errorf("codex adapter: decode Thread history: %w", err)
		}
		if len(previous) > 0 && bytes.Equal(previous, canonical) && before == after && !route.bufferedSince(after) {
			var thread historyThread
			if err := json.Unmarshal(response.Thread, &thread); err != nil {
				return historyThread{}, 0, fmt.Errorf("codex adapter: decode stable Thread history: %w", err)
			}
			if strings.TrimSpace(thread.ID) != threadID {
				return historyThread{}, 0, errors.New("codex adapter: app-server returned history for a different Thread")
			}
			return thread, after, nil
		}
		previous = canonical
	}
	return historyThread{}, 0, acp.NewInvalidRequest(map[string]any{"error": "Codex Thread history did not reach a stable replay barrier"})
}

func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func historyUpdates(thread historyThread, outputMode terminalOutputMode) ([]acp.SessionUpdate, error) {
	updates := make([]acp.SessionUpdate, 0)
	for _, turn := range thread.Turns {
		for _, raw := range turn.Items {
			itemUpdates, err := historyItemUpdates(thread.ID, raw, outputMode)
			if err != nil {
				return nil, err
			}
			updates = append(updates, itemUpdates...)
		}
	}
	return updates, nil
}

func historyItemUpdates(threadID string, raw json.RawMessage, outputMode terminalOutputMode) ([]acp.SessionUpdate, error) {
	var item threadItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, fmt.Errorf("codex adapter: decode history item: %w", err)
	}
	if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Type) == "" {
		return nil, acp.NewInvalidRequest(map[string]any{"error": "Codex history item lacks stable identity"})
	}
	switch item.Type {
	case "userMessage":
		var updates []acp.SessionUpdate
		for _, content := range item.Content {
			block, ok, err := historyContentBlock(content)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			update := acp.UpdateUserMessage(block)
			id := acp.MessageId(stableID(threadID, item.ID, "user-message"))
			update.UserMessageChunk.MessageId = &id
			updates = append(updates, update)
		}
		return updates, nil
	case "agentMessage":
		if item.Text == "" {
			return nil, nil
		}
		update := acp.UpdateAgentMessageText(item.Text)
		id := acp.MessageId(stableID(threadID, item.ID, "agent-message"))
		update.AgentMessageChunk.MessageId = &id
		return []acp.SessionUpdate{update}, nil
	case "reasoning":
		text := reasoningText(item)
		if text == "" {
			return nil, nil
		}
		update := acp.UpdateAgentThoughtText(text)
		id := acp.MessageId(stableID(threadID, item.ID, "reasoning"))
		update.AgentThoughtChunk.MessageId = &id
		return []acp.SessionUpdate{update}, nil
	case "plan":
		if item.Text == "" {
			return nil, nil
		}
		return []acp.SessionUpdate{acp.UpdateAgentThoughtText(item.Text)}, nil
	case "hookPrompt":
		// Hook prompts are provider-internal context, not a user-authored ACP
		// message. They are recognized so load remains forward-compatible but
		// deliberately do not enter the client transcript.
		return nil, nil
	case "subAgentActivity", "enteredReviewMode", "exitedReviewMode", "contextCompaction":
		label := map[string]string{
			"subAgentActivity": "Sub-agent activity", "enteredReviewMode": "Entered review mode",
			"exitedReviewMode": "Exited review mode", "contextCompaction": "Context compacted",
		}[item.Type]
		return []acp.SessionUpdate{acp.UpdateAgentThoughtText(label)}, nil
	default:
		if isToolItem(item.Type) {
			updates := []acp.SessionUpdate{toolStart(threadID, item)}
			if item.Status == "inProgress" {
				return updates, nil
			}
			updates = append(updates, toolComplete(threadID, item, true, false, outputMode))
			return updates, nil
		}
		return nil, acp.NewInvalidRequest(map[string]any{"error": "unsupported Codex history item type: " + item.Type})
	}
}

func historyContentBlock(raw json.RawMessage) (acp.ContentBlock, bool, error) {
	var value struct {
		Type string `json:"type"`
		Text string `json:"text"`
		URL  string `json:"url"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return acp.ContentBlock{}, false, err
	}
	switch value.Type {
	case "text":
		return acp.TextBlock(value.Text), true, nil
	case "image", "localImage":
		text := firstNonEmpty(value.Path, value.URL)
		if text == "" {
			return acp.ContentBlock{}, false, nil
		}
		return acp.TextBlock("[Image: " + text + "]"), true, nil
	case "skill", "mention":
		return acp.TextBlock(value.Text), value.Text != "", nil
	default:
		return acp.ContentBlock{}, false, acp.NewInvalidRequest(map[string]any{"error": "unsupported Codex user history content: " + value.Type})
	}
}
