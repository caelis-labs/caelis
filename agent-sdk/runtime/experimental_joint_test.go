package runtime

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

// Real stdio transport and effects, with a deliberately blocked synthetic summary.
// The scheduler, discovery, canonical checkpoint writer and replay are production.
func TestExperimentalAsyncMCPJointCheckpoint(t *testing.T) {
	for _, scenario := range []string{"retire", "late_mark", "reuse_tail", "cas_reuse", "unknown_commit", "failed_commit"} {
		t.Run(scenario, func(t *testing.T) {
			h := newAsyncHarness(t, "", session.WorkspaceRef{Key: "joint", CWD: t.TempDir()})
			receipts := filepath.Join(t.TempDir(), "receipts")
			manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "joint", Name: "local", Command: os.Args[0], WorkDir: t.TempDir(), Args: []string{"-test.run=^TestRetirementMCPServerProcess$"}, Env: map[string]string{"CAELIS_RETIREMENT_SERVER": "1", "CAELIS_RETIREMENT_RECEIPTS": receipts}}}, func(f mcp.ServerFailure) { t.Logf("MCP failure: %v", f.Err) })
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			<-manager.Initialized()
			ready := manager.Tools()
			if len(ready) != 1 {
				t.Fatal(manager.GetServerInfos("joint"))
			}
			binding := tool.BindingForDefinition(ready[0].Definition())
			search := toolsearch.NewSource(manager, toolsearch.NewLexicalRanker())
			h.rt.compaction.Async.Enabled = false
			h.rt.policies = retirementPolicy()
			h.rt.defaultPolicyMode = "retirement-test"
			// Use the same policy setup as the existing real-stdio acceptance.
			approvals := 0
			approve := approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
				approvals++
				return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
			})
			script := &retirementScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				switch i {
				case 0:
					return []model.ToolCall{{ID: "discover", Name: tool.ToolSearchToolName, Args: `{"query":"receipt"}`}}
				case 1:
					return []model.ToolCall{{ID: "effect", Name: binding.Name, Args: `{}`}}
				case 2:
					if scenario != "late_mark" {
						return []model.ToolCall{{ID: "mark", Name: tool.RetireToolName, Args: retirementMarkArgs(binding)}}
					}
				}
				return nil
			}}
			seeded, err := h.rt.Run(h.ctx, agent.RunRequest{SessionRef: h.active.SessionRef, Input: "discover and mark", AgentSpec: agent.AgentSpec{Model: script, Tools: []tool.Tool{search}, DeferredTools: manager}, ApprovalRequester: approve})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := drainRunnerEvents(t, seeded.Handle); err != nil {
				t.Fatal(err)
			}
			if approvals != 1 {
				for _, event := range h.load(t).Events {
					if event.Tool != nil {
						t.Logf("tool=%+v", event.Tool)
					}
				}
				t.Fatalf("approvals=%d requests=%d events=%+v", approvals, len(script.requests), h.load(t).Events)
			}
			h.rt.compaction.Async.Enabled = true
			h.source.set(agent.ModelRequestSnapshot{Revision: "joint-1", Model: h.llm, Tools: []tool.Tool{h.tool, search}, DeferredTools: manager})
			run, err := h.rt.Run(h.ctx, agent.RunRequest{SessionRef: h.active.SessionRef, Input: "continue fixture", AgentSpec: agent.AgentSpec{ResolveModelRequest: h.source.resolve}, ApprovalRequester: approve})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = run.Handle.Close()
				h.llm.summaries <- asyncTextResponse(asyncFixtureSummary, 2100)
				_ = run.Handle.WaitCompletion(context.Background())
			})
			next := h.startCandidate(t)
			if scenario == "reuse_tail" || scenario == "late_mark" {
				call := model.ToolCall{ID: "tail-mark", Name: tool.RetireToolName, Args: retirementMarkArgs(binding)}
				if scenario == "reuse_tail" {
					call = model.ToolCall{ID: "tail-effect", Name: binding.Name, Args: `{}`}
				}
				h.llm.responses <- snapshotResponse{response: &model.Response{Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, ""), TurnComplete: true, StepComplete: true}}
				receiveSnapshotValue(t, h.llm.foreground)
			}
			// The summary can be ready while exposure is still live.
			h.completeCandidate(t, next.scope)
			before := h.load(t)
			exposure, _ := exposureFromEvents(before.Events)
			if len(exposure.Active) != 1 {
				t.Fatal("ready candidate unloaded tool")
			}
			if scenario == "cas_reuse" {
				var once sync.Once
				h.store.beforeCheckpoint = func() {
					once.Do(func() {
						latest, err := h.store.LoadSession(h.ctx, session.LoadSessionRequest{SessionRef: h.active.SessionRef})
						if err != nil {
							t.Error(err)
							return
						}
						state, _ := exposureFromEvents(latest.Events)
						state.Pending = nil
						event := userTextEvent("new canonical tail cancels retirement mark")
						event.Meta = map[string]any{tool.MetadataToolExposure: state}
						_, err = h.store.Store.AppendEvent(h.ctx, session.AppendEventRequest{SessionRef: h.active.SessionRef, MutationGuard: session.RuntimeMutationGuard(h.ctx), Event: event})
						if err != nil {
							t.Error(err)
						}
					})
				}
			}
			if scenario == "unknown_commit" {
				h.store.fault = "after"
			}
			if scenario == "failed_commit" {
				h.store.fault = "before"
			}
			// Finish a second tool batch to reach the next safe model-request boundary.
			h.llm.responses <- asyncToolResponse("second")
			receiveSnapshotValue(t, h.secondTool)
			close(h.releaseSecond)
			if scenario == "failed_commit" {
				if err := run.Handle.WaitCompletion(t.Context()); err == nil {
					t.Fatal("failed checkpoint reported success")
				}
				current := h.load(t)
				state, _ := exposureFromEvents(current.Events)
				if len(state.Active) != 1 {
					t.Fatal("failed checkpoint retired")
				}
				return
			}
			after := receiveSnapshotValue(t, h.llm.foreground).request
			if !strings.Contains(asyncMessagesText(after.Messages), "CONTEXT CHECKPOINT") {
				t.Fatal("scheduler did not adopt")
			}
			h.llm.responses <- asyncTextResponse("complete", 500)
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			loaded := h.load(t)
			event, _, ok := compact.LatestCompactEvent(loaded.Events)
			if !ok {
				t.Fatal("checkpoint absent")
			}
			state, _ := tool.ExposureFromMetadata(event.Meta)
			wantActive := 0
			if scenario == "reuse_tail" || scenario == "cas_reuse" {
				wantActive = 1
			}
			if len(state.Active) != wantActive {
				t.Fatalf("exposure=%+v want active=%d", state, wantActive)
			}
			count := 0
			for _, e := range loaded.Events {
				if compact.IsCompactEvent(e) {
					count++
				}
			}
			if count != 1 || event.ID != next.scope.job.id {
				t.Fatal("checkpoint identity replayed")
			}
			raw, err := os.ReadFile(receipts)
			if err != nil {
				t.Fatal(err)
			}
			wantEffects := 1
			if scenario == "reuse_tail" {
				wantEffects = 2
			}
			if strings.Count(string(raw), "receipt\n") != wantEffects {
				t.Fatalf("effects=%s", raw)
			}
			restarted := sessionfile.NewStore(sessionfile.Config{RootDir: h.root})
			replay, err := restarted.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: h.active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(loaded.Events, replay.Events) {
				t.Fatal("restart lost/duplicated canonical events")
			}
			// Full request equality is covered by the existing async and stdio replay
			// tests; this projection also verifies the joint exposure checkpoint.
			if !reflect.DeepEqual(promptEventsWithToolVisibilityMetadata(compact.PromptEventsFromLatestCompact(loaded.Events), loaded.Events), promptEventsWithToolVisibilityMetadata(compact.PromptEventsFromLatestCompact(replay.Events), replay.Events)) {
				t.Fatal("joint replay projection differs")
			}
			t.Logf("scenario=%s checkpoints=%d effects=%d active=%d", scenario, count, wantEffects, wantActive)
		})
	}
}
