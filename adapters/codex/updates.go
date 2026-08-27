package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	acp "github.com/caelis-labs/acp-go-sdk"

	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

type terminalOutputMode uint8

const (
	terminalOutputLegacy terminalOutputMode = iota
	terminalOutputCanonical
)

func terminalOutputModeForCapabilities(capabilities acp.ClientCapabilities) terminalOutputMode {
	var supported bool
	if json.Unmarshal(capabilities.Meta[metautil.TerminalOutputKey], &supported) == nil && supported {
		return terminalOutputCanonical
	}
	return terminalOutputLegacy
}

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
	CommandActions   []commandAction   `json:"commandActions"`
	CWD              string            `json:"cwd"`
	AggregatedOutput string            `json:"aggregatedOutput"`
	ExitCode         *int              `json:"exitCode"`
	Status           string            `json:"status"`
	Changes          []struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	} `json:"changes"`
	Server            string         `json:"server"`
	Tool              string         `json:"tool"`
	Arguments         map[string]any `json:"arguments"`
	Prompt            string         `json:"prompt"`
	SenderThreadID    string         `json:"senderThreadId"`
	ReceiverThreadIDs []string       `json:"receiverThreadIds"`
	AgentsStates      any            `json:"agentsStates"`
	Model             string         `json:"model"`
	ReasoningEffort   string         `json:"reasoningEffort"`
	Result            any            `json:"result"`
	Error             any            `json:"error"`
	Query             string         `json:"query"`
	Path              string         `json:"path"`
	Duration          int64          `json:"durationMs"`
}

type commandAction struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Query   string `json:"query"`
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

func liveItemCompleted(
	threadID string,
	raw json.RawMessage,
	alreadyStreamed, toolStarted, toolOutputStreamed bool,
	outputMode terminalOutputMode,
) ([]acp.SessionUpdate, *terminalNotification, error) {
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
			return []acp.SessionUpdate{toolComplete(threadID, item, toolStarted, toolOutputStreamed, outputMode)}, nil, nil
		}
	}
	return nil, nil, nil
}

func toolStart(threadID string, item threadItem) acp.SessionUpdate {
	kind, title, locations, rawInput := toolPresentation(item)
	update := acp.StartToolCall(
		acp.ToolCallId(stableID(threadID, item.ID, "tool")), title,
		acp.WithStartKind(kind), acp.WithStartStatus(acp.ToolCallStatusInProgress),
		acp.WithStartLocations(locations), acp.WithStartRawInput(rawInput),
	)
	if commandExecutionUsesTerminal(item) {
		terminalID := stableID(threadID, item.ID, "tool")
		update.ToolCall.Content = []acp.ToolCallContent{acp.ToolTerminalRef(terminalID)}
		update.ToolCall.Meta = encodeMeta(terminalInfoMeta(terminalID, item.CWD))
	}
	return update
}

func toolComplete(
	threadID string,
	item threadItem,
	started, outputStreamed bool,
	outputMode terminalOutputMode,
) acp.SessionUpdate {
	status := acp.ToolCallStatusCompleted
	if item.Status == "failed" || item.Status == "declined" || item.Error != nil || item.ExitCode != nil && *item.ExitCode != 0 {
		status = acp.ToolCallStatusFailed
	}
	rawOutput := item.Result
	if item.Type == "commandExecution" {
		rawOutput = map[string]any{
			"formatted_output": item.AggregatedOutput,
			"exit_code":        item.ExitCode,
		}
	} else if strings.TrimSpace(item.AggregatedOutput) != "" {
		rawOutput = map[string]any{"formatted_output": item.AggregatedOutput, "exit_code": item.ExitCode}
	}
	if item.Type != "commandExecution" && item.Error != nil {
		rawOutput = item.Error
	}
	toolID := acp.ToolCallId(stableID(threadID, item.ID, "tool"))
	var update acp.SessionUpdate
	if started {
		update = acp.UpdateToolCall(toolID,
			acp.WithUpdateStatus(status), acp.WithUpdateRawOutput(rawOutput),
		)
	} else {
		kind, title, locations, rawInput := toolPresentation(item)
		update = acp.StartToolCall(toolID, title,
			acp.WithStartKind(kind), acp.WithStartStatus(status),
			acp.WithStartLocations(locations), acp.WithStartRawInput(rawInput),
			acp.WithStartRawOutput(rawOutput),
		)
	}
	if commandExecutionUsesTerminal(item) {
		terminalID := stableID(threadID, item.ID, "tool")
		meta := metautil.WithTerminalExit(nil, terminalID, item.ExitCode, nil)
		if !started {
			meta = terminalInfoMetaWithBase(meta, terminalID, item.CWD)
			update.ToolCall.Content = []acp.ToolCallContent{acp.ToolTerminalRef(terminalID)}
		}
		if (!outputStreamed || !started) && item.AggregatedOutput != "" {
			meta = withTerminalOutput(meta, outputMode, terminalID, item.AggregatedOutput)
		}
		if started {
			update.ToolCallUpdate.Meta = encodeMeta(meta)
		} else {
			update.ToolCall.Meta = encodeMeta(meta)
		}
	}
	return update
}

func toolPresentation(item threadItem) (acp.ToolKind, string, []acp.ToolCallLocation, any) {
	switch item.Type {
	case "commandExecution":
		if len(item.CommandActions) == 1 {
			action := item.CommandActions[0]
			switch action.Type {
			case "read":
				locations := []acp.ToolCallLocation(nil)
				if strings.TrimSpace(action.Path) != "" {
					locations = []acp.ToolCallLocation{{Path: action.Path}}
				}
				return acp.ToolKindRead, fmt.Sprintf("Read file '%s'", action.Path), locations, nil
			case "search":
				return acp.ToolKindSearch, searchTitle(action.Query, action.Path), nil, nil
			case "listFiles":
				title := "List files"
				if strings.TrimSpace(action.Path) != "" {
					title = fmt.Sprintf("List files in '%s'", action.Path)
				}
				return acp.ToolKindRead, title, nil, nil
			case "unknown":
				command := firstNonEmpty(action.Command, item.Command)
				return acp.ToolKindExecute, firstNonEmpty(stripShellPrefix(command), "Run command"), nil,
					map[string]any{"command": command, "cwd": item.CWD}
			}
		}
		return acp.ToolKindExecute, firstNonEmpty(stripShellPrefix(item.Command), "Run command"), nil,
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
		action := strings.ToLower(strings.TrimSpace(item.Tool))
		input := map[string]any{
			"action": action, "prompt": item.Prompt,
			"senderThreadId": item.SenderThreadID, "receiverThreadIds": item.ReceiverThreadIDs,
			"agentsStates": item.AgentsStates, "model": item.Model,
			"reasoningEffort": item.ReasoningEffort,
		}
		if action == "wait" {
			input["target_kind"] = "subagent"
		}
		return acp.ToolKindOther, firstNonEmpty(item.Tool, "Agent collaboration"), nil, input
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
	parts := make([]string, 0, len(item.Summary))
	for _, value := range item.Summary {
		if value = strings.TrimSpace(value); value != "" {
			parts = append(parts, value)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n\n")
	}
	parts = make([]string, 0, len(item.Content))
	for _, raw := range item.Content {
		var text string
		if json.Unmarshal(raw, &text) == nil && strings.TrimSpace(text) != "" {
			parts = append(parts, strings.TrimSpace(text))
			continue
		}
		var value struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value.Text) != "" {
			parts = append(parts, strings.TrimSpace(value.Text))
		}
	}
	return strings.Join(parts, "\n\n")
}

func commandExecutionUsesTerminal(item threadItem) bool {
	return item.Type == "commandExecution" &&
		(len(item.CommandActions) != 1 || item.CommandActions[0].Type == "unknown")
}

func stripShellPrefix(command string) string {
	command = strings.TrimSpace(command)
	for _, shell := range []string{"/bin/bash", "/bin/zsh", "/bin/sh", "bash", "zsh", "sh"} {
		prefix := shell + " "
		if !strings.HasPrefix(command, prefix) {
			continue
		}
		command = strings.TrimSpace(strings.TrimPrefix(command, shell))
		for _, flag := range []string{"-lc", "-cl", "-l", "-c"} {
			if strings.HasPrefix(command, flag+" ") {
				command = strings.TrimSpace(strings.TrimPrefix(command, flag))
				break
			}
		}
		break
	}
	if len(command) >= 2 && (command[0] == '\'' && command[len(command)-1] == '\'' ||
		command[0] == '"' && command[len(command)-1] == '"') {
		command = command[1 : len(command)-1]
	}
	return command
}

func searchTitle(query, path string) string {
	query = strings.TrimSpace(query)
	path = strings.TrimSpace(path)
	switch {
	case query != "" && path != "":
		return fmt.Sprintf("Search for '%s' in %s", query, path)
	case query != "":
		return fmt.Sprintf("Search for '%s'", query)
	case path != "":
		return fmt.Sprintf("Search in '%s'", path)
	default:
		return "Search"
	}
}

func encodeMeta(values map[string]any) map[string]json.RawMessage {
	meta := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		raw, err := json.Marshal(value)
		if err == nil {
			meta[key] = raw
		}
	}
	return meta
}

func terminalInfoMeta(terminalID, cwd string) map[string]any {
	return terminalInfoMetaWithBase(nil, terminalID, cwd)
}

func terminalInfoMetaWithBase(meta map[string]any, terminalID, cwd string) map[string]any {
	meta = metautil.WithTerminalInfo(meta, terminalID)
	if info, ok := meta[metautil.TerminalInfoKey].(map[string]any); ok && strings.TrimSpace(cwd) != "" {
		info["cwd"] = cwd
	}
	return meta
}

func withTerminalOutput(meta map[string]any, mode terminalOutputMode, terminalID, data string) map[string]any {
	out := metautil.WithTerminalOutput(meta, terminalID, data)
	if mode == terminalOutputCanonical {
		return out
	}
	output, ok := out[metautil.TerminalOutputKey]
	delete(out, metautil.TerminalOutputKey)
	if ok {
		out[metautil.TerminalOutputDeltaKey] = output
	}
	return out
}
