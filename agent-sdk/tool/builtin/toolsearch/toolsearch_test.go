package toolsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestNewReturnsNilWithoutMCPTools(t *testing.T) {
	t.Parallel()

	plain := tool.NamedTool{Def: tool.Definition{Name: "READ"}}
	if got := New([]tool.Tool{plain}); got != nil {
		t.Fatalf("New(non-MCP) = %#v, want nil", got)
	}
}

func TestToolSearchFindsDeferredMCPTools(t *testing.T) {
	t.Parallel()

	searchTool := New([]tool.Tool{
		mcpCandidate("mcp__calendar__demo__create_event", "Create calendar events", "calendar", "demo", "create_event", map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"start_time": map[string]any{"type": "string", "description": "Event start time"},
				"attendees":  map[string]any{"type": "string"},
			},
		}),
		mcpCandidate("mcp__drive__demo__read_file", "Read drive files", "drive", "demo", "read_file", map[string]any{
			"type": "object",
		}),
	})
	if searchTool == nil {
		t.Fatal("New(MCP tools) = nil")
	}
	def := searchTool.Definition()
	if def.Name != tool.ToolSearchToolName {
		t.Fatalf("Definition.Name = %q, want %q", def.Name, tool.ToolSearchToolName)
	}
	if !strings.Contains(def.Description, "calendar/demo") || !strings.Contains(def.Description, "drive/demo") {
		t.Fatalf("Definition.Description = %q, want source list", def.Description)
	}

	result, err := searchTool.Call(context.Background(), tool.Call{
		Name:  tool.ToolSearchToolName,
		Input: []byte(`{"query":"attendees event","limit":1}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	var output map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &output); err != nil {
		t.Fatalf("decode result JSON: %v", err)
	}
	payload := tool.ParseToolSearchOutput(output)
	if payload.Count != 1 || len(payload.Tools) != 1 {
		t.Fatalf("payload count/tools = %d/%d, want 1/1", payload.Count, len(payload.Tools))
	}
	got := payload.Tools[0]
	if got.Type != "function" || got.Name != "mcp__calendar__demo__create_event" || !got.DeferLoading {
		t.Fatalf("returned tool = %+v, want deferred calendar function", got)
	}
	if got.Source["plugin_id"] != "calendar" || got.Source["mcp_server"] != "demo" || got.Source["mcp_tool"] != "create_event" {
		t.Fatalf("source = %#v, want MCP provenance", got.Source)
	}
}

func TestParseRequestRejectsOversizedQueryAndLimit(t *testing.T) {
	t.Parallel()

	if _, err := parseRequest([]byte(`{"query":"calendar","limit":17}`)); err == nil {
		t.Fatal("parseRequest(limit=17) succeeded")
	}
	if _, err := parseRequest([]byte(`{"query":"calendar","limit":0}`)); err == nil {
		t.Fatal("parseRequest(explicit limit=0) succeeded")
	}
	if got, err := parseRequest([]byte(`{"query":"calendar"}`)); err != nil || got.Limit != defaultLimit {
		t.Fatalf("parseRequest(omitted limit) = %#v, %v; want default %d", got, err, defaultLimit)
	}
	query := strings.Repeat("界", maxQueryRunes+1)
	raw, _ := json.Marshal(map[string]any{"query": query})
	if _, err := parseRequest(raw); err == nil {
		t.Fatal("parseRequest(oversized query) succeeded")
	}

	def := New([]tool.Tool{mcpCandidate("mcp__calendar__demo__read", "Read", "calendar", "demo", "read", map[string]any{"type": "object"})}).Definition()
	props := def.InputSchema["properties"].(map[string]any)
	if got := props["query"].(map[string]any)["maxLength"]; got != maxQueryRunes {
		t.Fatalf("query maxLength = %#v, want %d", got, maxQueryRunes)
	}
	if got := props["limit"].(map[string]any)["maximum"]; got != maxLimit {
		t.Fatalf("limit maximum = %#v, want %d", got, maxLimit)
	}
}

func TestToolSearchBoundsOneResultByProjectedPromptCost(t *testing.T) {
	t.Parallel()

	tools := make([]tool.Tool, 0, maxLimit)
	for i := 0; i < maxLimit; i++ {
		tools = append(tools, mcpCandidate(
			fmt.Sprintf("mcp__plugin__server__heavy_%02d", i),
			strings.Repeat("heavy metadata ", 400),
			"plugin",
			"server",
			fmt.Sprintf("heavy_%02d", i),
			map[string]any{"type": "object"},
		))
	}
	searchTool := New(tools)
	result, err := searchTool.Call(context.Background(), tool.Call{
		Name:  tool.ToolSearchToolName,
		Input: []byte(`{"query":"heavy","limit":16}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &output); err != nil {
		t.Fatal(err)
	}
	payload := tool.ParseToolSearchOutput(output)
	if !payload.Truncated || payload.OmittedCount == 0 {
		t.Fatalf("payload = %#v, want prompt-cost truncation", payload)
	}
	if tokens := tool.EstimateToolSearchResultPromptTokens(payload); tokens > tool.MaxToolSearchResultPromptTokens {
		t.Fatalf("result projected tokens = %d, want <= %d", tokens, tool.MaxToolSearchResultPromptTokens)
	}
}

func TestToolSearchBoundsProjectedSourceMetadataAndDescription(t *testing.T) {
	t.Parallel()

	oversized := strings.Repeat("界", maxSourceMetadataRunes+100)
	tools := make([]tool.Tool, 0, maxDescriptionSources+10)
	for index := 0; index < maxDescriptionSources+10; index++ {
		tools = append(tools, mcpCandidate(
			fmt.Sprintf("mcp__plugin__server__tool_%02d", index),
			"metadata probe",
			fmt.Sprintf("%02d-%s", index, oversized),
			oversized,
			oversized,
			map[string]any{"type": "object"},
		))
	}
	searchTool := New(tools)
	description := searchTool.Definition().Description
	if got := utf8.RuneCountInString(description); got > maxToolSearchDescriptionRunes {
		t.Fatalf("ToolSearch description runes = %d, want <= %d", got, maxToolSearchDescriptionRunes)
	}
	if !strings.Contains(description, "additional sources omitted") {
		t.Fatalf("ToolSearch description omitted marker missing: %q", description)
	}
	result, err := searchTool.Call(context.Background(), tool.Call{
		Name:  tool.ToolSearchToolName,
		Input: []byte(`{"query":"metadata","limit":16}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &output); err != nil {
		t.Fatal(err)
	}
	payload := tool.ParseToolSearchOutput(output)
	if tokens := tool.EstimateToolSearchResultPromptTokens(payload); tokens > tool.MaxToolSearchResultPromptTokens {
		t.Fatalf("ToolSearch result tokens = %d, want <= %d", tokens, tool.MaxToolSearchResultPromptTokens)
	}
	for _, discovered := range payload.Tools {
		for key, value := range discovered.Source {
			text, _ := value.(string)
			if got := utf8.RuneCountInString(text); got > maxSourceMetadataRunes {
				t.Fatalf("source[%s] runes = %d, want <= %d", key, got, maxSourceMetadataRunes)
			}
		}
	}
}

func mcpCandidate(name, description, pluginID, server, mcpTool string, schema map[string]any) tool.Tool {
	return tool.NamedTool{Def: tool.Definition{
		Name:        name,
		Description: description,
		InputSchema: schema,
		Metadata: map[string]any{
			tool.MetadataToolKind:  tool.MetadataToolKindMCP,
			tool.MetadataPluginID:  pluginID,
			tool.MetadataMCPServer: server,
			tool.MetadataMCPTool:   mcpTool,
		},
	}}
}
