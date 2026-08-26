package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	acp "github.com/caelis-labs/acp-go-sdk"

	"github.com/caelis-labs/caelis/adapters/codex/internal/appserver"
)

const maxBufferedNotifications = 4096

type routeMode uint8

const (
	routeBuffering routeMode = iota
	routeLive
	routeClosed
)

type sessionRoute struct {
	agent *agent
	state *sessionState

	mu         sync.Mutex
	mode       routeMode
	barrier    uint64
	buffer     []appserver.Notification
	notify     chan appserver.Notification
	closed     chan struct{}
	closeErr   error
	seenItem   map[string]bool
	workerOnce sync.Once
	closeOnce  sync.Once
}

func newSessionRoute(agent *agent, state *sessionState, mode routeMode) *sessionRoute {
	route := &sessionRoute{
		agent: agent, state: state, mode: mode,
		notify: make(chan appserver.Notification, 256), closed: make(chan struct{}),
		seenItem: make(map[string]bool),
	}
	route.workerOnce.Do(func() { go route.run() })
	return route
}

func (r *sessionRoute) enqueue(notification appserver.Notification) {
	r.mu.Lock()
	switch r.mode {
	case routeBuffering:
		if len(r.buffer) >= maxBufferedNotifications {
			r.mu.Unlock()
			r.close(errors.New("codex adapter: notification buffer overflow"))
			return
		}
		r.buffer = append(r.buffer, notification)
		r.mu.Unlock()
		return
	case routeClosed:
		r.mu.Unlock()
		return
	default:
		r.mu.Unlock()
	}
	select {
	case r.notify <- notification:
	default:
		r.close(errors.New("codex adapter: live notification queue overflow"))
	}
}

func (r *sessionRoute) run() {
	for {
		select {
		case <-r.closed:
			return
		case notification := <-r.notify:
			if err := r.publish(notification); err != nil {
				r.close(err)
				return
			}
		}
	}
}

func (r *sessionRoute) close(err error) {
	closedNow := false
	r.closeOnce.Do(func() {
		closedNow = true
		r.mu.Lock()
		r.mode = routeClosed
		r.closeErr = err
		r.mu.Unlock()
		close(r.closed)
		r.state.mu.Lock()
		if done := r.state.turnDone; done != nil {
			select {
			case done <- turnResult{err: err}:
			default:
			}
		}
		r.state.mu.Unlock()
	})
	if closedNow && r.agent != nil {
		r.agent.releaseClosedSession(r.state.threadID, r)
	}
}

func (r *sessionRoute) acceptStableBarrier(sequence uint64) []appserver.Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.barrier = sequence
	post := make([]appserver.Notification, 0, len(r.buffer))
	for _, notification := range r.buffer {
		if notification.Sequence > sequence {
			post = append(post, notification)
		}
	}
	r.buffer = nil
	return post
}

func (r *sessionRoute) takeBufferedOrSwitchLive() ([]appserver.Notification, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mode == routeClosed {
		if r.closeErr != nil {
			return nil, false, r.closeErr
		}
		return nil, false, errors.New("codex adapter: Session route closed")
	}
	if len(r.buffer) > 0 {
		post := append([]appserver.Notification(nil), r.buffer...)
		r.buffer = nil
		return post, false, nil
	}
	r.mode = routeLive
	return nil, true, nil
}

func (r *sessionRoute) drainBufferedToLive() error {
	for {
		batch, live, err := r.takeBufferedOrSwitchLive()
		if err != nil {
			return err
		}
		for _, notification := range batch {
			if err := r.publish(notification); err != nil {
				return err
			}
		}
		if live {
			return nil
		}
	}
}

func (r *sessionRoute) failure() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mode != routeClosed {
		return nil
	}
	if r.closeErr != nil {
		return r.closeErr
	}
	return errors.New("codex adapter: Session route closed")
}

func (r *sessionRoute) bufferedSince(sequence uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, notification := range r.buffer {
		if notification.Sequence > sequence {
			return true
		}
	}
	return false
}

func (r *sessionRoute) lastSequence() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buffer) == 0 {
		return r.barrier
	}
	return r.buffer[len(r.buffer)-1].Sequence
}

func (r *sessionRoute) publish(notification appserver.Notification) error {
	updates, terminal, err := r.translateNotification(notification)
	if err != nil {
		return err
	}
	for _, update := range updates {
		if err := r.agent.connection.SessionUpdate(context.Background(), acp.SessionNotification{
			SessionId: acp.SessionId(r.state.threadID), Update: update,
		}); err != nil {
			return fmt.Errorf("codex adapter: publish ACP update: %w", err)
		}
	}
	if terminal != nil {
		r.state.mu.Lock()
		done := r.state.turnDone
		active := r.state.activeTurnID
		r.state.mu.Unlock()
		if done != nil && (terminal.turnID == "" || active == "" || terminal.turnID == active) {
			select {
			case done <- turnResult{stopReason: terminal.reason, err: terminal.err}:
			default:
			}
		}
	}
	return nil
}

func (r *sessionRoute) handleRequest(ctx context.Context, request appserver.Request) (any, error) {
	if r.agent.connection == nil {
		return appServerFallbackResponse(request.Method)
	}
	switch request.Method {
	case "item/tool/requestUserInput", "mcpServer/elicitation/request":
		return appServerFallbackResponse(request.Method)
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval":
	default:
		return appServerFallbackResponse(request.Method)
	}
	var params map[string]any
	if err := json.Unmarshal(request.Params, &params); err != nil {
		return appServerFallbackResponse(request.Method)
	}
	itemID, _ := params["itemId"].(string)
	if itemID == "" {
		itemID, _ = params["approvalId"].(string)
	}
	title, rawInput, kind := approvalPresentation(request.Method, params)
	options := []acp.PermissionOption{
		{OptionId: "allow_once", Name: "Yes, proceed", Kind: acp.PermissionOptionKindAllowOnce},
		{OptionId: "allow_for_session", Name: "Yes, allow for this session", Kind: acp.PermissionOptionKindAllowAlways},
		{OptionId: "decline", Name: "No, continue without it", Kind: acp.PermissionOptionKindRejectOnce},
		{OptionId: "cancel", Name: "No, and stop this turn", Kind: acp.PermissionOptionKindRejectOnce},
	}
	response, err := r.agent.connection.RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: acp.SessionId(r.state.threadID), Options: options,
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId(stableID(r.state.threadID, firstNonEmpty(itemID, request.Method), "approval")),
			Title:      &title, Kind: &kind, RawInput: rawInput,
		},
	})
	if err != nil || response.Outcome.Cancelled != nil || response.Outcome.Selected == nil {
		return appServerFallbackResponse(request.Method)
	}
	selected := string(response.Outcome.Selected.OptionId)
	switch request.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		decision := "cancel"
		switch selected {
		case "allow_once":
			decision = "accept"
		case "allow_for_session":
			decision = "acceptForSession"
		case "decline":
			decision = "decline"
		}
		return map[string]any{"decision": decision}, nil
	case "item/permissions/requestApproval":
		if selected == "allow_once" || selected == "allow_for_session" {
			permissions, _ := params["permissions"].(map[string]any)
			scope := "turn"
			if selected == "allow_for_session" {
				scope = "session"
			}
			return map[string]any{"permissions": permissions, "scope": scope, "strictAutoReview": false}, nil
		}
	}
	return appServerFallbackResponse(request.Method)
}

func approvalPresentation(method string, params map[string]any) (string, any, acp.ToolKind) {
	switch method {
	case "item/commandExecution/requestApproval":
		command := params["command"]
		return "Run command", map[string]any{"command": command, "cwd": params["cwd"], "reason": params["reason"]}, acp.ToolKindExecute
	case "item/fileChange/requestApproval":
		return "Apply file changes", params, acp.ToolKindEdit
	case "item/permissions/requestApproval":
		return "Grant additional permissions", params, acp.ToolKindOther
	default:
		return "Codex request", params, acp.ToolKindOther
	}
}

type terminalNotification struct {
	turnID string
	reason acp.StopReason
	err    error
}

func (r *sessionRoute) translateNotification(notification appserver.Notification) ([]acp.SessionUpdate, *terminalNotification, error) {
	var params map[string]json.RawMessage
	if err := json.Unmarshal(notification.Params, &params); err != nil {
		return nil, nil, fmt.Errorf("codex adapter: decode %s notification: %w", notification.Method, err)
	}
	switch notification.Method {
	case "item/agentMessage/delta":
		var value struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		_ = json.Unmarshal(notification.Params, &value)
		if value.Delta == "" {
			return nil, nil, nil
		}
		update := acp.UpdateAgentMessageText(value.Delta)
		id := acp.MessageId(stableID(r.state.threadID, value.ItemID, "agent-message"))
		update.AgentMessageChunk.MessageId = &id
		r.markSeen(value.ItemID)
		return []acp.SessionUpdate{update}, nil, nil
	case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		var value struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		_ = json.Unmarshal(notification.Params, &value)
		if value.Delta == "" {
			return nil, nil, nil
		}
		update := acp.UpdateAgentThoughtText(value.Delta)
		id := acp.MessageId(stableID(r.state.threadID, value.ItemID, "reasoning"))
		update.AgentThoughtChunk.MessageId = &id
		r.markSeen(value.ItemID)
		return []acp.SessionUpdate{update}, nil, nil
	case "item/started":
		item := params["item"]
		return liveItemStarted(r.state.threadID, item)
	case "item/completed":
		item := params["item"]
		return liveItemCompleted(r.state.threadID, item, r.itemSeen(item))
	case "item/commandExecution/outputDelta", "command/exec/outputDelta":
		var value struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		_ = json.Unmarshal(notification.Params, &value)
		if value.ItemID == "" || value.Delta == "" {
			return nil, nil, nil
		}
		return []acp.SessionUpdate{acp.UpdateToolCall(
			acp.ToolCallId(stableID(r.state.threadID, value.ItemID, "tool")),
			acp.WithUpdateRawOutput(value.Delta),
		)}, nil, nil
	case "turn/completed":
		var value struct {
			Turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  any    `json:"error"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(notification.Params, &value)
		terminal := &terminalNotification{turnID: value.Turn.ID, reason: acp.StopReasonEndTurn}
		switch value.Turn.Status {
		case "interrupted", "cancelled":
			terminal.reason = acp.StopReasonCancelled
		case "failed":
			terminal.err = fmt.Errorf("codex turn failed: %v", value.Turn.Error)
		}
		return nil, terminal, nil
	case "warning", "deprecationNotice", "configWarning", "guardianWarning":
		var value struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(notification.Params, &value)
		if strings.TrimSpace(value.Message) != "" {
			return []acp.SessionUpdate{acp.UpdateAgentThoughtText(value.Message)}, nil, nil
		}
	}
	return nil, nil, nil
}

func (r *sessionRoute) markSeen(itemID string) {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return
	}
	r.mu.Lock()
	r.seenItem[itemID] = true
	r.mu.Unlock()
}

func (r *sessionRoute) itemSeen(raw json.RawMessage) bool {
	var item struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &item)
	r.mu.Lock()
	seen := r.seenItem[item.ID]
	r.seenItem[item.ID] = true
	r.mu.Unlock()
	return seen
}
