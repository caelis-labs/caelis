package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

// Pause target enumeration after one Host reads its startup history, so a
// different yielded script can durably pin a version before that Host proceeds.
type overlappingBindingSource struct {
	mu        sync.Mutex
	current   tool.Tool
	pauseNext atomic.Bool
	paused    chan struct{}
	resume    chan struct{}
}

func (s *overlappingBindingSource) Tools() []tool.Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []tool.Tool{s.current}
}

func (s *overlappingBindingSource) SearchTools() []tool.Tool {
	if s.pauseNext.Swap(false) {
		close(s.paused)
		<-s.resume
	}
	return s.Tools()
}

func (s *overlappingBindingSource) Lookup(ctx context.Context, name string) (tool.Tool, bool, error) {
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

func (s *overlappingBindingSource) CheckToolBinding(ctx context.Context, binding tool.Binding) error {
	one, found, err := s.Lookup(ctx, binding.Name)
	if err != nil {
		return err
	}
	if !found || tool.BindingForDefinition(one.Definition()) != binding {
		return tool.NewError(tool.ErrorCodeStaleRevision, "source replaced the binding")
	}
	return nil
}

func (s *overlappingBindingSource) publish(one tool.Tool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = one
}

func TestCodeModeOverlappingDirectBindingRequiresExplicitRefresh(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "without-search", true: "explicit-search"}[refresh], func(t *testing.T) {
			files, active, root := codeModeStore(t)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			source := &overlappingBindingSource{paused: make(chan struct{}), resume: make(chan struct{})}
			var effectsV1, effectsV2 atomic.Int32
			publish := func(version string, counter *atomic.Int32) {
				def := fixtureMCPDefinition(version)
				def.Metadata[tool.MetadataMCPSourceTrusted] = true
				def.Capabilities.ParallelSafe = true
				def.InputSchema = map[string]any{"type": "object", "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": "integer"}}}
				source.publish(tool.NamedTool{Def: def, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
					counter.Add(1)
					if call.Parent == nil || call.TargetBinding == nil || call.TargetBinding.Version != version {
						return tool.Result{}, fmt.Errorf("wrong canonical identity: %+v", call)
					}
					t.Logf("EFFECT version=%s parent=%s binding=%+v args=%s", version, call.Parent.ID, *call.TargetBinding, call.Input)
					return tool.Result{Content: []model.Part{model.NewJSONPart([]byte(`{"ok":true}`))}}, nil
				}})
			}
			publish("overlap-v1", &effectsV1)
			waitAEntered := make(chan struct{})
			releaseA := make(chan struct{})
			unblockA := sync.OnceFunc(func() { close(releaseA) })
			unblockB := sync.OnceFunc(func() { close(source.resume) })
			defer unblockA()
			defer unblockB()
			waitA := tool.NamedTool{Def: tool.Definition{Name: "WaitA", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
				close(waitAEntered)
				select {
				case <-source.paused:
					t.Log("B startup history is captured; its source enumeration is paused before reading current tools")
					return tool.Result{}, nil
				case <-ctx.Done():
					return tool.Result{}, ctx.Err()
				}
			}}
			keepA := tool.NamedTool{Def: tool.Definition{Name: "KeepAAlive", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
				events, err := files.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if err != nil {
					return tool.Result{}, err
				}
				binding := codeModeBindings(events)["docs__read"]
				if binding.Version != "overlap-v1" || effectsV1.Load() != 1 {
					return tool.Result{}, fmt.Errorf("interleaving failed: binding=%+v v1=%d", binding, effectsV1.Load())
				}
				t.Logf("A completed v1; durable canonical binding BEFORE B target preparation=%+v", binding)
				publish("overlap-v2", &effectsV2)
				t.Log("source changed to v2 with compatible schema; releasing B startup")
				unblockB()
				select {
				case <-releaseA:
					return tool.Result{}, nil
				case <-ctx.Done():
					return tool.Result{}, ctx.Err()
				}
			}}
			getHandle := func(req *model.Request, id string) string {
				for _, message := range req.Messages {
					for _, part := range message.Parts {
						if part.ToolResult == nil || part.ToolResult.Name != codemode.ToolName || part.ToolResult.ToolUseID != id {
							continue
						}
						for _, content := range part.ToolResult.Content {
							if content.JSON != nil {
								var snapshot struct {
									Handle string `json:"handle"`
								}
								if json.Unmarshal(content.JSON.Value, &snapshot) == nil && snapshot.Handle != "" {
									return snapshot.Handle
								}
							}
						}
					}
				}
				return ""
			}
			var handleA, handleB string
			waitHandle := func(id, handle string) []model.ToolCall {
				if handle == "" {
					t.Error("missing yielded script handle for " + id)
					return nil
				}
				raw, _ := json.Marshal(map[string]any{"action": "wait", "handle": handle})
				return []model.ToolCall{{ID: id, Name: tasktool.ToolName, Args: string(raw)}}
			}
			driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
				switch i {
				case 0:
					return []model.ToolCall{{ID: "script-a", Name: codemode.ToolName, Args: `{"code":"await WaitA({}); const r=await tools.call('docs__read',{value:1}); await KeepAAlive({}); return r;","yield_time_ms":0}`}}
				case 1:
					handleA = getHandle(req, "script-a")
					select {
					case <-waitAEntered:
					case <-ctx.Done():
						t.Error("A did not start")
						return nil
					}
					source.pauseNext.Store(true)
					code := "return await tools.call('docs__read',{value:2});"
					if refresh {
						code = "await tools.search({name:'docs__read'}); " + code
					}
					raw, _ := json.Marshal(map[string]any{"code": code, "yield_time_ms": 0})
					return []model.ToolCall{{ID: "script-b", Name: codemode.ToolName, Args: string(raw)}}
				case 2:
					handleB = getHandle(req, "script-b")
					return waitHandle("wait-b", handleB)
				case 3:
					unblockA()
					return waitHandle("wait-a", handleA)
				default:
					return nil
				}
			}}
			rt, err := New(Config{Sessions: files, TaskStore: sessionfile.NewTaskStore(files), AgentFactory: chat.Factory{}, PolicyRegistry: mcpFixturePolicy(), DefaultPolicyMode: "mcp-test"})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "overlapping direct calls must retain canonical binding", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{waitA, keepA, tasktool.New(), toolsearch.NewSource(source, exactMCPRanker{}), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: source}})
			if err != nil {
				t.Fatal(err)
			}
			if err = run.Handle.WaitCompletion(ctx); err != nil {
				t.Fatal(err)
			}
			events, err := files.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			searches := 0
			for _, event := range events {
				if event.Journal == nil || event.Journal.ToolExecution == nil {
					continue
				}
				r := event.Journal.ToolExecution
				if r.ToolName == tool.ToolSearchToolName {
					searches++
				}
				if r.ToolName == "docs__read" {
					t.Logf("JOURNAL parent=%s status=%s binding=%+v input=%s", r.Parent.ID, r.Status, r.Binding, r.Input)
				}
			}
			entries, err := rt.tasks.store.ListSession(ctx, active.SessionRef)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				t.Logf("TASK handle=%s state=%s running=%v", entry.Handle, entry.State, entry.Running)
				if entry.Running || entry.State == "cancelled" {
					t.Fatal("overlapping scripts did not settle before Turn completion")
				}
			}
			t.Logf("RESULT real Runtime/Goja overlapping yielded scripts: handleA=%s handleB=%s v1_effects=%d v2_effects=%d explicit_search_journal_records=%d final_binding=%+v", handleA, handleB, effectsV1.Load(), effectsV2.Load(), searches, codeModeBindings(events)["docs__read"])
			if len(entries) != 2 || handleA == "" || handleB == "" || effectsV1.Load() != 1 {
				t.Fatal("scripts did not establish required interleaving")
			}
			wantV2 := int32(0)
			wantBinding := "overlap-v1"
			wantBState := "failed"
			if refresh {
				wantV2, wantBinding, wantBState = 1, "overlap-v2", "completed"
			}
			if effectsV2.Load() != wantV2 {
				t.Fatalf("replacement effects=%d, want %d; refresh=%v", effectsV2.Load(), wantV2, refresh)
			}
			if (searches > 0) != refresh {
				t.Fatalf("search journal records=%d; refresh=%v", searches, refresh)
			}
			for _, entry := range entries {
				wantState := "completed"
				if entry.Handle == handleB {
					wantState = wantBState
				}
				if string(entry.State) != wantState {
					t.Fatalf("Task %s state=%s, want %s", entry.Handle, entry.State, wantState)
				}
			}
			if binding := codeModeBindings(events)["docs__read"]; binding.Version != wantBinding {
				t.Fatalf("canonical binding=%+v, want version %s", binding, wantBinding)
			}
			if !refresh {
				for _, event := range events {
					if event.Journal != nil && event.Journal.ToolExecution != nil {
						record := event.Journal.ToolExecution
						if record.ToolName == "docs__read" && record.Parent != nil && record.Parent.ID == "script-b" {
							t.Fatalf("rejected replacement wrote an execution claim: %+v", record)
						}
					}
				}
				raw, _ := json.Marshal(driver.requests[len(driver.requests)-1].Messages)
				if !strings.Contains(string(raw), "schema changed") || !strings.Contains(string(raw), "tools.search") {
					t.Fatalf("rejected replacement lost explicit refresh guidance: %s", raw)
				}
			}
			// A new Store and Runtime must preserve both the canonical binding and the
			// exact model context produced by the completed Turn, without replaying tools.
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
			resumedEvents, err := reopened.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			if binding := codeModeBindings(resumedEvents)["docs__read"]; binding.Version != wantBinding {
				t.Fatalf("reopened binding=%+v, want version %s", binding, wantBinding)
			}
			probe := &mcpScriptModel{steps: func(int, *model.Request) []model.ToolCall { return nil }}
			runCodeMode(t, reopened, active, agent.AgentSpec{Model: probe}, nil, nil)
			wantMessages := append(model.CloneMessages(driver.requests[len(driver.requests)-1].Messages),
				model.NewTextMessage(model.RoleAssistant, "done"), model.NewTextMessage(model.RoleUser, "synthetic Code Mode task"))
			if !reflect.DeepEqual(probe.requests[0].Messages, wantMessages) {
				t.Fatal("reopened model context differs from Runtime-produced context")
			}
			if effectsV1.Load() != 1 || effectsV2.Load() != wantV2 {
				t.Fatal("Store reopen replayed tool execution")
			}
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
