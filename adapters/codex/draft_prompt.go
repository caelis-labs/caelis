package codex

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/adapters/codex/internal/appserver"
	"github.com/caelis-labs/caelis/internal/acpdraft"
)

func (d *draftAgent) Prompt(ctx context.Context, request v2.PromptRequest) (v2.PromptResponse, error) {
	if err := d.ready(); err != nil {
		return v2.PromptResponse{}, err
	}
	guard, err := acpdraft.ParseActiveOnly(request.Meta)
	if err != nil {
		return v2.PromptResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	if guard != nil && guard.IdleOnly {
		return v2.PromptResponse{}, acp.NewInvalidParams(map[string]any{"error": "Codex app-server does not support atomic idle-only prompt admission"})
	}
	state, err := d.core.lookupSession(string(request.SessionId))
	if err != nil {
		return v2.PromptResponse{}, err
	}
	blocks, err := draftConvert[[]acp.ContentBlock](request.Prompt)
	if err != nil {
		return v2.PromptResponse{}, err
	}
	input, err := promptInput(blocks)
	if err != nil {
		return v2.PromptResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	// The native request and its insertion fence are serialized per Thread.
	// A second prompt never steals the first prompt's actual user item receipt.
	state.promptMu.Lock()
	defer state.promptMu.Unlock()
	if err = ctx.Err(); err != nil {
		return v2.PromptResponse{}, err
	}
	if err = state.route.failure(); err != nil {
		return v2.PromptResponse{}, err
	}
	state.mu.Lock()
	turnID := state.activeTurnID
	loading := state.draftLoading
	state.mu.Unlock()
	if loading {
		return v2.PromptResponse{}, acp.NewInvalidRequest(map[string]any{"error": "Session setup is not complete"})
	}
	if guard != nil && (turnID == "" || turnID != guard.ActivityID) {
		return v2.PromptResponse{}, acpdraft.InactivePromptError()
	}
	session := d.session(state.threadID)
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	nativeCtx, nativeCancel := context.WithCancelCause(startCtx)
	defer nativeCancel(nil)
	pending := &draftInsertion{turnID: turnID, token: rand.Text(), changed: make(chan struct{}, 1), nativeCancel: nativeCancel}
	session.mu.Lock()
	session.pending = pending
	if turnID == "" {
		session.cancelRequested = false
	}
	session.mu.Unlock()
	defer func() {
		session.mu.Lock()
		if session.pending == pending {
			session.pending = nil
		}
		session.mu.Unlock()
	}()
	if turnID != "" {
		var response struct {
			TurnID string `json:"turnId"`
		}
		err = d.core.backend.rpc.Request(nativeCtx, "turn/steer", map[string]any{"threadId": state.threadID, "expectedTurnId": turnID, "input": input, "clientUserMessageId": pending.token}, &response)
		if err == nil && strings.TrimSpace(response.TurnID) != turnID {
			err = errors.New("codex adapter: steering acknowledgement does not identify the expected Turn; insertion outcome is unknown")
		}
		if err != nil && guard != nil {
			state.mu.Lock()
			active := state.activeTurnID
			state.mu.Unlock()
			// Only a native rejected RPC combined with a completed target proves that
			// this guarded submission could not begin replacement work.
			var rejection *appserver.Error
			if errors.As(err, &rejection) && active != guard.ActivityID {
				return v2.PromptResponse{}, acpdraft.InactivePromptError()
			}
		}
	} else {
		params := state.promptParams(d.core.cachedAccountType(), input)
		params["clientUserMessageId"] = pending.token
		var response struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		err = d.core.backend.rpc.Request(nativeCtx, "turn/start", params, &response)
		turnID = strings.TrimSpace(response.Turn.ID)
		if err == nil && turnID == "" {
			err = errors.New("codex adapter: app-server returned an empty Turn ID")
		}
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			state.route.closeAsync(errors.New("codex adapter: prompt insertion outcome is unknown after timeout"))
		}
		return v2.PromptResponse{}, err
	}
	session.pubMu.Lock()
	session.mu.Lock()
	pending.turnID = turnID
	cancelled := session.cancelRequested
	terminal := session.terminalTurn == turnID
	blocked := session.hasPermissionLocked(turnID)
	session.mu.Unlock()
	if !terminal {
		state.mu.Lock()
		state.activeTurnID = turnID
		state.mu.Unlock()
		update := v2.RunningUpdate()
		if blocked {
			update = v2.RequiresActionUpdate()
		}
		err = d.sendStateUnlocked(state.threadID, turnID, update)
	}
	session.pubMu.Unlock()
	if err != nil {
		return v2.PromptResponse{}, err
	}
	if cancelled || ctx.Err() != nil {
		_ = d.core.interrupt(startCtx, state)
	}
	for {
		session.mu.Lock()
		inserted := pending.messageID != "" && pending.echoTurn == turnID
		id := pending.messageID
		terminal = session.terminalTurn == turnID
		session.mu.Unlock()
		if inserted {
			return v2.PromptResponse{MessageId: id, Meta: acpdraft.ActivityMeta(turnID)}, nil
		}
		if terminal {
			return v2.PromptResponse{}, acp.NewInternalError(map[string]any{"error": "Codex Turn ended without an observed user-message insertion; outcome is unknown"})
		}
		select {
		case <-pending.changed:
		case <-state.route.closed:
			return v2.PromptResponse{}, state.route.failure()
		case <-d.connection.Done():
			return v2.PromptResponse{}, d.connection.Err()
		case <-d.core.backend.Done():
			return v2.PromptResponse{}, d.core.backend.Err()
		case <-ctx.Done():
			session.mu.Lock()
			session.cancelRequested = true
			session.mu.Unlock()
			_ = d.core.interrupt(startCtx, state)
			return v2.PromptResponse{}, context.Cause(ctx)
		case <-startCtx.Done():
			_ = d.core.interrupt(startCtx, state)
			state.route.closeAsync(errors.New("codex adapter: no user-message insertion fence before timeout"))
			return v2.PromptResponse{}, errors.New("codex adapter: prompt insertion outcome is unknown after timeout")
		}
	}
}

func (s *sessionState) promptParams(accountType string, input []map[string]any) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	params := map[string]any{"threadId": s.threadID, "input": input, "cwd": s.cwd, "summary": s.reasoningSummaryModeLocked(accountType)}
	if s.model != "" {
		params["model"] = s.model
	}
	if s.effort != "" {
		params["effort"] = s.effort
	}
	if s.serviceTier != nil {
		params["serviceTier"] = *s.serviceTier
	}
	return params
}

func (d *draftAgent) Cancel(ctx context.Context, request v2.CancelSessionNotification) error {
	if err := d.ready(); err != nil {
		return err
	}
	// Cancellation of an already closed Session has no effect.
	state, _ := d.core.lookupSession(string(request.SessionId))
	if state == nil {
		return nil
	}
	session := d.session(state.threadID)
	session.mu.Lock()
	session.cancelRequested = true
	for _, permission := range session.permissionCancels {
		permission.cancel()
	}
	session.mu.Unlock()
	return d.core.interrupt(ctx, state)
}

func (d *draftAgent) publishNative(route *sessionRoute, notification appserver.Notification) (bool, error) {
	if route.failure() != nil {
		return true, nil
	}
	current, err := d.core.lookupSession(route.state.threadID)
	if err != nil || current != route.state {
		return true, nil
	}
	var params struct {
		TurnID string `json:"turnId"`
		Turn   struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  any    `json:"error"`
		} `json:"turn"`
		Item json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(notification.Params, &params); err != nil {
		return true, fmt.Errorf("codex adapter: decode draft notification: %w", err)
	}
	session := d.session(route.state.threadID)
	switch notification.Method {
	case "turn/started":
		id := strings.TrimSpace(params.Turn.ID)
		if id == "" {
			return true, errors.New("codex adapter: started Turn has no identity")
		}
		session.pubMu.Lock()
		defer session.pubMu.Unlock()
		session.mu.Lock()
		terminal := session.terminalTurn == id
		blocked := session.hasPermissionLocked(id)
		if session.pending != nil && session.pending.turnID == "" {
			session.pending.turnID = id
		}
		session.mu.Unlock()
		if terminal {
			return true, nil
		}
		route.state.mu.Lock()
		route.state.activeTurnID = id
		route.state.mu.Unlock()
		update := v2.RunningUpdate()
		if blocked {
			update = v2.RequiresActionUpdate()
		}
		return true, d.sendStateUnlocked(route.state.threadID, id, update)
	case "turn/completed":
		id := strings.TrimSpace(params.Turn.ID)
		if id == "" {
			return true, errors.New("codex adapter: completed Turn has no identity")
		}
		session.pubMu.Lock()
		defer session.pubMu.Unlock()
		session.mu.Lock()
		session.terminalTurn = id
		for _, permission := range session.permissionCancels {
			if permission.activity == id {
				permission.cancel()
			}
		}
		pending := session.pending
		session.mu.Unlock()
		route.state.mu.Lock()
		if route.state.activeTurnID == id {
			route.state.activeTurnID = ""
		}
		route.state.mu.Unlock()
		err := d.sendStateUnlocked(route.state.threadID, id, draftTerminalState(params.Turn.Status, params.Turn.Error))
		if pending != nil {
			select {
			case pending.changed <- struct{}{}:
			default:
			}
		}
		return true, err
	case "item/started", "item/completed":
		var item threadItem
		if err := json.Unmarshal(params.Item, &item); err != nil {
			return true, err
		}
		if item.Type == "userMessage" {
			blocks := make([]v2.ContentBlock, 0, len(item.Content))
			for _, content := range item.Content {
				block, err := draftUserContent(content)
				if err != nil {
					return true, err
				}
				converted, err := draftConvert[v2.ContentBlock](block)
				if err != nil {
					return true, err
				}
				blocks = append(blocks, converted)
			}
			id := v2.MessageId(stableID(route.state.threadID, item.ID, "user-message"))
			if item.ID == "" || params.TurnID == "" {
				return true, errors.New("codex adapter: live user message lacks item or Turn identity")
			}
			if err := d.connection.SessionUpdate(context.Background(), v2.UpdateSessionNotification{SessionId: v2.SessionId(route.state.threadID), Update: v2.SessionUpdate{UserMessage: &v2.SessionUpdateUserMessage{SessionUpdate: "user_message", MessageId: id, Content: blocks}}}); err != nil {
				return true, err
			}
			session.mu.Lock()
			pending := session.pending
			if pending != nil && item.ClientID == pending.token && (pending.turnID == "" || pending.turnID == params.TurnID) {
				if pending.messageID != "" && pending.messageID != id {
					session.mu.Unlock()
					return true, errors.New("codex adapter: native insertion token identifies multiple user items")
				}
				pending.echoTurn = params.TurnID
				pending.messageID = id
				select {
				case pending.changed <- struct{}{}:
				default:
				}
			}
			session.mu.Unlock()
			return true, nil
		}
		if notification.Method == "item/completed" && (item.Type == "agentMessage" || item.Type == "reasoning") {
			text, role := item.Text, "agent-message"
			if item.Type == "reasoning" {
				text = reasoningText(item)
				role = "reasoning"
			}
			id := v2.MessageId(stableID(route.state.threadID, item.ID, role))
			update := v2.SessionUpdate{}
			if item.Type == "agentMessage" {
				update.AgentMessage = &v2.SessionUpdateAgentMessage{SessionUpdate: "agent_message", MessageId: id, Content: []v2.ContentBlock{v2.TextBlock(text)}}
			} else {
				update.AgentThought = &v2.SessionUpdateAgentThought{SessionUpdate: "agent_thought", MessageId: id, Content: []v2.ContentBlock{v2.TextBlock(text)}}
			}
			return true, d.connection.SessionUpdate(context.Background(), v2.UpdateSessionNotification{SessionId: v2.SessionId(route.state.threadID), Update: update})
		}
	}
	return false, nil
}

func draftTerminalState(status string, failure any) v2.SessionUpdate {
	switch status {
	case "interrupted", "cancelled":
		return v2.IdleUpdate("cancelled")
	case "failed":
		return v2.IdleErrorUpdate(&v2.Error{Code: v2.ErrorCode{InternalError: acp.Ptr(v2.ErrorCodeInternalError(-32603))}, Message: fmt.Sprintf("Codex Turn failed: %v", failure)})
	case "completed":
		return v2.IdleUpdate("end_turn")
	default:
		return v2.IdleErrorUpdate(&v2.Error{Code: v2.ErrorCode{InternalError: acp.Ptr(v2.ErrorCodeInternalError(-32603))}, Message: "Codex Turn has an unknown terminal state: " + status})
	}
}
func (d *draftAgent) sendState(id, activity string, update v2.SessionUpdate) error {
	session := d.session(id)
	session.pubMu.Lock()
	defer session.pubMu.Unlock()
	if update.StateUpdate.StateUpdate.Running != nil || update.StateUpdate.StateUpdate.RequiresAction != nil {
		session.mu.Lock()
		terminal := session.terminalTurn == activity
		blocked := session.hasPermissionLocked(activity)
		session.mu.Unlock()
		state, err := d.core.lookupSession(id)
		if err != nil {
			return err
		}
		state.mu.Lock()
		active := state.activeTurnID
		state.mu.Unlock()
		if activity == "" || terminal || active != activity {
			if update.StateUpdate.StateUpdate.RequiresAction != nil {
				return errors.New("codex adapter: native approval activity has ended")
			}
			return nil
		}
		if blocked {
			update = v2.RequiresActionUpdate()
		}
	}
	return d.sendStateUnlocked(id, activity, update)
}
func (d *draftAgent) sendStateUnlocked(id, activity string, update v2.SessionUpdate) error {
	raw, err := json.Marshal(update)
	if err != nil {
		return err
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	fields["_meta"] = acpdraft.ActivityMeta(activity)
	raw, err = json.Marshal(fields)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &update); err != nil {
		return err
	}
	return d.connection.SessionUpdate(context.Background(), v2.UpdateSessionNotification{SessionId: v2.SessionId(id), Update: update})
}

// preparePermission binds the native approval to its actual foreground Turn.
// Codex may send a request before the turn/start response reaches this adapter.
func (d *draftAgent) preparePermission(route *sessionRoute, request appserver.Request) bool {
	switch request.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "mcpServer/elicitation/request":
	default:
		return true
	}
	if route.failure() != nil {
		return false
	}
	current, err := d.core.lookupSession(route.state.threadID)
	if err != nil || current != route.state {
		return false
	}
	var params struct {
		TurnID string `json:"turnId"`
	}
	if json.Unmarshal(request.Params, &params) != nil {
		return false
	}
	session := d.session(route.state.threadID)
	session.pubMu.Lock()
	defer session.pubMu.Unlock()
	session.mu.Lock()
	terminal := params.TurnID != "" && session.terminalTurn == params.TurnID
	cancelled := session.cancelRequested
	session.mu.Unlock()
	if terminal || cancelled {
		return false
	}
	route.state.mu.Lock()
	defer route.state.mu.Unlock()
	if params.TurnID == "" {
		return route.state.activeTurnID != ""
	}
	if route.state.activeTurnID != "" && route.state.activeTurnID != params.TurnID {
		return false
	}
	route.state.activeTurnID = params.TurnID
	return true
}
