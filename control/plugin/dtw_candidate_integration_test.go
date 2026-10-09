package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

// Run with CAELIS_DTW_CANDIDATE_ROOT pointed at an independently verified
// Desktop World Full archive. The normal test suite stays self-contained.
func TestDesktopWorldStandardCandidateInstallAndProtocol(t *testing.T) {
	root := os.Getenv("CAELIS_DTW_CANDIDATE_ROOT")
	if root == "" {
		t.Skip("set CAELIS_DTW_CANDIDATE_ROOT to a verified Full archive root")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	host := &memoryHost{dir: t.TempDir()}
	service := NewService(host)
	installed, err := service.Install(ctx, root)
	if err != nil || !installed.Enabled || installed.Name != "desktop-world" || len(installed.Skills) == 0 {
		t.Fatalf("standard Install = %+v, %v", installed, err)
	}
	contributions, err := ResolveContributions(host.state.Plugins, RuntimePaths{StoreDir: host.dir})
	if err != nil || len(contributions.MCPServerSpecs) != 1 || len(contributions.SkillBundles) != 1 {
		t.Fatalf("standard contributions = %+v, %v", contributions, err)
	}
	mgr, err := mcp.NewManager(ctx, contributions.MCPServerSpecs, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	select {
	case <-mgr.Initialized():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	all := map[string]tool.Tool{}
	for _, candidate := range mgr.Tools() {
		all[candidate.Definition().Name] = candidate
	}
	execTool, statusTool := all["desktop_world__desktop_exec"], all["desktop_world__desktop_status"]
	if execTool == nil || statusTool == nil {
		t.Fatalf("candidate tools = %v; status = %+v", all, mgr.GetServerInfos(installed.ID))
	}
	search := toolsearch.NewWithRanker(mgr.Tools(), dtwCandidateRanker{})
	if search == nil {
		t.Fatal("DTW MCP tools were absent from ToolSearch catalog")
	}
	found, err := search.Call(ctx, tool.Call{ID: "search", Name: tool.ToolSearchToolName, Input: []byte(`{"query":"Desktop World tools","limit":2}`)})
	if err != nil || len(found.Content) != 1 || found.Content[0].JSON == nil {
		t.Fatalf("ToolSearch = %+v, %v", found, err)
	}
	var discovery struct {
		Count int `json:"count"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(found.Content[0].JSON.Value, &discovery); err != nil || discovery.Count != 2 || len(discovery.Tools) != 2 {
		t.Fatalf("ToolSearch discovery = %+v, %v", discovery, err)
	}
	status, err := statusTool.Call(ctx, tool.Call{ID: "status-first", Name: statusTool.Definition().Name, Input: []byte(`{}`)})
	if err != nil || status.IsError {
		t.Fatalf("desktop_status = %+v, %v", status, err)
	}
	for i := 1; i <= 2; i++ {
		input := fmt.Sprintf(`{"execution_id":"core112-%d","code":"state.core112 = (state.core112 || 0) + 1; print(state.core112);"}`, i)
		result, err := execTool.Call(ctx, tool.Call{ID: fmt.Sprintf("call-%d", i), Name: execTool.Definition().Name, Input: []byte(input)})
		body, _ := json.Marshal(result)
		if err != nil || result.IsError || result.ID != fmt.Sprintf("call-%d", i) {
			t.Fatalf("desktop_exec %d = %s, %v", i, body, err)
		}
		if len(result.Content) != 2 || result.Content[0].Text == nil || result.Content[1].JSON == nil {
			t.Fatalf("desktop_exec %d lost text or structuredContent: %s", i, body)
		}
		var structured struct {
			ExecutionID string `json:"execution_id"`
			State       string `json:"state"`
			Outputs     []int  `json:"outputs"`
		}
		if err := json.Unmarshal(result.Content[1].JSON.Value, &structured); err != nil || structured.ExecutionID != fmt.Sprintf("core112-%d", i) || structured.State != "completed" || len(structured.Outputs) != 1 || structured.Outputs[0] != i {
			t.Fatalf("desktop_exec %d did not preserve state: %s, %v", i, body, err)
		}
		status, err = statusTool.Call(ctx, tool.Call{ID: fmt.Sprintf("status-%d", i), Name: statusTool.Definition().Name, Input: []byte(`{}`)})
		if err != nil || status.IsError {
			t.Fatalf("desktop_status %d = %+v, %v", i, status, err)
		}
	}
}

// The selector is deterministic test plumbing. Production continues to use
// the restricted model selector; this checks actual candidate definitions.
type dtwCandidateRanker struct{}

func (dtwCandidateRanker) Rank(_ context.Context, _ string, definitions []tool.Definition, _ int, _ toolsearch.SearchModel) ([]string, error) {
	var names []string
	for _, definition := range definitions {
		if definition.Name == "desktop_world__desktop_exec" || definition.Name == "desktop_world__desktop_status" {
			names = append(names, definition.Name)
		}
	}
	return names, nil
}
