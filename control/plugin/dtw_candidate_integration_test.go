package plugin

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

// This uses an independently verified platform package. Install and protocol
// checks do not establish Runtime approval or real desktop effects.
func TestDesktopWorldStandardCandidateInstallAndProtocol(t *testing.T) {
	root := os.Getenv("CAELIS_DTW_CANDIDATE_ROOT")
	if root == "" {
		t.Skip("set CAELIS_DTW_CANDIDATE_ROOT to a verified standard Plugin root")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	host := &memoryHost{dir: t.TempDir()}
	installed, err := NewService(host).Install(ctx, root)
	if err != nil || !installed.Enabled || installed.ID != "desktop-world" || installed.Name != "desktop-world" || len(installed.Skills) == 0 {
		t.Fatalf("standard Install = %+v, %v", installed, err)
	}
	contributions, err := ResolveContributions(host.state.Plugins, RuntimePaths{StoreDir: host.dir})
	if err != nil || len(contributions.MCPServerSpecs) != 1 || len(contributions.SkillBundles) != 1 {
		t.Fatalf("standard contributions = %+v, %v", contributions, err)
	}
	connect := func(t *testing.T) *mcp.Manager {
		t.Helper()
		manager, err := mcp.NewManager(ctx, contributions.MCPServerSpecs, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = manager.Close() })
		select {
		case <-manager.Initialized():
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		return manager
	}
	manager := connect(t)
	all := dtwCandidateTools(t, manager)
	var schema struct {
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}
	raw, _ := json.Marshal(all["exec"].Definition().InputSchema)
	if err := json.Unmarshal(raw, &schema); err != nil || !slices.Equal(schema.Required, []string{"app", "source"}) || len(schema.Properties) != 2 || schema.AdditionalProperties {
		t.Fatalf("exec must accept only app and source: %s, %v", raw, err)
	}
	apps := dtwCandidateInvoke(t, ctx, all["apps"], map[string]any{})
	var inventory []struct{ App, Name string }
	if err := json.Unmarshal(apps["apps"], &inventory); err != nil {
		t.Fatalf("apps inventory = %s, %v", apps["apps"], err)
	}
	api := dtwCandidateInvoke(t, ctx, all["api"], map[string]any{"methods": []string{"windows"}})
	var methods map[string]json.RawMessage
	if err := json.Unmarshal(api["methods"], &methods); err != nil || len(methods) != 1 || methods["windows"] == nil {
		t.Fatalf("selected api = %s, %v", api["methods"], err)
	}

	search := toolsearch.NewWithRanker(manager.Tools(), dtwCandidateRanker{})
	if search == nil {
		t.Fatal("DTW MCP tools were absent from ToolSearch catalog")
	}
	found, err := search.Call(ctx, tool.Call{ID: "search", Name: tool.ToolSearchToolName, Input: []byte(`{"query":"Desktop World tools","limit":2}`)})
	if err != nil || len(found.Content) != 1 || found.Content[0].JSON == nil {
		t.Fatalf("ToolSearch = %+v, %v", found, err)
	}
	var discovery struct {
		Count int                     `json:"count"`
		Tools []struct{ Name string } `json:"tools"`
	}
	if err := json.Unmarshal(found.Content[0].JSON.Value, &discovery); err != nil || discovery.Count != 1 || len(discovery.Tools) != 1 || discovery.Tools[0].Name != all["exec"].Definition().Name {
		t.Fatalf("ToolSearch discovery = %+v, %v", discovery, err)
	}

	t.Run("app state and original ledger", func(t *testing.T) {
		app := os.Getenv("CAELIS_DTW_TEST_APP")
		if app == "" {
			t.Skip("set CAELIS_DTW_TEST_APP to a running test app for state/ledger checks; no desktop action is performed")
		}
		input := map[string]any{"app": app, "source": "state.n=(state.n||0)+1; return state.n;"}
		first := dtwCandidateInvoke(t, ctx, all["exec"], input)
		dtwCandidateState(t, first, 1)
		dtwCandidateState(t, dtwCandidateInvoke(t, ctx, all["exec"], input), 2)
		var executionID string
		if err := json.Unmarshal(first["execution_id"], &executionID); err != nil || executionID == "" {
			t.Fatalf("missing execution ID: %s, %v", first["execution_id"], err)
		}
		dtwCandidateState(t, dtwCandidateInvoke(t, ctx, all["result"], map[string]any{"app": app, "execution_id": executionID}), 1)
		dtwCandidateState(t, dtwCandidateInvoke(t, ctx, all["exec"], input), 3)
		independent := dtwCandidateTools(t, connect(t))
		dtwCandidateState(t, dtwCandidateInvoke(t, ctx, independent["exec"], input), 1)
	})
}

func dtwCandidateTools(t *testing.T, manager *mcp.Manager) map[string]tool.Tool {
	t.Helper()
	all := map[string]tool.Tool{}
	for _, candidate := range manager.Tools() {
		remote, _ := candidate.Definition().Metadata[tool.MetadataMCPTool].(string)
		all[remote] = candidate
	}
	if len(all) != 4 || all["apps"] == nil || all["api"] == nil || all["exec"] == nil || all["result"] == nil {
		t.Fatalf("candidate tools = %v", all)
	}
	return all
}

func dtwCandidateInvoke(t *testing.T, ctx context.Context, candidate tool.Tool, input map[string]any) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := candidate.Call(ctx, tool.Call{ID: "candidate", Name: candidate.Definition().Name, Input: raw})
	if err != nil || result.IsError {
		detail, _ := json.Marshal(result)
		t.Fatalf("candidate %s = %s, %v", candidate.Definition().Name, detail, err)
	}
	for _, part := range result.Content {
		var raw []byte
		if part.JSON != nil {
			raw = part.JSON.Value
		} else if part.Text != nil {
			raw = []byte(part.Text.Text)
		}
		var data map[string]json.RawMessage
		if json.Unmarshal(raw, &data) == nil && data != nil {
			return data
		}
	}
	t.Fatalf("candidate returned no JSON object: %+v", result)
	return nil
}

func dtwCandidateState(t *testing.T, data map[string]json.RawMessage, want int) {
	t.Helper()
	var value struct {
		Result int `json:"result"`
		Calls  int `json:"calls"`
	}
	if err := json.Unmarshal(data["value"], &value); err != nil || value.Result != want || value.Calls != 0 || string(data["status"]) != `"completed"` {
		t.Fatalf("state %d: %+v, %v", want, data, err)
	}
}

// Production uses its restricted model selector; this checks the actual
// package's discoverable exec definition without another model dependency.
type dtwCandidateRanker struct{}

func (dtwCandidateRanker) Rank(ctx context.Context, _ string, definitions []tool.Definition, _ int, selected toolsearch.SearchModel) ([]string, error) {
	var names []string
	for _, definition := range definitions {
		schema, err := selected.ReadSchema(ctx, definition.Name)
		if err != nil {
			return nil, err
		}
		if schema.Metadata[tool.MetadataMCPTool] == "exec" {
			names = append(names, definition.Name)
		}
	}
	return names, nil
}
