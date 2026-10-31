package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFixedEntryMCPServerProcess(t *testing.T) {
	if os.Getenv("CAELIS_FIXED_MCP_SERVER") != "1" {
		return
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fixed-entry", Version: "1"}, nil)
	for i := 0; i < 500; i++ {
		name := fmt.Sprintf("item_%03d", i)
		server.AddTool(&mcpsdk.Tool{Name: name, Description: "Read synthetic numbered receipt", InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []any{"number"}, "properties": map[string]any{"number": map[string]any{"type": "integer"}}}}, func(_ context.Context, r *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			f, err := os.OpenFile(os.Getenv("CAELIS_FIXED_MCP_RECEIPTS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return nil, err
			}
			_, err = fmt.Fprintln(f, name)
			closeErr := f.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, closeErr
			}
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(r.Params.Arguments)}}}, nil
		})
	}
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

type exactMCPRanker struct{}

func (exactMCPRanker) Rank(_ context.Context, q string, defs []tool.Definition, _ int, _ toolsearch.SearchModel) ([]string, error) {
	for _, d := range defs {
		if d.Name == q {
			return []string{q}, nil
		}
	}
	return nil, nil
}

type fixedEntryModel struct {
	steps      int
	calls      int
	start      int
	firstTools []byte
	t          *testing.T
	search     bool
}

func (*fixedEntryModel) Name() string                     { return "fixed-entry-fixture" }
func (*fixedEntryModel) Capabilities() model.Capabilities { return runtimeTestModelCapabilities() }
func (m *fixedEntryModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	raw, _ := json.Marshal(req.Tools)
	if m.firstTools == nil {
		m.firstTools = raw
	} else if string(raw) != string(m.firstTools) {
		m.t.Fatal("MCP discovery changed actual model tools bytes")
	}
	names := toolNamesFromRequestForTest(*req)
	if !reflect.DeepEqual(names, []string{tool.ToolSearchToolName, tool.MCPCallToolName}) {
		m.t.Fatalf("tools=%v", names)
	}
	step := m.steps
	m.steps++
	return func(yield func(*model.StreamEvent, error) bool) {
		message := model.NewTextMessage(model.RoleAssistant, "done")
		total := m.calls
		if m.search {
			total *= 2
		}
		if step < total {
			index := step
			if m.search {
				index /= 2
			}
			name := fmt.Sprintf("bulk__item_%03d", index+m.start)
			call := model.ToolCall{ID: fmt.Sprintf("call-%d", step), Name: tool.MCPCallToolName, Args: fmt.Sprintf(`{"tool_name":%q,"arguments":{"number":%d}}`, name, index)}
			if m.search && step%2 == 0 {
				call.Name = tool.ToolSearchToolName
				call.Args = fmt.Sprintf(`{"query":%q,"limit":1}`, name)
			}
			message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, "")
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: message, Status: model.ResponseStatusCompleted, StepComplete: true, TurnComplete: true}), nil)
	}
}

func TestFixedMCPEntryHundredsAndCanonicalResume(t *testing.T) {
	root := t.TempDir()
	receipt := filepath.Join(root, "receipts")
	manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "fixture", Name: "bulk", Command: os.Args[0], Args: []string{"-test.run=^TestFixedEntryMCPServerProcess$"}, WorkDir: root, SourceTrusted: true, Env: map[string]string{"CAELIS_FIXED_MCP_SERVER": "1", "CAELIS_FIXED_MCP_RECEIPTS": receipt}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	<-manager.Initialized()
	if len(manager.Tools()) != 500 {
		t.Fatalf("catalog=%d", len(manager.Tools()))
	}
	store := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "fixture", Workspace: session.WorkspaceRef{Key: "fixture", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	policies := 0
	registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "fixed", Decide: func(_ context.Context, c policy.ToolContext) (policy.Decision, error) {
		if tool.IsMCPDefinition(c.Tool) {
			policies++
			if c.Call.Name != c.Tool.Name || c.Call.ID == "" || c.Call.Execution.ItemID == "" || c.Call.ModelCall == nil {
				t.Errorf("real-tool policy identity missing: %s", c.Call.Name)
			}
		}
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}}
	run := func(sessions session.Service, llm *fixedEntryModel) {
		rt, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}, PolicyRegistry: registry, DefaultPolicyMode: "fixed"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "read numbered synthetic records", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{toolsearch.NewSource(manager, exactMCPRanker{})}, DeferredTools: manager}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = drainRunnerEvents(t, result.Handle)
		if err != nil {
			t.Fatal(err)
		}

	}
	run(store, &fixedEntryModel{t: t, calls: 200, search: true})
	reloaded := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	run(reloaded, &fixedEntryModel{t: t, calls: 1, start: 199})
	raw, err := os.ReadFile(receipt)
	if err != nil || len(strings.Fields(string(raw))) != 201 || policies != 201 {
		t.Fatalf("effects=%d policies=%d err=%v", len(strings.Fields(string(raw))), policies, err)
	}
	loaded, err := reloaded.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	terminals := 0
	for _, e := range loaded.Events {
		if e.Tool != nil && e.Tool.ModelCall != nil {
			if !strings.HasPrefix(e.Tool.Name, "bulk__") || e.Tool.Input["number"] == nil || e.Tool.ID != e.Tool.ModelCall.ID {
				t.Fatalf("display identity lost: %+v", e.Tool)
			}
			if e.Type == session.EventTypeToolResult && e.Message != nil {
				r := e.Message.ToolResults()[0]
				if r.Name != tool.MCPCallToolName || r.ToolUseID != e.Tool.ID {
					t.Fatal("model pairing lost")
				}
			}
		}
		if e.Journal != nil && e.Journal.ToolExecution != nil {
			r := e.Journal.ToolExecution
			if strings.HasPrefix(r.ToolName, "bulk__") && r.Status == session.ToolExecutionSucceeded {
				terminals++
				if r.ModelCall == nil || r.Binding == nil {
					t.Fatal("journal lost protocol or target binding")
				}
			}
		}
	}
	if terminals != 201 {
		t.Fatalf("terminal journals=%d", terminals)
	}
}
