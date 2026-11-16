package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

// changingBindingSource replaces one method while retaining its original name.
type changingBindingSource struct {
	mu      sync.Mutex
	current tool.Tool
}

func (s *changingBindingSource) Tools() []tool.Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []tool.Tool{s.current}
}

func (s *changingBindingSource) SearchTools() []tool.Tool { return s.Tools() }

func (s *changingBindingSource) Lookup(ctx context.Context, name string) (tool.Tool, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil || s.current.Definition().Name != name {
		return nil, false, nil
	}
	return s.current, true, nil
}

func (s *changingBindingSource) CheckToolBinding(ctx context.Context, binding tool.Binding) error {
	one, found, err := s.Lookup(ctx, binding.Name)
	if err != nil {
		return err
	}
	if !found || tool.BindingForDefinition(one.Definition()) != binding {
		return tool.NewError(tool.ErrorCodeStaleRevision, "source replaced the binding")
	}
	return nil
}

func (s *changingBindingSource) publish(one tool.Tool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = one
}

// Session scripts are serial. A compatible replacement still needs explicit
// discovery; a retained VM does not refresh canonical binding authority.
func TestCodeModeSequentialBindingRequiresExplicitRefresh(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "without-search", true: "explicit-search"}[refresh], func(t *testing.T) {
			files, active, root := codeModeStore(t)
			source := &changingBindingSource{}
			var effectsV1, effectsV2 atomic.Int32
			publish := func(version string, counter *atomic.Int32) {
				def := fixtureMCPDefinition(version)
				def.Metadata[tool.MetadataMCPSourceTrusted] = true
				source.publish(tool.NamedTool{Def: def, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
					counter.Add(1)
					if call.Parent == nil || call.TargetBinding == nil || call.TargetBinding.Version != version {
						return tool.Result{}, fmt.Errorf("wrong canonical binding: %+v", call)
					}
					return tool.Result{Content: []model.Part{model.NewJSONPart([]byte(`{"ok":true}`))}}, nil
				}})
			}
			publish("serial-v1", &effectsV1)
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "script-a", Name: codemode.ToolName, Args: `{"code":"return await tools.call('docs__read',{});"}`}}
				}
				if i == 1 {
					if effectsV1.Load() != 1 {
						t.Error("first script did not complete before the next")
					}
					publish("serial-v2", &effectsV2)
					code := `return await tools.call("docs__read",{});`
					if refresh {
						code = `(await tools.search({name:"docs__read"})).json();` + code
					}
					raw, _ := json.Marshal(map[string]any{"code": code})
					return []model.ToolCall{{ID: "script-b", Name: codemode.ToolName, Args: string(raw)}}
				}
				return nil
			}}
			_, rt := runCodeMode(t, files, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{toolsearch.NewSource(source, exactMCPRanker{}), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: source}, nil, nil)
			events, err := files.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			wantV2 := int32(0)
			wantBinding, wantState := "serial-v1", "failed"
			if refresh {
				wantV2 = 1
				wantBinding, wantState = "serial-v2", "completed"
			}
			if effectsV1.Load() != 1 || effectsV2.Load() != wantV2 {
				t.Fatalf("effects v1=%d v2=%d; want 1/%d", effectsV1.Load(), effectsV2.Load(), wantV2)
			}
			entries, err := rt.tasks.store.ListSession(t.Context(), active.SessionRef)
			if err != nil || len(entries) != 2 {
				t.Fatalf("Tasks=%d error=%v", len(entries), err)
			}
			for _, entry := range entries {
				if entry.Metadata["parent_call"] == "script-b" && string(entry.State) != wantState {
					t.Fatalf("second Task state=%s want %s", entry.State, wantState)
				}
				if entry.Running {
					t.Fatal("script remained running")
				}
			}
			if binding := codeModeBindings(events)["docs__read"]; binding.Version != wantBinding {
				t.Fatalf("binding=%+v want %s", binding, wantBinding)
			}
			if !refresh {
				result := errorDeliveryModelResult(t, driver.requests[len(driver.requests)-1], "script-b")
				raw := string(errorDeliveryJSON(t, result.Content))
				if !result.IsError || !strings.Contains(raw, "tools.search") {
					t.Fatal(raw)
				}
			}
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
			reloaded, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			if binding := codeModeBindings(reloaded)["docs__read"]; binding.Version != wantBinding {
				t.Fatal(binding)
			}
			assertSubagentSagaModelRoundTrip(t, reopened, active.SessionRef)
		})
	}
}

// Hold the first journal append after the binding comparison. A concurrent
// explicit refresh must not commit until the complete started claim is durable.
type bindingClaimStore struct {
	session.Service
	prepared chan struct{}
	resume   chan struct{}
}

func (s bindingClaimStore) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if journal := req.Event.Journal; journal != nil && journal.ToolExecution != nil {
		record := journal.ToolExecution
		if record.ToolName == "docs__read" && record.Status == session.ToolExecutionPrepared {
			close(s.prepared)
			select {
			case <-s.resume:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	return s.Service.AppendEvent(ctx, req)
}

func TestCodeModeBindingClaimOrdersConcurrentSearchReceipt(t *testing.T) {
	files, active, root := codeModeStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	store := bindingClaimStore{Service: files, prepared: make(chan struct{}), resume: make(chan struct{})}
	resume := sync.OnceFunc(func() { close(store.resume) })
	defer resume()
	writes := &sessionWriteQueue{}
	def := fixtureMCPDefinition("claim-v1")
	binding := tool.BindingForDefinition(def)
	var effects atomic.Int32
	effect := journaledTool{sessions: store, writes: writes, sessionRef: active.SessionRef, runID: "run", turnID: "turn", base: tool.NamedTool{Def: def, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		effects.Add(1)
		return tool.Result{}, tool.NewError(errorcode.UnknownOutcome, "effect outcome is unknown")
	}}}
	makeCall := func(name, id string) (context.Context, tool.Call) {
		call := tool.Call{ID: id, Name: name, Input: []byte(`{}`), Execution: tool.InvocationContext{SessionID: active.SessionID, TurnID: "turn", ItemID: id}, Parent: &tool.ParentCall{Kind: codemode.ToolKind, ID: "script", Name: codemode.ToolName}}
		admission := &codeModeAdmissionBinding{callID: call.ID, name: call.Name, parent: *call.Parent, validate: func(ctx context.Context) (func(), error) { return func() {}, ctx.Err() }}
		return context.WithValue(ctx, codeModeAdmissionKey{}, admission), call
	}
	effectCtx, effectCall := makeCall(def.Name, "effect")
	effectCall.TargetBinding = &binding
	effectDone := make(chan error, 1)
	go func() { _, err := effect.Call(effectCtx, effectCall); effectDone <- err }()
	select {
	case <-store.prepared:
	case <-ctx.Done():
		t.Fatal("effect did not reach its prepared append")
	}
	claimTicket := sessionWriteTailForTest(writes, active.SessionRef)
	if claimTicket == nil {
		t.Fatal("binding comparison released the Session writer before its started claim")
	}
	fresh := fixtureMCPDefinition("claim-v2")
	selected := tool.ToolSearchResult{Count: 1, Tools: []tool.ToolSearchDiscoveredTool{tool.NewToolSearchDiscoveredTool(fresh)}}
	raw, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	search := journaledTool{sessions: store, writes: writes, sessionRef: active.SessionRef, runID: "run", turnID: "turn", base: tool.NamedTool{Def: tool.Definition{Name: tool.ToolSearchToolName}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{Content: []model.Part{model.NewJSONPart(raw)}, Metadata: map[string]any{mcpdiscovery.MetadataKey: true}}, nil
	}}}
	searchCtx, searchCall := makeCall(tool.ToolSearchToolName, "search")
	searchDone := make(chan error, 1)
	go func() { _, err := search.Call(searchCtx, searchCall); searchDone <- err }()
	waitForSessionWriteTailChange(t, writes, active.SessionRef, claimTicket)
	select {
	case err := <-searchDone:
		t.Fatalf("search receipt overtook an uncommitted started claim: %v", err)
	default:
	}
	resume()
	if err := <-effectDone; !errorcode.Is(err, errorcode.UnknownOutcome) {
		t.Fatalf("effect outcome=%v, want unknown", err)
	}
	if err := <-searchDone; err != nil {
		t.Fatal(err)
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	events, err := reopened.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	var startedSeq, searchSeq uint64
	unknown := false
	for _, event := range events {
		if event.Journal == nil || event.Journal.ToolExecution == nil {
			continue
		}
		record := event.Journal.ToolExecution
		if record.ToolName == def.Name && record.Status == session.ToolExecutionStarted {
			startedSeq = event.Seq
		}
		if record.ToolName == def.Name && record.Status == session.ToolExecutionUnknownOutcome {
			unknown = true
		}
		if record.ToolName == tool.ToolSearchToolName && record.Status == session.ToolExecutionSucceeded {
			searchSeq = event.Seq
		}
	}
	if startedSeq == 0 || searchSeq <= startedSeq || !unknown || effects.Load() != 1 {
		t.Fatalf("started seq=%d search seq=%d unknown=%v effects=%d", startedSeq, searchSeq, unknown, effects.Load())
	}
	if binding := codeModeBindings(events)[def.Name]; binding != tool.BindingForDefinition(fresh) {
		t.Fatalf("refresh lost canonical order: %+v", binding)
	}
}
