package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	acp "github.com/caelis-labs/acp-go-sdk"
)

func stableID(threadID, itemID, role string) string {
	sum := sha256.Sum256([]byte("caelis-codex-acp-id-v1\x00" + threadID + "\x00" + itemID + "\x00" + role))
	return "codex-" + hex.EncodeToString(sum[:16])
}

type threadItem struct {
	ID               string            `json:"id"`
	Type             string            `json:"type"`
	Text             string            `json:"text"`
	Content          []json.RawMessage `json:"content"`
	Summary          []string          `json:"summary"`
	Command          string            `json:"command"`
	CWD              string            `json:"cwd"`
	AggregatedOutput string            `json:"aggregatedOutput"`
	ExitCode         *int              `json:"exitCode"`
	Status           string            `json:"status"`
	Changes          []struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	} `json:"changes"`
	Server    string         `json:"server"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
	Result    any            `json:"result"`
	Error     any            `json:"error"`
	Query     string         `json:"query"`
	Path      string         `json:"path"`
	Duration  int64          `json:"durationMs"`
}

func liveItemStarted(threadID string, raw json.RawMessage) ([]acp.SessionUpdate, *terminalNotification, error) {
	var item threadItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, nil, fmt.Errorf("codex adapter: decode started item: %w", err)
	}
	if !isToolItem(item.Type) {
		return nil, nil, nil
	}
	return []acp.SessionUpdate{toolStart(threadID, item)}, nil, nil
}

func liveItemCompleted(threadID string, raw json.RawMessage, alreadyStreamed bool) ([]acp.SessionUpdate, *terminalNotification, error) {
	var item threadItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, nil, fmt.Errorf("codex adapter: decode completed item: %w", err)
	}
	switch item.Type {
	case "agentMessage":
		if alreadyStreamed || item.Text == "" {
			return nil, nil, nil
		}
		update := acp.UpdateAgentMessageText(item.Text)
		id := acp.MessageId(stableID(threadID, item.ID, "agent-message"))
		update.AgentMessageChunk.MessageId = &id
		return []acp.SessionUpdate{update}, nil, nil
	case "reasoning":
		if alreadyStreamed {
			return nil, nil, nil
		}
		text := reasoningText(item)
		if text == "" {
			return nil, nil, nil
		}
		update := acp.UpdateAgentThoughtText(text)
		id := acp.MessageId(stableID(threadID, item.ID, "reasoning"))
		update.AgentThoughtChunk.MessageId = &id
		return []acp.SessionUpdate{update}, nil, nil
	default:
		if isToolItem(item.Type) {
			return []acp.SessionUpdate{toolComplete(threadID, item)}, nil, nil
		}
	}
	return nil, nil, nil
}

func toolStart(threadID string, item threadItem) acp.SessionUpdate {
	kind, title, locations, rawInput := toolPresentation(item)
	return acp.StartToolCall(
		acp.ToolCallId(stableID(threadID, item.ID, "tool")), title,
		acp.WithStartKind(kind), acp.WithStartStatus(acp.ToolCallStatusInProgress),
		acp.WithStartLocations(locations), acp.WithStartRawInput(rawInput),
	)
}

func toolComplete(threadID string, item threadItem) acp.SessionUpdate {
	status := acp.ToolCallStatusCompleted
	if item.Status == "failed" || item.Status == "declined" || item.Error != nil || item.ExitCode != nil && *item.ExitCode != 0 {
		status = acp.ToolCallStatusFailed
	}
	_, title, locations, _ := toolPresentation(item)
	rawOutput := item.Result
	if strings.TrimSpace(item.AggregatedOutput) != "" {
		rawOutput = item.AggregatedOutput
	}
	if item.Error != nil {
		rawOutput = item.Error
	}
	return acp.UpdateToolCall(
		acp.ToolCallId(stableID(threadID, item.ID, "tool")),
		acp.WithUpdateTitle(title), acp.WithUpdateStatus(status),
		acp.WithUpdateLocations(locations), acp.WithUpdateRawOutput(rawOutput),
	)
}

func toolPresentation(item threadItem) (acp.ToolKind, string, []acp.ToolCallLocation, any) {
	switch item.Type {
	case "commandExecution":
		return acp.ToolKindExecute, firstNonEmpty(item.Command, "Run command"), nil,
			map[string]any{"command": item.Command, "cwd": item.CWD}
	case "fileChange":
		locations := make([]acp.ToolCallLocation, 0, len(item.Changes))
		for _, change := range item.Changes {
			if strings.TrimSpace(change.Path) != "" {
				locations = append(locations, acp.ToolCallLocation{Path: change.Path})
			}
		}
		return acp.ToolKindEdit, "Apply file changes", locations, item.Changes
	case "mcpToolCall":
		return acp.ToolKindOther, firstNonEmpty(item.Server+" / "+item.Tool, "MCP tool"), nil, item.Arguments
	case "dynamicToolCall":
		return acp.ToolKindOther, firstNonEmpty(item.Tool, "Tool call"), nil, item.Arguments
	case "collabAgentToolCall":
		return acp.ToolKindOther, firstNonEmpty(item.Tool, "Agent collaboration"), nil, item.Arguments
	case "webSearch":
		return acp.ToolKindSearch, firstNonEmpty(item.Query, "Web search"), nil, map[string]any{"query": item.Query}
	case "imageView":
		locations := []acp.ToolCallLocation{}
		if filepath.IsAbs(item.Path) {
			locations = append(locations, acp.ToolCallLocation{Path: item.Path})
		}
		return acp.ToolKindRead, firstNonEmpty(item.Path, "View image"), locations, map[string]any{"path": item.Path}
	case "sleep":
		return acp.ToolKindOther, "Wait", nil, map[string]any{"durationMs": item.Duration}
	case "imageGeneration":
		return acp.ToolKindOther, "Generate image", nil, nil
	default:
		return acp.ToolKindOther, firstNonEmpty(item.Type, "Codex tool"), nil, item.Arguments
	}
}

func isToolItem(itemType string) bool {
	switch itemType {
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "webSearch", "imageView", "sleep", "imageGeneration":
		return true
	default:
		return false
	}
}

func reasoningText(item threadItem) string {
	parts := make([]string, 0, len(item.Summary)+len(item.Content))
	for _, value := range item.Summary {
		if value = strings.TrimSpace(value); value != "" {
			parts = append(parts, value)
		}
	}
	for _, raw := range item.Content {
		var value struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value.Text) != "" {
			parts = append(parts, value.Text)
		}
	}
	return strings.Join(parts, "\n")
}
