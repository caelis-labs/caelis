package gatewayapp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/plugin"
)

// This opt-in uses a separately verified native package with synthetic model
// decisions. Only private per-connection state changes; no desktop API is called.
// Baseline Core and ordinary MCP tests never require this package or an app.
func TestDesktopWorldCandidateUsesFixedRealToolChain(t *testing.T) {
	candidate, app := os.Getenv("CAELIS_DTW_CANDIDATE_ROOT"), os.Getenv("CAELIS_DTW_TEST_APP")
	if candidate == "" || app == "" {
		t.Skip("requires an authorized candidate and running test app")
	}
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	contributions, err := plugin.ResolveContributions([]plugin.Config{{ID: "desktop-world", Root: candidate, Enabled: true}}, plugin.RuntimePaths{StoreDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(contributions.MCPServerSpecs) != 1 {
		t.Fatal("expected one configured Desktop World connection")
	}
	manager, err := mcp.NewManager(ctx, contributions.MCPServerSpecs, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	select {
	case <-manager.Initialized():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	names := map[string]string{}
	for _, one := range manager.SearchTools() {
		d := one.Definition()
		remote, _ := d.Metadata[tool.MetadataMCPTool].(string)
		names[remote] = d.Name
	}
	for _, name := range []string{"apps", "api", "exec", "result"} {
		if names[name] == "" {
			t.Fatalf("candidate missing %s", name)
		}
	}
	store := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "candidate-probe", Workspace: session.WorkspaceRef{Key: "candidate-probe", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := runtime.NewMCPGrantStore(filepath.Join(root, "grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(runtime.Config{Sessions: store, AgentFactory: chat.Factory{}, MCPGrants: grants, PolicyRegistry: liveMCPPolicy{allowed: map[string]bool{"apps": true, "api": true, "exec": true, "result": true}}, DefaultPolicyMode: "live-read-only"})
	if err != nil {
		t.Fatal(err)
	}
	probe := &dtwFixedProbe{names: names, app: app, t: t}
	approvals := 0
	result, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "probe private DTW state without desktop input", ApprovalRequester: liveApprovalFunc(func(_ context.Context, r agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		approvals++
		var input map[string]any
		_ = json.Unmarshal(r.Call.Input, &input)
		if r.Tool.Name != names["exec"] || r.Call.Name != r.Tool.Name || input["app"] != app || r.Call.ModelCall == nil || r.Approval == nil || len(r.Approval.Options) != 2 {
			return agent.ApprovalResponse{}, fmt.Errorf("approval lost exact real resource identity")
		}
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
	}), AgentSpec: agent.AgentSpec{Model: probe, Tools: []tool.Tool{toolsearch.NewSource(manager, dtwProbeRanker{})}, DeferredTools: manager}})
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Handle.WaitCompletion(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	completed := 0
	for _, e := range loaded.Events {
		if e.Type == session.EventTypeToolResult && e.Tool != nil && e.Tool.ModelCall != nil {
			completed++
			if e.Tool.Status != "completed" || e.Tool.Name == tool.MCPCallToolName || e.Tool.ModelCall.ID != e.Tool.ID {
				t.Fatalf("real-tool result failed: %+v", e.Tool)
			}
		}
	}
	if approvals != 1 || completed != 6 || probe.verified != 4 {
		t.Fatalf("approvals=%d completed=%d verified_state_results=%d", approvals, completed, probe.verified)
	}
	t.Logf("native candidate fixed route: actual_tools=6 exec=3 app_approvals=1 original_result=verified native_calls=0")
}

type dtwProbeRanker struct{}

func (dtwProbeRanker) Rank(_ context.Context, _ string, defs []tool.Definition, _ int, _ toolsearch.SearchModel) ([]string, error) {
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	return names, nil
}

type dtwFixedProbe struct {
	names       map[string]string
	app         string
	t           *testing.T
	step        int
	executionID string
	verified    int
	tools       []byte
}

func (*dtwFixedProbe) Name() string { return "synthetic-native-dtw-probe" }
func (*dtwFixedProbe) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true, Streaming: true}
}
func (p *dtwFixedProbe) Generate(_ context.Context, r *model.Request) iter.Seq2[*model.StreamEvent, error] {
	raw, _ := json.Marshal(r.Tools)
	if p.tools == nil {
		p.tools = raw
	} else if string(raw) != string(p.tools) {
		p.t.Fatal("native candidate discovery changed tools bytes")
	}
	switch p.step {
	case 4:
		p.checkState(r, "exec-1", 1)
	case 5:
		p.checkState(r, "exec-2", 2)
	case 6:
		p.checkState(r, "original-result", 1)
	case 7:
		p.checkState(r, "exec-3", 3)
	}
	step := p.step
	p.step++
	return func(yield func(*model.StreamEvent, error) bool) {
		call := model.ToolCall{ID: fmt.Sprintf("native-%d", step), Name: tool.MCPCallToolName}
		remote := ""
		input := map[string]any{}
		switch step {
		case 0:
			call.Name = tool.ToolSearchToolName
			call.Args = `{"query":"Desktop World tools","limit":4}`
		case 1:
			remote = "apps"
		case 2:
			remote = "api"
			input = map[string]any{"methods": []string{"windows"}}
		case 3, 4, 6:
			remote = "exec"
			call.ID = map[int]string{3: "exec-1", 4: "exec-2", 6: "exec-3"}[step]
			input = map[string]any{"app": p.app, "source": "state.n=(state.n||0)+1; return state.n;"}
		case 5:
			remote = "result"
			call.ID = "original-result"
			input = map[string]any{"app": p.app, "execution_id": p.executionID}
		default:
			yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, "probe complete"), Status: model.ResponseStatusCompleted, TurnComplete: true, StepComplete: true}), nil)
			return
		}
		if remote != "" {
			args, _ := json.Marshal(map[string]any{"tool_name": p.names[remote], "arguments": input})
			call.Args = string(args)
		}
		message := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, "")
		yield(model.StreamEventFromResponse(&model.Response{Message: message, Status: model.ResponseStatusCompleted, TurnComplete: true, StepComplete: true}), nil)
	}
}
func (p *dtwFixedProbe) checkState(r *model.Request, id string, want int) {
	for _, m := range r.Messages {
		for _, result := range m.ToolResults() {
			if result.ToolUseID != id {
				continue
			}
			for _, part := range result.Content {
				var raw []byte
				if part.JSON != nil {
					raw = part.JSON.Value
				} else if part.Text != nil {
					raw = []byte(part.Text.Text)
				}
				var data struct {
					Status      string `json:"status"`
					ExecutionID string `json:"execution_id"`
					Value       struct {
						Result int `json:"result"`
						Calls  int `json:"calls"`
					} `json:"value"`
				}
				if json.Unmarshal(raw, &data) == nil && data.Status == "completed" {
					if data.Value.Result != want || data.Value.Calls != 0 {
						p.t.Fatalf("native state got=%+v want=%d", data.Value, want)
					}
					if id == "exec-1" {
						p.executionID = data.ExecutionID
					}
					p.verified++
					return
				}
			}
		}
	}
	p.t.Fatalf("original state result %s unavailable", id)
}
