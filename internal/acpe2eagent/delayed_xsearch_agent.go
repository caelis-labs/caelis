package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	acp "github.com/caelis-labs/caelis/protocol/acp/schema"
	surfaceacp "github.com/caelis-labs/caelis/surfaces/acp"
)

// delayedXSearchAgent is an external wire-level ACP fixture. Its repeated
// start payloads deliberately resemble the former Control-watchdog false
// positive while its completed payloads carry distinct query arguments.
type delayedXSearchAgent struct {
	mu      sync.Mutex
	current map[string]map[string]string
}

func newDelayedXSearchAgent() *delayedXSearchAgent {
	return &delayedXSearchAgent{current: map[string]map[string]string{}}
}

func (a *delayedXSearchAgent) Initialize(context.Context, acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	return acpsdk.InitializeResponse{
		ProtocolVersion:   acpsdk.ProtocolVersionNumber,
		AgentCapabilities: acpsdk.AgentCapabilities{},
		AgentInfo: &acpsdk.Implementation{
			Name:    "caelis-acp-wire-e2e",
			Title:   acpsdk.Ptr("Caelis ACP Wire E2E Agent"),
			Version: "0.1.0",
		},
		AuthMethods: []acpsdk.AuthMethod{},
	}, nil
}

func (a *delayedXSearchAgent) NewSession(context.Context, acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	sessionID := newSessionID()
	a.mu.Lock()
	a.current[sessionID] = map[string]string{"model": "sonnet", "effort": "high"}
	options := a.configOptionsLocked(sessionID)
	a.mu.Unlock()
	return acpsdk.NewSessionResponse{SessionId: acpsdk.SessionId(sessionID), ConfigOptions: options}, nil
}

func (a *delayedXSearchAgent) Prompt(ctx context.Context, req acp.PromptRequest, callbacks surfaceacp.PromptCallbacks) (acp.PromptResponse, error) {
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

func (*delayedXSearchAgent) Cancel(context.Context, acpsdk.CancelNotification) error {
	recordDelayedXSearchInterruption("session/cancel")
	return nil
}

func (a *delayedXSearchAgent) SessionConfigOptions(context.Context, session.Session) ([]acpsdk.SessionConfigOption, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.configOptionsLocked(""), nil
}

func (a *delayedXSearchAgent) SetSessionConfigOption(_ context.Context, req acpsdk.SetSessionConfigOptionRequest) (acpsdk.SetSessionConfigOptionResponse, error) {
	if req.ValueId == nil {
		return acpsdk.SetSessionConfigOptionResponse{}, fmt.Errorf("acpe2eagent: invalid config update")
	}
	sessionID := strings.TrimSpace(string(req.ValueId.SessionId))
	configID := strings.TrimSpace(string(req.ValueId.ConfigId))
	value := strings.TrimSpace(string(req.ValueId.Value))
	if sessionID == "" {
		return acpsdk.SetSessionConfigOptionResponse{}, fmt.Errorf("acpe2eagent: invalid config update")
	}
	if !validDelayedXSearchConfig(configID, value) {
		return acpsdk.SetSessionConfigOptionResponse{}, fmt.Errorf("acpe2eagent: invalid value %q for config %q", value, configID)
	}
	a.mu.Lock()
	if a.current[sessionID] == nil {
		a.current[sessionID] = map[string]string{"model": "sonnet", "effort": "high"}
	}
	a.current[sessionID][configID] = value
	options := a.configOptionsLocked(sessionID)
	a.mu.Unlock()
	return acpsdk.SetSessionConfigOptionResponse{ConfigOptions: options}, nil
}

func (a *delayedXSearchAgent) configOptionsLocked(sessionID string) []acpsdk.SessionConfigOption {
	current := a.current[sessionID]
	modelID := "sonnet"
	effort := "high"
	if current != nil {
		modelID = firstNonEmpty(current["model"], modelID)
		effort = firstNonEmpty(current["effort"], effort)
	}
	return []acpsdk.SessionConfigOption{
		delayedConfigOption("model", "Model", "model", modelID, []acpsdk.SessionConfigSelectOption{
			{Value: "sonnet", Name: "Sonnet"}, {Value: "opus", Name: "Opus"},
		}),
		delayedConfigOption("effort", "Reasoning effort", "reasoning", effort, []acpsdk.SessionConfigSelectOption{
			{Value: "high", Name: "High"}, {Value: "max", Name: "Max"},
		}),
	}
}

func delayedConfigOption(id string, name string, category string, current string, choices []acpsdk.SessionConfigSelectOption) acpsdk.SessionConfigOption {
	ungrouped := acpsdk.SessionConfigSelectOptionsUngrouped(choices)
	typedCategory := acpsdk.SessionConfigOptionCategory(category)
	return acpsdk.SessionConfigOption{Select: &acpsdk.SessionConfigOptionSelect{
		Type: "select", Id: acpsdk.SessionConfigId(id), Name: name, Category: &typedCategory,
		CurrentValue: acpsdk.SessionConfigValueId(current),
		Options:      acpsdk.SessionConfigSelectOptions{Ungrouped: &ungrouped},
	}}
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
