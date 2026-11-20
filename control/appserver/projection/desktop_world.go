package projection

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

func desktopWorldTitle(fact *session.EventTool) string {
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
			if identity != *fact.Binding {
				return ""
			}
			if method == "apps" {
				return fact.Name + " · Apps"
			}
			title := []string{fact.Name}
			if summary := desktopWorldActions(fact.Result); summary != "" {
				title = append(title, summary)
			}
			if app, ok := fact.Input["app"].(string); ok && app != "" {
				title = append(title, app)
			}
			return strings.Join(title, " · ")
		}
	}
	return ""
}

// The DTW MCP reply carries dispatch counts and selected acknowledgements
// from the execution ledger. Script source and arbitrary return values carry
// no evidence about which operations actually ran.
func desktopWorldActions(result *tool.Result) string {
	if result == nil {
		return ""
	}
	raw, ok := result.Structured()
	if !ok {
		return ""
	}
	var data struct {
		ExecutionID string `json:"execution_id"`
		Effects     struct {
			Dispatch         map[string]int
			Interrupted      int
			Acknowledgements []struct{ Operation string }
		}
	}
	if json.Unmarshal(raw, &data) != nil || data.ExecutionID == "" {
		return ""
	}
	var labels []string
	for _, acknowledgement := range data.Effects.Acknowledgements {
		switch acknowledgement.Operation {
		case "click", "setValue", "move", "drag", "typeText", "keypress", "scroll", "focus", "quit":
			labels = append(labels, acknowledgement.Operation)
		}
		if len(labels) == 3 {
			break
		}
	}
	for _, outcome := range []struct{ key, label string }{
		{"dispatched", "dispatched"}, {"unknown", "unknown"}, {"not_dispatched", "not dispatched"},
	} {
		if count := data.Effects.Dispatch[outcome.key]; count > 0 {
			labels = append(labels, fmt.Sprintf("%d %s", count, outcome.label))
		}
	}
	if count := data.Effects.Interrupted; count > 0 {
		labels = append(labels, fmt.Sprintf("%d interrupted", count))
	}
	return strings.Join(labels, ", ")
}
