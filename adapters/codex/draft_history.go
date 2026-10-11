package codex

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
)

// draftHistoryUpdates keeps the existing stable-history barrier and workspace
// owner, while giving every replayed v2 message its native item identity.
func draftHistoryUpdates(thread historyThread, outputMode terminalOutputMode) ([]acp.SessionUpdate, error) {
	var result []acp.SessionUpdate
	for _, turn := range thread.Turns {
		for _, raw := range turn.Items {
			var item threadItem
			if err := json.Unmarshal(raw, &item); err != nil {
				return nil, err
			}
			var updates []acp.SessionUpdate
			if item.Type == "userMessage" {
				for _, content := range item.Content {
					block, err := draftUserContent(content)
					if err != nil {
						return nil, err
					}
					updates = append(updates, acp.UpdateUserMessage(block))
				}
			} else {
				var err error
				updates, err = historyItemUpdates(thread.ID, raw, outputMode)
				if err != nil {
					return nil, err
				}
			}
			if item.ID == "" {
				return nil, fmt.Errorf("codex adapter: draft history item lacks stable identity")
			}
			for i := range updates {
				if updates[i].UserMessageChunk != nil {
					updates[i].UserMessageChunk.MessageId = acp.Ptr(acp.MessageId(stableID(thread.ID, item.ID, "user-message")))
				}
				if updates[i].AgentMessageChunk != nil {
					updates[i].AgentMessageChunk.MessageId = acp.Ptr(acp.MessageId(stableID(thread.ID, item.ID, "agent-message")))
				}
				if updates[i].AgentThoughtChunk != nil {
					updates[i].AgentThoughtChunk.MessageId = acp.Ptr(acp.MessageId(stableID(thread.ID, item.ID, "reasoning")))
				}
			}
			result = append(result, updates...)
		}
	}
	return result, nil
}
func draftUserContent(raw json.RawMessage) (acp.ContentBlock, error) {
	var value struct {
		Type string `json:"type"`
		Name string `json:"name"`
		Text string `json:"text"`
		URL  string `json:"url"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return acp.ContentBlock{}, err
	}
	switch value.Type {
	case "text":
		return acp.TextBlock(value.Text), nil
	case "image":
		if strings.HasPrefix(value.URL, "data:") {
			mime, body, ok := strings.Cut(strings.TrimPrefix(value.URL, "data:"), ";base64,")
			if !ok {
				return acp.ContentBlock{}, fmt.Errorf("codex adapter: malformed native image data URL")
			}
			if _, err := base64.StdEncoding.DecodeString(body); err != nil {
				return acp.ContentBlock{}, err
			}
			return acp.ImageBlock(body, mime), nil
		}
		if value.URL != "" {
			return acp.ResourceLinkBlock("Image", value.URL), nil
		}
	case "localImage":
		if value.Path != "" {
			return acp.ResourceLinkBlock("Image", "file://"+value.Path), nil
		}
	case "skill", "mention":
		if value.Name != "" && value.Path != "" {
			// Native selections contain references, not copied file contents.
			// Keep app/mcp/skill/plugin URIs intact and escape local file paths.
			return acp.ResourceLinkBlock(value.Name, draftNativeReferenceURI(value.Path)), nil
		}
		if value.Text != "" {
			return acp.TextBlock(value.Text), nil
		}
	}
	return acp.ContentBlock{}, fmt.Errorf("codex adapter: unsupported draft user content %q", value.Type)
}

// draftNativeReferenceURI recognizes native absolute path syntax independently
// of the adapter's OS. It neither resolves a relative reference nor reads its
// target. Drive letters are URI path components; UNC servers are authorities.
func draftNativeReferenceURI(path string) string {
	if len(path) >= 3 && path[1] == ':' && (path[2] == '\\' || path[2] == '/') &&
		(path[0] >= 'A' && path[0] <= 'Z' || path[0] >= 'a' && path[0] <= 'z') {
		return (&url.URL{Scheme: "file", Path: "/" + strings.ReplaceAll(path, `\`, "/")}).String()
	}
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		host, share, ok := strings.Cut(strings.ReplaceAll(path[2:], `\`, "/"), "/")
		if ok && host != "" && share != "" {
			return (&url.URL{Scheme: "file", Host: host, Path: "/" + share}).String()
		}
	}
	if strings.HasPrefix(path, "/") {
		return (&url.URL{Scheme: "file", Path: path}).String()
	}
	return path
}

// acceptHistoryState runs behind the existing native replay barrier, before
// post-barrier live notifications. Resume cannot resurrect a stale running Turn.
func (d *draftAgent) acceptHistoryState(route *sessionRoute, thread historyThread) error {
	id, status := "", "completed"
	if n := len(thread.Turns); n > 0 {
		id = thread.Turns[n-1].ID
		status = thread.Turns[n-1].Status
	}
	session := d.session(route.state.threadID)
	session.pubMu.Lock()
	defer session.pubMu.Unlock()
	session.mu.Lock()
	session.cancelRequested = false
	session.mu.Unlock()
	route.state.mu.Lock()
	update := draftTerminalState(status, nil)
	if status == "inProgress" || status == "in_progress" {
		route.state.activeTurnID = id
		update = v2.RunningUpdate()
	} else {
		route.state.activeTurnID = ""
		session.mu.Lock()
		session.terminalTurn = id
		session.mu.Unlock()
	}
	route.state.mu.Unlock()
	return d.sendStateUnlocked(route.state.threadID, id, update)
}
