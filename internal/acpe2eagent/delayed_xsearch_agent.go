package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp"
)

// delayedXSearchAgent is an external wire-level ACP fixture. Its repeated
// start payloads deliberately resemble the former Control-watchdog false
// positive while its completed payloads carry distinct query arguments.
type delayedXSearchAgent struct {
	mu      sync.Mutex
	current map[string]map[string]string
}

var (
	_ acp.Agent          = (*delayedXSearchAgent)(nil)
	_ acp.ConfigProvider = (*delayedXSearchAgent)(nil)
)

func newDelayedXSearchAgent() *delayedXSearchAgent {
	return &delayedXSearchAgent{current: map[string]map[string]string{}}
}

func (a *delayedXSearchAgent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{
		ProtocolVersion: acp.CurrentProtocolVersion,
		AgentCapabilities: acp.AgentCapabilities{
			Auth:                map[string]any{},
			MCPCapabilities:     acp.MCPCapabilities{},
			PromptCapabilities:  acp.PromptCapabilities{},
			SessionCapabilities: map[string]json.RawMessage{},
		},
		AgentInfo: &acp.Implementation{
			Name:    "caelis-acp-wire-e2e",
			Title:   "Caelis ACP Wire E2E Agent",
			Version: "0.1.0",
		},
		AuthMethods: []json.RawMessage{},
	}, nil
}

func (*delayedXSearchAgent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *delayedXSearchAgent) NewSession(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	sessionID := newSessionID()
	a.mu.Lock()
	a.current[sessionID] = map[string]string{"model": "sonnet", "effort": "high"}
	options := a.configOptionsLocked(sessionID)
	a.mu.Unlock()
	return acp.NewSessionResponse{SessionID: sessionID, ConfigOptions: options}, nil
}

func (a *delayedXSearchAgent) Prompt(ctx context.Context, req acp.PromptRequest, callbacks acp.PromptCallbacks) (acp.PromptResponse, error) {
	for index := 1; index <= 6; index++ {
		toolCallID := fmt.Sprintf("x-search-%d", index)
		if err := callbacks.SessionUpdate(ctx, acp.SessionNotification{
			SessionID: req.SessionID,
			Update: acp.ToolCall{
				SessionUpdate: acp.UpdateToolCall,
				ToolCallID:    toolCallID,
				Title:         "X search:",
				Kind:          acp.ToolKindSearch,
				Status:        acp.ToolStatusInProgress,
				RawInput:      map[string]any{"variant": "XSearch", "backend": true},
			},
		}); err != nil {
			recordDelayedXSearchInterruption("start update: " + err.Error())
			return acp.PromptResponse{}, err
		}
		title := "X search:"
		status := acp.ToolStatusCompleted
		query := fmt.Sprintf("CAELIS_ACP_XSEARCH_QUERY_%d", index)
		serializedInput, err := json.Marshal(map[string]any{
			"query": query,
			"limit": "3",
			"mode":  "Latest",
		})
		if err != nil {
			return acp.PromptResponse{}, err
		}
		if err := callbacks.SessionUpdate(ctx, acp.SessionNotification{
			SessionID: req.SessionID,
			Update: acp.ToolCallUpdate{
				SessionUpdate: acp.UpdateToolCallInfo,
				ToolCallID:    toolCallID,
				Title:         &title,
				Status:        &status,
				RawOutput: map[string]any{
					"name":  "x_keyword_search",
					"input": string(serializedInput),
				},
			},
		}); err != nil {
			recordDelayedXSearchInterruption("completed update: " + err.Error())
			return acp.PromptResponse{}, err
		}
	}

	timer := time.NewTimer(350 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		recordDelayedXSearchInterruption("prompt context: " + ctx.Err().Error())
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, ctx.Err()
	case <-timer.C:
	}
	if err := callbacks.SessionUpdate(ctx, acp.SessionNotification{
		SessionID: req.SessionID,
		Update: acp.ContentChunk{
			SessionUpdate: acp.UpdateAgentMessage,
			Content:       acp.TextContent{Type: "text", Text: "external xsearch sequence complete"},
			MessageID:     "xsearch-final",
		},
	}); err != nil {
		recordDelayedXSearchInterruption("final update: " + err.Error())
		return acp.PromptResponse{}, err
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (*delayedXSearchAgent) Cancel(context.Context, acp.CancelNotification) error {
	recordDelayedXSearchInterruption("session/cancel")
	return nil
}

func (a *delayedXSearchAgent) SessionConfigOptions(context.Context, session.Session) ([]acp.SessionConfigOption, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.configOptionsLocked(""), nil
}

func (a *delayedXSearchAgent) SetSessionConfigOption(_ context.Context, req acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	configID := strings.TrimSpace(req.ConfigID)
	value, ok := req.Value.(string)
	if sessionID == "" || !ok {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("acpe2eagent: invalid config update")
	}
	value = strings.TrimSpace(value)
	if !validDelayedXSearchConfig(configID, value) {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("acpe2eagent: invalid value %q for config %q", value, configID)
	}
	a.mu.Lock()
	if a.current[sessionID] == nil {
		a.current[sessionID] = map[string]string{"model": "sonnet", "effort": "high"}
	}
	a.current[sessionID][configID] = value
	options := a.configOptionsLocked(sessionID)
	a.mu.Unlock()
	return acp.SetSessionConfigOptionResponse{ConfigOptions: options}, nil
}

func (a *delayedXSearchAgent) configOptionsLocked(sessionID string) []acp.SessionConfigOption {
	current := a.current[sessionID]
	modelID := "sonnet"
	effort := "high"
	if current != nil {
		modelID = firstNonEmpty(current["model"], modelID)
		effort = firstNonEmpty(current["effort"], effort)
	}
	return []acp.SessionConfigOption{
		{
			Type: "select", ID: "model", Name: "Model", Category: "model", CurrentValue: modelID,
			Options: []acp.SessionConfigSelectOption{{Value: "sonnet", Name: "Sonnet"}, {Value: "opus", Name: "Opus"}},
		},
		{
			Type: "select", ID: "effort", Name: "Reasoning effort", Category: "reasoning", CurrentValue: effort,
			Options: []acp.SessionConfigSelectOption{{Value: "high", Name: "High"}, {Value: "max", Name: "Max"}},
		},
	}
}

func validDelayedXSearchConfig(configID string, value string) bool {
	switch configID {
	case "model":
		return value == "sonnet" || value == "opus"
	case "effort":
		return value == "high" || value == "max"
	default:
		return false
	}
}

func recordDelayedXSearchInterruption(reason string) {
	path := strings.TrimSpace(os.Getenv("SDK_ACP_WATCHDOG_PROBE_PATH"))
	if path == "" {
		return
	}
	_ = os.WriteFile(path, []byte(strings.TrimSpace(reason)+"\n"), 0o600)
}
