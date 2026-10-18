package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

// Run with CAELIS_DTW_CANDIDATE_ROOT pointed at an independently verified
// Desktop World standard Plugin archive. The normal test suite stays self-contained.
func TestDesktopWorldStandardCandidateInstallAndProtocol(t *testing.T) {
	root := os.Getenv("CAELIS_DTW_CANDIDATE_ROOT")
	if root == "" {
		t.Skip("set CAELIS_DTW_CANDIDATE_ROOT to a verified standard Plugin root")
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
	execTool := all["desktop_world__exec"]
	if execTool == nil || len(all) != 1 {
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
	if err := json.Unmarshal(found.Content[0].JSON.Value, &discovery); err != nil || discovery.Count != 1 || len(discovery.Tools) != 1 {
		t.Fatalf("ToolSearch discovery = %+v, %v", discovery, err)
	}

	invoke := func(candidate tool.Tool, input string) string {
		t.Helper()
		result, callErr := candidate.Call(ctx, tool.Call{ID: "candidate", Name: candidate.Definition().Name, Input: []byte(input)})
		if callErr != nil || result.IsError || len(result.Content) == 0 || result.Content[0].Text == nil {
			t.Fatalf("exec = %+v, %v", result, callErr)
		}
		text := result.Content[0].Text.Text
		fields := strings.Fields(text)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "id=") {
			t.Fatalf("missing receipt: %s", text)
		}
		id := strings.TrimPrefix(fields[0], "id=")
		for strings.Contains(text, "status=running") {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(25 * time.Millisecond):
			}
			result, callErr = candidate.Call(ctx, tool.Call{ID: "poll", Name: candidate.Definition().Name, Input: []byte(fmt.Sprintf(`{"operation":"result","id":%q}`, id))})
			if callErr != nil || result.IsError || len(result.Content) == 0 || result.Content[0].Text == nil {
				t.Fatalf("result = %+v, %v", result, callErr)
			}
			text = result.Content[0].Text.Text
		}
		return text
	}
	for i := 1; i <= 2; i++ {
		text := invoke(execTool, `{"script":"state.n=(state.n||0)+1; return state.n;"}`)
		if !strings.Contains(text, "status=completed") || !strings.Contains(text, fmt.Sprintf("return value: %d", i)) {
			t.Fatalf("state %d: %s", i, text)
		}
	}
	independent, err := mcp.NewManager(ctx, contributions.MCPServerSpecs, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer independent.Close()
	select {
	case <-independent.Initialized():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	isolated := independent.Tools()
	if len(isolated) != 1 {
		t.Fatalf("independent tools = %v", isolated)
	}
	text := invoke(isolated[0], `{"script":"state.n=(state.n||0)+1; return state.n;"}`)
	if !strings.Contains(text, "return value: 1") {
		t.Fatalf("state crossed runtime boundary: %s", text)
	}

}

// The selector is deterministic test plumbing. Production continues to use
// the restricted model selector; this checks actual candidate definitions.
type dtwCandidateRanker struct{}

func (dtwCandidateRanker) Rank(_ context.Context, _ string, definitions []tool.Definition, _ int, _ toolsearch.SearchModel) ([]string, error) {
	var names []string
	for _, definition := range definitions {
		if definition.Name == "desktop_world__exec" {
			names = append(names, definition.Name)
		}
	}
	return names, nil
}
