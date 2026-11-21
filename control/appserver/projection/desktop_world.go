package projection

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/internal/eventmeta"
)

const desktopWorldDisplayKey = "desktop_world"

// DesktopWorldToolDisplay reads the display hint emitted for bound Desktop
// World calls. It has no bearing on tool identity or authorization.
func DesktopWorldToolDisplay(name string, meta map[string]any) string {
	method := eventmeta.String(meta, eventmeta.Root, "display", desktopWorldDisplayKey)
	if (method == "apps" || method == "exec") && (name == "dtw__"+method || name == "desktop_world__"+method) {
		return method
	}
	return ""
}

func desktopWorldDisplayMeta(method string) map[string]any {
	return map[string]any{eventmeta.Root: map[string]any{"display": map[string]any{desktopWorldDisplayKey: method}}}
}

func desktopWorldMethod(fact *session.EventTool) string {
	if fact.Binding == nil || fact.Binding.Name != fact.Name {
		return ""
	}
	for _, server := range []string{"dtw", "desktop-world"} {
		for _, method := range []string{"apps", "exec"} {
			if fact.Name != mcp.ServerToolNamePrefix(server)+method {
				continue
			}
			identity := mcp.BindingForDefinition(tool.Definition{Name: fact.Name, Metadata: map[string]any{
				mcp.MetadataKind: mcp.KindTool, mcp.MetadataPluginID: "desktop-world", mcp.MetadataServer: server,
				mcp.MetadataTool: method, mcp.MetadataSourceFingerprint: fact.Binding.Version,
			}})
			if identity == *fact.Binding {
				return method
			}
			return ""
		}
	}
	return ""
}

func desktopWorldTitle(fact *session.EventTool, method string) string {
	var raw json.RawMessage
	if fact.Result != nil {
		raw, _ = fact.Result.Structured()
	}
	if method == "apps" {
		return desktopWorldAppsTitle(fact, raw)
	}
	return desktopWorldExecTitle(fact, raw)
}

func desktopWorldAppsTitle(fact *session.EventTool, raw json.RawMessage) string {
	parts := []string{"Apps"}
	var result struct {
		Apps     []json.RawMessage
		Next     *int
		Coverage struct {
			Complete  *bool
			Installed *bool
		}
	}
	offset, _ := eventmeta.Int64(fact.Input, "offset")
	if failure := desktopWorldFailure(fact); failure != "" {
		parts = append(parts, failure)
	} else if json.Unmarshal(raw, &result) == nil && result.Apps != nil {
		limited := result.Coverage.Complete != nil && !*result.Coverage.Complete
		runningOnly := result.Coverage.Installed != nil && !*result.Coverage.Installed
		switch {
		case result.Next != nil || offset > 0:
			parts = append(parts, fmt.Sprintf("%d on page", len(result.Apps)))
		case len(result.Apps) == 0 && (limited || runningOnly):
			parts = append(parts, "0 in scope")
		case len(result.Apps) == 1:
			parts = append(parts, "1 match")
		default:
			parts = append(parts, fmt.Sprintf("%d matches", len(result.Apps)))
		}
		switch {
		case runningOnly:
			parts = append(parts, "running-only coverage")
		case limited:
			parts = append(parts, "limited coverage")
		}
		if result.Next != nil {
			parts = append(parts, "more available")
		}
	}
	if state, _ := fact.Input["state"].(string); state == "running" || state == "stopped" {
		parts = append(parts, state)
	}
	if query, _ := fact.Input["query"].(string); query != "" {
		parts = append(parts, fmt.Sprintf("%q", query))
	}
	if offset > 0 {
		parts = append(parts, "offset "+strconv.FormatInt(offset, 10))
	}
	if limit, ok := eventmeta.Int64(fact.Input, "limit"); ok && limit > 0 {
		parts = append(parts, "limit "+strconv.FormatInt(limit, 10))
	}
	return strings.Join(parts, " · ")
}

func desktopWorldFailure(fact *session.EventTool) string {
	if fact.Outcome == session.ToolOutcomeUnknown {
		return "outcome unknown"
	}
	if acpToolStatus(fact.Status) == eventstream.ToolStatusFailed {
		return "failed"
	}
	return ""
}

type desktopWorldExecResult struct {
	ExecutionID string `json:"execution_id"`
	App         string
	Effects     struct {
		Dispatch         map[string]int
		Interrupted      int
		Acknowledgements []struct{ Operation string }
	}
	Observation struct{ Status string }
	View        json.RawMessage
	Result      json.RawMessage
}

// Summarize ledger facts. JS source and arbitrary return values supply no
// evidence about which desktop operations ran.
func desktopWorldExecTitle(fact *session.EventTool, raw json.RawMessage) string {
	var data desktopWorldExecResult
	if json.Unmarshal(raw, &data) != nil || data.ExecutionID == "" {
		data = desktopWorldExecResult{}
	}
	parts := []string{"Exec"}
	for _, outcome := range []struct{ key, label string }{
		{"unknown", "unknown dispatch"}, {"not_dispatched", "not dispatched"},
	} {
		if count := data.Effects.Dispatch[outcome.key]; count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", count, outcome.label))
		}
	}
	if count := data.Effects.Interrupted; count > 0 {
		parts = append(parts, fmt.Sprintf("%d interrupted", count))
	}
	if failure := desktopWorldFailure(fact); failure != "" {
		parts = append(parts, failure)
	}
	switch data.Observation.Status {
	case "failed":
		parts = append(parts, "observation failed")
	case "partial":
		parts = append(parts, "partial observation")
	}
	var actions []string
	for _, acknowledgement := range data.Effects.Acknowledgements {
		// DTW copies the ledger operation (scene.<method>) into the acknowledgement.
		method, ok := strings.CutPrefix(acknowledgement.Operation, "scene.")
		if !ok {
			continue
		}
		switch method {
		case "click", "setValue", "move", "drag", "typeText", "keypress", "scroll", "focus", "quit":
			if !slices.Contains(actions, method) {
				actions = append(actions, method)
			}
		}
		if len(actions) == 3 {
			break
		}
	}
	if len(actions) > 0 {
		parts = append(parts, strings.Join(actions, ", "))
	}
	app, _ := fact.Input["app"].(string)
	if app := firstNonEmpty(app, data.App); app != "" {
		parts = append(parts, app)
	}
	if count := data.Effects.Dispatch["dispatched"]; count > 0 {
		parts = append(parts, fmt.Sprintf("%d dispatched", count))
	}
	switch {
	case len(data.View) > 0 && string(data.View) != "null":
		parts = append(parts, "view returned")
	case len(data.Result) > 0 || data.ExecutionID == "" && len(raw) > 0 && desktopWorldFailure(fact) == "":
		parts = append(parts, "result returned")
	}
	return strings.Join(parts, " · ")
}

// Keep the original reply available for disclosure, including JSON numbers
// that cannot round-trip through the legacy rawOutput map.
func desktopWorldReply(fact *session.EventTool) []eventstream.ToolCallContent {
	var out []eventstream.ToolCallContent
	if fact.Result != nil {
		parts := tool.ModelParts(fact.Result.Content)
		for i := range parts {
			if parts[i].JSON != nil {
				// JSON remains in its original precision, with indentation for
				// inspection in the terminal.
				var pretty bytes.Buffer
				if json.Indent(&pretty, parts[i].JSON.Value, "", "  ") == nil {
					parts[i].JSON.Value = pretty.Bytes()
				}
			}
		}
		out = projectToolResultParts(parts)
	}
	if errText := strings.TrimSpace(fact.Error); errText != "" {
		for _, item := range out {
			if content, ok := item.Content.(eventstream.TextContent); ok && strings.TrimSpace(content.Text) == errText {
				return out
			}
		}
		out = append(out, eventstream.ToolCallContent{
			Type: "content", Content: eventstream.TextContent{Type: "text", Text: errText},
		})
	}
	return out
}
