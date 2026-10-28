package runtime

import (
	"context"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExperimentalAsyncPreservesTurnStartBudget(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			h := newAsyncHarness(t, "")
			h.rt.compaction.Async.Enabled = enabled
			if _, err := h.store.AppendEvent(h.ctx, session.AppendEventRequest{SessionRef: h.active.SessionRef, MutationGuard: session.RuntimeMutationGuard(h.ctx), Event: experimentalAssistantEvent(strings.Repeat("previous work with concrete evidence; ", 4000))}); err != nil {
				t.Fatal(err)
			}
			h.llm.summaries <- asyncTextResponse(asyncFixtureSummary, 1000)
			h.llm.responses <- asyncTextResponse("answer", 1000)
			run, err := h.rt.Run(h.ctx, agent.RunRequest{SessionRef: h.active.SessionRef, Input: "preserve this current user input", AgentSpec: agent.AgentSpec{Model: h.llm}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := drainRunnerEvents(t, run.Handle); err != nil {
				t.Fatal(err)
			}
			req := receiveSnapshotValue(t, h.llm.foreground).request
			text := asyncMessagesText(req.Messages)
			if !strings.Contains(text, "CONTEXT CHECKPOINT") || !strings.Contains(text, "preserve this current user input") {
				t.Fatal("first request bypassed compaction or lost new user input")
			}
		})
	}
}

func TestExperimentalFailedMCPDoesNotBlockUnrelatedCompact(t *testing.T) {
	for bits := 0; bits < 2; bits++ {
		t.Run(fmt.Sprint(bits), func(t *testing.T) {
			manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "fixture", Name: "missing", Command: filepath.Join(t.TempDir(), "nonexistent")}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			<-manager.Initialized()
			store, active := newTestSessionService(t, "failed-mcp")
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Compaction: CompactionConfig{Enabled: true, Async: AsyncCompactionConfig{Enabled: bits&1 != 0}}})
			if err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				appendTestEvent(t, store, active.SessionRef, userTextEvent("independent history"))
				appendTestEvent(t, store, active.SessionRef, experimentalAssistantEvent("independent answer"))
				result, err := rt.Compact(t.Context(), CompactRequest{SessionRef: active.SessionRef, Model: &compactionRequestCaptureModel{}, DeferredTools: manager})
				if err != nil || !result.Compacted {
					t.Fatalf("attempt %d: compact=%v err=%v", attempt, result.Compacted, err)
				}
			}
		})
	}
}

func TestExperimentalMCPWorkflowIndependentCombinations(t *testing.T) {
	for bits := 0; bits < 2; bits++ {
		for _, dynamic := range []bool{false, true} {
			t.Run(fmt.Sprintf("flags_%d/dynamic_%t", bits, dynamic), func(t *testing.T) {
				t.Parallel() // Each workflow owns its HTTP server, Runtime and file Store.
				server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "workflow", Version: "1"}, nil)
				var calls atomic.Int32
				install := func(version string) {
					server.AddTool(&mcpsdk.Tool{Name: "read", Description: version, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
						calls.Add(1)
						return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: version}}}, nil
					})
				}
				install("schema-v1")
				endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
				defer endpoint.Close()
				manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "fixture", Name: "local", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer manager.Close()
				<-manager.Initialized()
				root := t.TempDir()
				store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
				active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "workflow", UserID: "fixture", Workspace: session.WorkspaceRef{Key: "fixture", CWD: t.TempDir()}})
				if err != nil {
					t.Fatal(err)
				}
				rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Compaction: CompactionConfig{Enabled: true, Async: AsyncCompactionConfig{Enabled: bits&1 != 0}}, PolicyRegistry: retirementPolicy(), DefaultPolicyMode: "retirement-test"})
				if err != nil {
					t.Fatal(err)
				}
				search := toolsearch.NewSource(manager, toolsearch.NewLexicalRanker())
				update := tool.NamedTool{Def: tool.Definition{Name: "UpdateCatalog", InputSchema: map[string]any{"type": "object"}, EffectClass: tool.EffectReadOnly}, Invoke: func(ctx context.Context, c tool.Call) (tool.Result, error) {
					install("schema-v2")
					{
						deadline := time.Now().Add(5 * time.Second)
						for {
							current := manager.Tools()
							if len(current) == 1 && strings.Contains(current[0].Definition().Description, "schema-v2") {
								break
							}
							if time.Now().After(deadline) {
								return tool.Result{}, fmt.Errorf("notification did not refresh")
							}
							time.Sleep(time.Millisecond)
						}
					}
					return tool.Result{ID: c.ID, Name: c.Name, Content: []model.Part{model.NewTextPart("updated")}}, nil
				}}
				script := &retirementScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
					switch i {
					case 0, 2:
						return []model.ToolCall{{ID: fmt.Sprintf("search-%d", i), Name: tool.ToolSearchToolName, Args: `{"query":"read"}`}}
					case 1:
						return []model.ToolCall{{ID: "update", Name: "UpdateCatalog", Args: `{}`}}
					case 3:
						want := "schema-v2"
						found := false
						for _, def := range req.Tools {
							if def.Function != nil && def.Function.Name == "local__read" {
								found = strings.Contains(def.Function.Description, want)
							}
						}
						if !found {
							t.Errorf("fresh discovery did not supply %s", want)
						}
						return []model.ToolCall{{ID: "effect", Name: "local__read", Args: `{}`}}
					case 4:
						return []model.ToolCall{{ID: "mark", Name: tool.RetireToolName, Args: retirementMarkArgs(tool.BindingForDefinition(manager.Tools()[0].Definition()))}}
					}
					return nil
				}}
				spec := agent.AgentSpec{Model: script, Tools: []tool.Tool{search, update}, DeferredTools: manager}
				if dynamic {
					spec.ResolveModelRequest = func(context.Context) (agent.ModelRequestSnapshot, error) {
						return agent.ModelRequestSnapshot{Revision: "profile", Model: script, Tools: []tool.Tool{search, update}, DeferredTools: manager, Validate: func(context.Context) (func(), error) { return func() {}, nil }}, nil
					}
				}
				run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "exercise catalog", AgentSpec: spec, ApprovalRequester: approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
					return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
				})})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := drainRunnerEvents(t, run.Handle); err != nil {
					t.Fatal(err)
				}
				if calls.Load() != 1 {
					t.Fatalf("remote effects=%d want 1", calls.Load())
				}
				result, err := rt.Compact(t.Context(), CompactRequest{SessionRef: active.SessionRef, Model: &compactionRequestCaptureModel{}, DeferredTools: manager})
				if err != nil {
					t.Fatal(err)
				}
				data, _ := compact.CompactEventDataFromEvent(result.Event)
				want := 0
				if len(data.DiscoveredTools) != want {
					t.Fatalf("compact exposure=%v", data.DiscoveredTools)
				}
				// A later activation can rediscover tools retired by a prior checkpoint.
				ordinary, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
				if err != nil {
					t.Fatal(err)
				}
				again := &retirementScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
					if i == 0 {
						return []model.ToolCall{{ID: "ordinary-search", Name: tool.ToolSearchToolName, Args: `{"query":"read"}`}}
					}
					return nil
				}}
				resumed, err := ordinary.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "ordinary discovery", AgentSpec: agent.AgentSpec{Model: again, Tools: []tool.Tool{search}, DeferredTools: manager}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := drainRunnerEvents(t, resumed.Handle); err != nil {
					t.Fatal(err)
				}
				result, err = ordinary.Compact(t.Context(), CompactRequest{SessionRef: active.SessionRef, Model: &compactionRequestCaptureModel{}, DeferredTools: manager})
				if err != nil {
					t.Fatal(err)
				}
				data, _ = compact.CompactEventDataFromEvent(result.Event)
				if !reflect.DeepEqual(data.DiscoveredTools, []string{"local__read"}) {
					t.Fatalf("ordinary compact lost later discovery: %v", data.DiscoveredTools)
				}
				live, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
				if err != nil {
					t.Fatal(err)
				}
				replay, err := sessionfile.NewStore(sessionfile.Config{RootDir: root}).LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(live.Events, replay.Events) {
					t.Fatal("activation/replay lost or duplicated canonical results")
				}
				capture := func(loaded session.LoadedSession) *model.Request {
					probe := &retirementScriptModel{steps: func(int, *model.Request) []model.ToolCall { return nil }}
					chatAgent, err := (chat.Factory{}).NewAgent(t.Context(), agent.AgentSpec{Model: probe, Tools: []tool.Tool{search, tool.NewRetirementTool()}, DeferredTools: manager})
					if err != nil {
						t.Fatal(err)
					}
					events := promptEventsWithToolVisibilityMetadata(compact.PromptEventsFromLatestCompact(loaded.Events), loaded.Events)
					for _, err := range chatAgent.Run(agent.NewContext(agent.ContextSpec{Context: t.Context(), Session: loaded.Session, Events: events, State: loaded.State})) {
						if err != nil {
							t.Fatal(err)
						}
					}
					return probe.requests[0]
				}
				before, after := capture(live), capture(replay)
				if !reflect.DeepEqual(before, after) || len(after.Tools) != 3 {
					t.Fatal("ordinary model context/exposure did not round trip")
				}
			})
		}
	}
}

func experimentalAssistantEvent(text string) *session.Event {
	message := model.NewTextMessage(model.RoleAssistant, text)
	return &session.Event{Type: session.EventTypeAssistant, Message: &message}
}

type changingCheckpointCatalog struct {
	mu               sync.Mutex
	revision         int
	changeOnValidate bool
}

func (*changingCheckpointCatalog) Tools() []tool.Tool { return nil }
func (s *changingCheckpointCatalog) CatalogRevision() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprint(s.revision)
}
func (s *changingCheckpointCatalog) ValidateCatalog(ctx context.Context, revision string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.changeOnValidate {
		s.revision++
		s.changeOnValidate = false
	}
	if revision != fmt.Sprint(s.revision) {
		s.mu.Unlock()
		return nil, tool.ErrCatalogSnapshotStale
	}
	return s.mu.Unlock, nil
}
func TestExperimentalAsyncCatalogChangeDiscardsCandidateAndContinues(t *testing.T) {
	for _, atCommit := range []bool{false, true} {
		for _, dynamic := range []bool{false, true} {
			t.Run(fmt.Sprintf("dynamic_%t/at_commit_%t", dynamic, atCommit), func(t *testing.T) {
				h := newAsyncHarness(t, "")
				source := &changingCheckpointCatalog{revision: 1}
				spec := agent.AgentSpec{Model: h.llm, Tools: []tool.Tool{h.tool}, DeferredTools: source}
				if dynamic {
					h.source.set(agent.ModelRequestSnapshot{Revision: "provider", Model: h.llm, Tools: []tool.Tool{h.tool}, DeferredTools: source})
					spec.ResolveModelRequest = h.source.resolve
				}
				run, err := h.rt.Run(h.ctx, agent.RunRequest{SessionRef: h.active.SessionRef, Input: "continue", AgentSpec: spec})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = run.Handle.Close(); _ = run.Handle.WaitCompletion(context.Background()) })
				next := h.startCandidate(t)
				h.completeCandidate(t, next.scope)
				source.mu.Lock()
				if atCommit {
					source.changeOnValidate = true
				} else {
					source.revision++
				}
				source.mu.Unlock()
				h.llm.responses <- asyncToolResponse("second")
				receiveSnapshotValue(t, h.secondTool)
				close(h.releaseSecond)
				h.llm.responses <- asyncTextResponse("done", 500)
				if err := run.Handle.WaitCompletion(t.Context()); err != nil {
					t.Fatalf("healthy catalog update killed Turn: %v", err)
				}
				if _, _, ok := compact.LatestCompactEvent(h.load(t).Events); ok {
					t.Fatal("old catalog candidate was adopted")
				}
				receiveSnapshotValue(t, h.llm.foreground)
			})
		}
	}
}

// A low watermark creates distinct healthy recovery cycles without a live model.
type progressingCompactModel struct{ work, summaries int }

func (*progressingCompactModel) Name() string             { return "progressing" }
func (*progressingCompactModel) ProviderName() string     { return "fixture" }
func (*progressingCompactModel) ContextWindowTokens() int { return 10000 }
func (*progressingCompactModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (m *progressingCompactModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		text := "finished"
		var calls []model.ToolCall
		if isRuntimeCompactionRequest(req) {
			m.summaries++
			text = asyncFixtureSummary
		} else if m.work < 8 {
			m.work++
			calls = []model.ToolCall{{ID: fmt.Sprint(m.work), Name: "work", Args: `{}`}}
		}
		message := model.NewTextMessage(model.RoleAssistant, text)
		if len(calls) > 0 {
			message = model.MessageFromToolCalls(model.RoleAssistant, calls, "")
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: message, TurnComplete: true, StepComplete: true}), nil)
	}
}
func TestExperimentalAsyncAllowsHealthyRepeatedRecovery(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			store, active := newTestSessionService(t, "progress")
			llm := &progressingCompactModel{}
			cfg := Config{Sessions: store, AgentFactory: chat.Factory{}, Compaction: CompactionConfig{Enabled: true, WatermarkRatio: .15, ForceWatermarkRatio: .2, ReserveOutputTokens: 10, SafetyMarginTokens: 10, Async: AsyncCompactionConfig{Enabled: enabled}}}
			rt, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			work := tool.NamedTool{Def: tool.Definition{Name: "work", EffectClass: tool.EffectReadOnly, InputSchema: map[string]any{"type": "object"}}, Invoke: func(_ context.Context, c tool.Call) (tool.Result, error) {
				return tool.Result{ID: c.ID, Name: c.Name, Content: []model.Part{model.NewTextPart(strings.Repeat("new independent evidence ", 500))}}, nil
			}}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "perform eight independent steps", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{work}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := drainRunnerEvents(t, run.Handle); err != nil {
				t.Fatal(err)
			}
			if llm.work != 8 || llm.summaries < 3 {
				t.Fatalf("work=%d summaries=%d", llm.work, llm.summaries)
			}
		})
	}
}

func TestExperimentalUnreadyCatalogPreservesExposureAcrossActivation(t *testing.T) {
	for _, dynamic := range []bool{false, true} {
		t.Run(fmt.Sprint(dynamic), func(t *testing.T) {
			remote := tool.NamedTool{Def: tool.Definition{Name: "remote__read", InputSchema: map[string]any{"type": "object"}, Metadata: map[string]any{tool.MetadataToolKind: tool.MetadataToolKindMCP, tool.MetadataMCPServer: "remote", tool.MetadataMCPTool: "read", tool.MetadataMCPSourceFingerprint: "stable-v1"}}}
			source := &runtimeDeferredSource{ready: []tool.Tool{remote}}
			search := toolsearch.NewSource(source, toolsearch.NewLexicalRanker())
			store, active := newTestSessionService(t, "unready")
			binding := tool.BindingForDefinition(remote.Definition())
			run := func(llm model.LLM, local ...tool.Tool) {
				t.Helper()
				rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, PolicyRegistry: retirementPolicy(), DefaultPolicyMode: "retirement-test"})
				if err != nil {
					t.Fatal(err)
				}
				tools := append([]tool.Tool{search}, local...)
				spec := agent.AgentSpec{Model: llm, Tools: tools, DeferredTools: source}
				if dynamic {
					spec.ResolveModelRequest = func(context.Context) (agent.ModelRequestSnapshot, error) {
						return agent.ModelRequestSnapshot{Model: llm, Tools: tools, DeferredTools: source}, nil
					}
				}
				result, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "continue", AgentSpec: spec})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := drainRunnerEvents(t, result.Handle); err != nil {
					t.Fatal(err)
				}
			}
			run(&retirementScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "discover", Name: tool.ToolSearchToolName, Args: `{"query":"read"}`}}
				}
				if i == 1 {
					return []model.ToolCall{{ID: "mark", Name: tool.RetireToolName, Args: retirementMarkArgs(binding)}}
				}
				return nil
			}})
			source.ready = nil
			local := tool.NamedTool{Def: tool.Definition{Name: "LocalRead", EffectClass: tool.EffectReadOnly, InputSchema: map[string]any{"type": "object"}}, Invoke: func(_ context.Context, c tool.Call) (tool.Result, error) {
				if c.ID == "ready" {
					source.ready = []tool.Tool{remote}
				}
				return tool.Result{ID: c.ID, Name: c.Name, Content: []model.Part{model.NewTextPart("local result")}}, nil
			}}
			run(&retirementScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
				hasRemote := false
				for _, def := range req.Tools {
					if def.Function != nil && def.Function.Name == binding.Name {
						hasRemote = true
					}
				}
				if hasRemote != (i == 2) {
					t.Errorf("request %d exposed unavailable or lost ready exact binding", i)
				}
				if i < 2 {
					id := "read"
					if i == 1 {
						id = "ready"
					}
					return []model.ToolCall{{ID: id, Name: "LocalRead", Args: `{}`}}
				}
				return nil
			}}, local)
			loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			state, ok := exposureFromEvents(loaded.Events)
			if !ok || !reflect.DeepEqual(state.Active, []tool.Binding{binding}) || !reflect.DeepEqual(state.Pending, []tool.Binding{binding}) {
				t.Fatalf("unready activation erased durable state: %+v", state)
			}
		})
	}
}

// A Source cannot grant checkpoint authority without holding a real guard.
type missingCatalogGuard struct{ changingCheckpointCatalog }

func (*missingCatalogGuard) ValidateCatalog(context.Context, string) (func(), error) { return nil, nil }
func TestExperimentalCatalogRequiresReleaseGuard(t *testing.T) {
	store, active := newTestSessionService(t, "missing-catalog-guard")
	appendTestEvent(t, store, active.SessionRef, userTextEvent("history"))
	appendTestEvent(t, store, active.SessionRef, experimentalAssistantEvent("answer"))
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := rt.Compact(t.Context(), CompactRequest{SessionRef: active.SessionRef, Model: &compactionRequestCaptureModel{}, DeferredTools: &missingCatalogGuard{}})
	if err == nil || result.Compacted {
		t.Fatalf("unguarded checkpoint admitted: %+v %v", result, err)
	}
	loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := compact.LatestCompactEvent(loaded.Events); ok {
		t.Fatal("checkpoint written without catalog guard")
	}
}
