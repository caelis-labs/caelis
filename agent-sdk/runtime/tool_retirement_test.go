package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/prefixusage"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// A separate stdio process, real MCP transport and temporary receipt file;
// only the model decisions in the parent are deterministic.
func TestRetirementMCPServerProcess(t *testing.T) {
	if os.Getenv("CAELIS_RETIREMENT_SERVER") != "1" {
		return
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "retirement-local", Version: "1"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "receipt", Description: "write receipt", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		file, err := os.OpenFile(os.Getenv("CAELIS_RETIREMENT_RECEIPTS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		_, err = file.WriteString("receipt\n")
		err = errors.Join(err, file.Close())
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "durable local receipt"}}}, err
	})
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

type retirementScriptModel struct {
	requests []*model.Request
	steps    func(int, *model.Request) []model.ToolCall
}

func (*retirementScriptModel) Name() string { return "retirement-script" }
func (*retirementScriptModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (m *retirementScriptModel) Generate(_ context.Context, request *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.requests = append(m.requests, model.CloneRequest(request))
	index := len(m.requests) - 1
	return func(yield func(*model.StreamEvent, error) bool) {
		calls := m.steps(index, request)
		message := model.NewTextMessage(model.RoleAssistant, "done")
		reason := model.FinishReasonStop
		if len(calls) > 0 {
			message = model.MessageFromToolCalls(model.RoleAssistant, calls, "")
			reason = model.FinishReasonToolCalls
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: message, Status: model.ResponseStatusCompleted, FinishReason: reason, StepComplete: true, TurnComplete: true}), nil)
	}
}
func retirementMarkArgs(binding tool.Binding) string {
	raw, _ := json.Marshal(map[string]any{"action": "mark", "tools": []tool.Binding{binding}})
	return string(raw)
}
func retirementPolicy() policy.Registry {
	return staticPolicyRegistry{mode: policy.NamedMode{ID: "retirement-test", Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}}
}

func TestRetirementRealStdioRuntimeCompactReloadAndRediscover(t *testing.T) {
	for _, mode := range []struct{ dynamic, refresh bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		dynamic := mode.dynamic
		t.Run(fmt.Sprintf("refresh_%t/resolver_%t", mode.refresh, dynamic), func(t *testing.T) {
			root, work := t.TempDir(), t.TempDir()
			receipts := filepath.Join(work, "receipts.txt")
			manager, err := mcp.NewManagerWithOptions(t.Context(), []mcp.ServerSpec{{PluginID: "poc", Name: "local", Command: os.Args[0], Args: []string{"-test.run=^TestRetirementMCPServerProcess$"}, WorkDir: work, Env: map[string]string{"CAELIS_RETIREMENT_SERVER": "1", "CAELIS_RETIREMENT_RECEIPTS": receipts}}}, nil, mcp.ManagerOptions{RefreshTools: mode.refresh})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			<-manager.Initialized()
			ready := manager.Tools()
			if len(ready) != 1 {
				t.Fatalf("local MCP tools=%v", manager.GetServerInfos("poc"))
			}
			binding := tool.BindingForDefinition(ready[0].Definition())
			store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "poc", UserID: "local", Workspace: session.WorkspaceRef{Key: "poc", CWD: work}})
			if err != nil {
				t.Fatal(err)
			}
			cfg := Config{Sessions: store, AgentFactory: chat.Factory{}, ExperimentalToolRetirement: true, PolicyRegistry: retirementPolicy(), DefaultPolicyMode: "retirement-test"}
			runtime, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			search := toolsearch.NewSource(manager, toolsearch.NewLexicalRanker())
			makeSpec := func(llm model.LLM) agent.AgentSpec {
				spec := agent.AgentSpec{Model: llm, Tools: []tool.Tool{search}, DeferredTools: manager}
				if dynamic {
					spec.ResolveModelRequest = func(context.Context) (agent.ModelRequestSnapshot, error) {
						return agent.ModelRequestSnapshot{Model: llm, Tools: []tool.Tool{search}, DeferredTools: manager}, nil
					}
				}
				return spec
			}
			approvals := 0
			run := func(rt *Runtime, llm model.LLM) {
				t.Helper()
				result, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "execute local receipt and retire", AgentSpec: makeSpec(llm), ApprovalRequester: approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
					approvals++
					return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
				})})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := drainRunnerEvents(t, result.Handle); err != nil {
					t.Fatal(err)
				}
			}
			script := &retirementScriptModel{steps: func(index int, _ *model.Request) []model.ToolCall {
				switch index {
				case 0:
					return []model.ToolCall{{ID: "search", Name: tool.ToolSearchToolName, Args: `{"query":"receipt"}`}}
				case 1:
					return []model.ToolCall{{ID: "effect", Name: binding.Name, Args: `{}`}}
				case 2:
					return []model.ToolCall{{ID: "mark", Name: tool.RetireToolName, Args: retirementMarkArgs(binding)}}
				}
				return nil
			}}
			run(runtime, script)
			if len(script.requests) != 4 || len(script.requests[0].Tools) != 1 || len(script.requests[1].Tools) != 3 || !reflect.DeepEqual(script.requests[1].Tools, script.requests[3].Tools) {
				t.Fatalf("unexpected exposure across mark: %+v", script.requests)
			}
			if prefixusage.ForRequest(script.requests[1]) != prefixusage.ForRequest(script.requests[3]) {
				t.Fatal("mark changed request prefix")
			}
			before, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			state, ok := exposureFromEvents(before.Events)
			if !ok || len(state.Active) != 1 || len(state.Pending) != 1 {
				t.Fatalf("pending projection=%+v", state)
			}
			result, err := runtime.Compact(t.Context(), CompactRequest{SessionRef: active.SessionRef, Model: &compactionRequestCaptureModel{}, DeferredTools: manager})
			if err != nil || !result.Compacted {
				t.Fatalf("compact=%+v %v", result, err)
			}
			coverage, covered := compact.CompactEventDataFromEvent(result.Event)
			if !covered || result.Event.ID == "" || result.Event.Seq == 0 || coverage.SummarizedThroughID == "" ||
				coverage.SummarizedThroughSeq == 0 || coverage.SummarizedThroughSeq >= result.Event.Seq {
				t.Fatal("retirement lacks a durable checkpoint identity/coverage")
			}
			state, ok = tool.ExposureFromMetadata(result.Event.Meta)
			if !ok || len(state.Active) != 0 || len(state.Pending) != 0 {
				t.Fatalf("committed exposure=%+v", state)
			}
			reloaded := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			loaded, err := reloaded.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Events) < len(before.Events) || !reflect.DeepEqual(before.Events, loaded.Events[:len(before.Events)]) {
				t.Fatal("retirement changed historical events")
			}
			// Compare entire model requests from live and fresh file-store projections.
			live, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			capture := func(loaded session.LoadedSession) *model.Request {
				probe := &retirementScriptModel{steps: func(int, *model.Request) []model.ToolCall { return nil }}
				a, err := (chat.Factory{}).NewAgent(t.Context(), agent.AgentSpec{Model: probe, Tools: []tool.Tool{search, tool.NewRetirementTool()}, DeferredTools: manager})
				if err != nil {
					t.Fatal(err)
				}
				events := promptEventsWithToolVisibilityMetadata(compact.PromptEventsFromLatestCompact(loaded.Events), loaded.Events, true)
				for _, err := range a.Run(agent.NewContext(agent.ContextSpec{Context: t.Context(), Session: loaded.Session, Events: events, State: loaded.State})) {
					if err != nil {
						t.Fatal(err)
					}
				}
				return probe.requests[0]
			}
			liveRequest, replayRequest := capture(live), capture(loaded)
			if !reflect.DeepEqual(liveRequest, replayRequest) || len(replayRequest.Tools) != 1 {
				t.Fatal("whole model context did not round trip after retirement")
			}
			cfg.Sessions = reloaded
			runtime2, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			again := &retirementScriptModel{steps: func(index int, _ *model.Request) []model.ToolCall {
				if index == 0 {
					return []model.ToolCall{{ID: "discover-again", Name: tool.ToolSearchToolName, Args: `{"query":"receipt"}`}}
				}
				if index == 1 {
					return []model.ToolCall{{ID: "new-effect", Name: binding.Name, Args: `{}`}}
				}
				return nil
			}}
			run(runtime2, again)
			raw, err := os.ReadFile(receipts)
			if err != nil || string(raw) != "receipt\nreceipt\n" || approvals != 2 {
				t.Fatalf("local effects=%q approvals=%d err=%v", raw, approvals, err)
			}
			if len(again.requests[0].Tools) != 1 || len(again.requests[1].Tools) != 3 {
				t.Fatal("rediscovery did not restore manager and tool")
			}
			t.Logf("real stdio MCP: approvals=%d effects=2, schemas 1 -> 3 -> 3(mark) -> 1(compact/reload) -> 3(rediscover)", approvals)
		})
	}
}

func TestRetirementIgnoresGeneratedCheckpointUntilCanonicalIdentityAndCoverage(t *testing.T) {
	binding := tool.Binding{Name: "local__receipt", Identity: "stable", Version: "v1"}
	prior := &session.Event{ID: "result", Seq: 1, Type: session.EventTypeToolResult, Meta: map[string]any{
		tool.MetadataToolExposure: tool.ExposureState{Version: 1, Active: []tool.Binding{binding}, Pending: []tool.Binding{binding}},
	}}
	data := compact.CompactEventData{ContractVersion: compact.CompactContractVersion, SummarizedThroughID: prior.ID, SummarizedThroughSeq: prior.Seq}
	checkpoint := buildCompactEvent(session.Session{}, "generated summary", data)
	checkpoint.Meta[tool.MetadataToolExposure] = tool.ExposureState{Version: 1}
	for _, scenario := range []string{"generated", "identified_uncommitted", "invalid_coverage", "canonical"} {
		t.Run(scenario, func(t *testing.T) {
			event := session.CloneEvents([]*session.Event{checkpoint})[0]
			if scenario != "generated" {
				event.ID = "checkpoint"
			}
			switch scenario {
			case "invalid_coverage":
				event.Seq = 1
			case "canonical":
				event.Seq = 2
			}
			state, ok := exposureFromEvents([]*session.Event{prior, event, event})
			want := 1
			if scenario == "canonical" {
				want = 0
			}
			if !ok || len(state.Active) != want {
				t.Fatalf("exposure after %s = %+v", scenario, state)
			}
		})
	}
}

type retirementFaultStore struct {
	session.Service
	after bool
}

func (s retirementFaultStore) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if compact.IsCompactEvent(req.Event) {
		if s.after {
			if _, err := s.Service.AppendEvent(ctx, req); err != nil {
				return nil, err
			}
		}
		return nil, errors.New("injected checkpoint write failure")
	}
	return s.Service.AppendEvent(ctx, req)
}
func TestRetirementCheckpointFailureCancellationAndPendingCalls(t *testing.T) {
	for _, scenario := range []string{"write_failure", "unknown_committed", "cancel", "pending_call", "complete"} {
		t.Run(scenario, func(t *testing.T) {
			store, active := newTestSessionService(t, "retirement-"+scenario)
			binding := tool.Binding{Name: "local__receipt", Identity: "stable", Version: "v1"}
			state := tool.ExposureState{Version: 1, Active: []tool.Binding{binding}, Pending: []tool.Binding{binding}}
			event := userTextEvent("preserve objective and receipts")
			event.Meta = map[string]any{tool.MetadataToolExposure: state}
			appendTestEvent(t, store, active.SessionRef, event)
			if scenario == "pending_call" {
				appendTestEvent(t, store, active.SessionRef, &session.Event{Type: session.EventTypeToolCall, Tool: &session.EventTool{ID: "pending", Name: binding.Name, Status: "pending"}})
			}
			service := store
			if scenario == "write_failure" || scenario == "unknown_committed" {
				service = retirementFaultStore{Service: store, after: scenario == "unknown_committed"}
			}
			rt, err := New(Config{Sessions: service, AgentFactory: chat.Factory{}, ExperimentalToolRetirement: true})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var llm model.LLM = &compactionRequestCaptureModel{}
			if scenario == "cancel" {
				llm = &cancelledCompactionModel{cancel: cancel}
			}
			result, err := rt.Compact(ctx, CompactRequest{SessionRef: active.SessionRef, Model: llm})
			if slices.Contains([]string{"write_failure", "cancel"}, scenario) && (err == nil || result.Compacted) {
				t.Fatalf("failure adopted checkpoint: %+v %v", result, err)
			}
			if scenario == "unknown_committed" && (err != nil || !result.Compacted || result.Event == nil || result.Event.Seq == 0) {
				t.Fatalf("original checkpoint was not reconciled: %+v %v", result, err)
			}
			loaded, loadErr := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			current, _ := exposureFromEvents(loaded.Events)
			want := 1
			if scenario == "complete" || scenario == "unknown_committed" {
				want = 0
			}
			if len(current.Active) != want {
				t.Fatalf("exposure after %s=%+v", scenario, current)
			}
		})
	}
}

func TestRetirementManualCompactWaitsForPendingModelBatch(t *testing.T) {
	store, active := newTestSessionService(t, "retirement-pending-model")
	def := fixtureMCPDefinition("pending-v1")
	binding := tool.BindingForDefinition(def)
	state := tool.ExposureState{Version: 1, Active: []tool.Binding{binding}, Pending: []tool.Binding{binding}}
	event := userTextEvent("preserve pending model batch")
	event.Meta = map[string]any{tool.MetadataToolExposure: state}
	appendTestEvent(t, store, active.SessionRef, event)
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, ExperimentalToolRetirement: true, PolicyRegistry: retirementPolicy(), DefaultPolicyMode: "retirement-test"})
	if err != nil {
		t.Fatal(err)
	}
	source := &runtimeDeferredSource{ready: []tool.Tool{tool.NamedTool{Def: def}}}
	entered, release := make(chan struct{}), make(chan struct{})
	llm := &retirementScriptModel{steps: func(int, *model.Request) []model.ToolCall { close(entered); <-release; return nil }}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "continue", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{toolsearch.NewSource(source, toolsearch.NewLexicalRanker())}, DeferredTools: source}})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	result, err := rt.Compact(t.Context(), CompactRequest{SessionRef: active.SessionRef, Model: &compactionRequestCaptureModel{}})
	// Always release the blocked model before reporting failure.
	close(release)
	if _, runErr := drainRunnerEvents(t, run.Handle); runErr != nil {
		t.Fatal(runErr)
	}
	if err != nil || !result.Compacted {
		t.Fatalf("manual compact: %+v %v", result, err)
	}
	pending, _ := tool.ExposureFromMetadata(result.Event.Meta)
	if len(pending.Active) != 1 || len(pending.Pending) != 1 {
		t.Fatal("retired while an admitted model response was pending")
	}
	result, err = rt.Compact(t.Context(), CompactRequest{SessionRef: active.SessionRef, Model: &compactionRequestCaptureModel{}})
	if err != nil || !result.Compacted {
		t.Fatalf("idle compact: %+v %v", result, err)
	}
	retired, _ := tool.ExposureFromMetadata(result.Event.Meta)
	if len(retired.Active) != 0 {
		t.Fatal("idle checkpoint did not apply pending mark")
	}
}
