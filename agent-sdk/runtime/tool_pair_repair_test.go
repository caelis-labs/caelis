package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/plan"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
)

type factResultFault struct {
	*inmemory.Store
	kind   string
	failed atomic.Bool
}

func (s *factResultFault) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if req.Event.Type == session.EventTypeToolResult && req.Event.Tool != nil && req.Event.Tool.ID == "B" && s.failed.CompareAndSwap(false, true) {
		if s.kind == "absent" {
			return nil, errors.New("fixture: result write failed before commit")
		}
		if s.kind == "different" {
			req.Event = session.CloneEvent(req.Event)
			req.Event.Tool.Error = "different full payload"
		}
		persisted, err := s.Store.AppendEvent(ctx, req)
		if err != nil {
			return persisted, err
		}
		if s.kind == "warning" {
			return persisted, &session.CommittedError{Err: errors.New("fixture: committed report failure")}
		}
		return nil, errors.New("fixture: unknown append report")
	}
	return s.Store.AppendEvent(ctx, req)
}

type factSerialModel struct{ model.LLM }

func (m factSerialModel) Capabilities() model.Capabilities {
	caps, _ := model.CapabilitiesOf(m.LLM)
	caps.ParallelToolCalls = false
	return caps
}

func TestToolFactsResultWriteNeverReplaysEffect(t *testing.T) {
	for _, kind := range []string{"absent", "warning", "unknown", "different"} {
		t.Run(kind, func(t *testing.T) {
			base, active := newToolFactTestSession(t, "result-fault-"+kind)
			store := &factResultFault{Store: base, kind: kind}
			counts := map[string]int{}
			var tools []tool.Tool
			for _, name := range []string{"A", "B", "C"} {
				name := name
				tools = append(tools, tool.NamedTool{Def: tool.Definition{Name: name, InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
					counts[name]++
					return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: json.RawMessage(`{"integer":9007199254740993123456789,"body":"complete"}`)}}}, nil
				}})
			}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i > 0 {
					return nil
				}
				return []model.ToolCall{{ID: "A", Name: "A", Args: "{}"}, {ID: "B", Name: "B", Args: "{}"}, {ID: "C", Name: "C", Args: "{}"}}
			}}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "one batch", AgentSpec: agent.AgentSpec{Model: factSerialModel{driver}, Tools: tools}})
			if err != nil {
				t.Fatal(err)
			}
			runErr := run.Handle.WaitCompletion(t.Context())
			failed := kind == "absent" || kind == "different"
			if failed != (runErr != nil) {
				t.Fatalf("completion=%v", runErr)
			}
			wantC := 1
			if failed {
				wantC = 0
			}
			if counts["A"] != 1 || counts["B"] != 1 || counts["C"] != wantC {
				t.Fatalf("effects=%v", counts)
			}
			before, err := base.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			next, err := New(Config{Sessions: base, AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}
			fence, err := base.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "repair"})
			if err != nil {
				t.Fatal(err)
			}
			ctx := session.ContextWithRuntimeFence(t.Context(), fence)
			if err := next.repairToolPairs(ctx, active.SessionRef); err != nil {
				t.Fatal(err)
			}
			after, _ := base.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
			if err := next.repairToolPairs(ctx, active.SessionRef); err != nil {
				t.Fatal(err)
			}
			again, _ := base.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
			if !reflect.DeepEqual(after, again) {
				t.Fatal("repeat repair changed facts")
			}
			for _, event := range before {
				if event.Type == session.EventTypeToolResult {
					found := false
					for _, current := range after {
						if current.ID == event.ID {
							found = reflect.DeepEqual(current, event)
						}
					}
					if !found {
						t.Fatal("existing complete result changed")
					}
				}
			}
			pairs, err := session.PairToolFacts(after)
			if err != nil || len(pairs) != 3 {
				t.Fatalf("pairs=%v err=%v", pairs, err)
			}
			for _, pair := range pairs {
				if pair.Result == nil {
					t.Fatal("missing result")
				}
			}
			if counts["C"] != wantC || counts["A"] != 1 || counts["B"] != 1 {
				t.Fatalf("repair dispatched effects: %v", counts)
			}
		})
	}
}
func TestToolFactsCancellationHoldsFenceUntilProducerEnds(t *testing.T) {
	store, active := newToolFactTestSession(t, "slow-tool")
	started := make(chan struct{})
	release := make(chan struct{})
	var effects atomic.Int32
	target := tool.NamedTool{Def: tool.Definition{Name: "Slow", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		effects.Add(1)
		close(started)
		<-release
		return tool.Result{Content: []tool.ContentBlock{tool.Text("complete late body")}}, nil
	}}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "slow", Name: "Slow", Args: "{}"}}
		}
		return nil
	}}
	rt, _ := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "slow", AgentSpec: agent.AgentSpec{Model: factImageModel{driver}, Tools: []tool.Tool{target}}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	run.Handle.Cancel()
	other, _ := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	_, err = other.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "blocked", AgentSpec: agent.AgentSpec{Model: &recoveryCaptureModel{}}})
	if !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("live producer allowed repair: %v", err)
	}
	close(release)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_ = run.Handle.WaitCompletion(ctx)
	events, _ := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	pairs, err := session.PairToolFacts(events)
	if err != nil || len(pairs) != 1 || pairs[0].Result == nil || effects.Load() != 1 {
		t.Fatalf("late known result/effect lost: %v %v %d", pairs, err, effects.Load())
	}
}
func TestToolFactsCompleteResultAndModelViewRoundTrip(t *testing.T) {
	for _, backend := range []string{"memory", "file"} {
		t.Run(backend, func(t *testing.T) {
			var store session.ManagedService
			var active session.Session
			var root string
			if backend == "memory" {
				store, active = newToolFactTestSession(t, "complete-roundtrip")
			} else {
				var files *sessionfile.Store
				files, active, root = codeModeStore(t)
				store = files
				root = filepath.Join(root, "sessions")
			}
			raw := json.RawMessage(`{"large":9007199254740993123456789,"decimal":1.00,"body":"` + strings.Repeat("body ", 10000) + `"}`)
			image := tool.Image{Data: []byte(strings.Repeat("abc", 8192)), MIMEType: "image/png", Name: "fixture"}
			full := tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}, image}}
			target := tool.NamedTool{Def: tool.Definition{Name: "Full", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { return full, nil }}
			var foreground []model.Message
			driver := &mcpScriptModel{steps: func(i int, request *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "same", Name: "Full", Args: `{"large":9007199254740993123456789}`}}
				}
				foreground = model.CloneMessages(request.Messages)
				return nil
			}}
			rt, _ := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "complete", AgentSpec: agent.AgentSpec{Model: factImageModel{driver}, Tools: []tool.Tool{target}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			var stored *tool.Result
			var final model.Message
			for _, event := range loaded.Events {
				if event.Type == session.EventTypeToolResult && event.Tool != nil {
					stored = event.Tool.Result
				}
				if event.Type == session.EventTypeAssistant && event.Message != nil {
					final = *event.Message
				}
			}
			if stored == nil || !reflect.DeepEqual(*stored, full) {
				t.Fatal("complete structured/media result changed or summarized")
			}
			if backend == "file" {
				store = sessionfile.NewStore(sessionfile.Config{RootDir: root})
			} else {
				rawEvents, err := json.Marshal(loaded.Events)
				if err != nil {
					t.Fatal(err)
				}
				var events []*session.Event
				if err := json.Unmarshal(rawEvents, &events); err != nil {
					t.Fatal(err)
				}
				reopened := inmemory.NewStore(inmemory.Config{SessionIDGenerator: func() string { return active.SessionID }})
				if _, err := reopened.StartSession(t.Context(), session.StartSessionRequest{AppName: active.AppName, UserID: active.UserID}); err != nil {
					t.Fatal(err)
				}
				if _, err := reopened.AppendEvents(t.Context(), session.AppendEventsRequest{SessionRef: active.SessionRef, Events: events}); err != nil {
					t.Fatal(err)
				}
				store = reopened
			}
			probe := &recoveryCaptureModel{}
			fresh, _ := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
			resumed, err := fresh.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, AgentSpec: agent.AgentSpec{Model: factImageModel{probe}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := resumed.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			want := append(model.CloneMessages(foreground), final)
			if !reflect.DeepEqual(probe.messages, want) {
				t.Fatalf("reopened model view differs: got=%d want=%d", len(probe.messages), len(want))
			}
			after, _ := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
			pairs, err := session.PairToolFacts(after)
			if err != nil || len(pairs) != 1 || !reflect.DeepEqual(pairs[0].Result.Tool.Result, stored) {
				t.Fatalf("complete result reopen: %v", err)
			}
		})
	}
}

type factImageModel struct{ model.LLM }

func (m factImageModel) Capabilities() model.Capabilities {
	c := model.Capabilities{ToolCalls: true}
	c.ImageInput = true
	return c
}

func TestToolFactsLegacyHalfBatchAndLateRepairRoundTrip(t *testing.T) {
	files, active, root := codeModeStore(t)
	calls := []model.ToolCall{{ID: "A", Name: "Write", Args: `{"large":9007199254740993123456789}`}, {ID: "B", Name: "Write", Args: `{}`}, {ID: "C", Name: "Write", Args: `{}`}}

	message := model.MessageFromToolCalls(model.RoleAssistant, calls, "")
	intent, err := files.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest), Event: &session.Event{ID: "legacy-batch", Type: session.EventTypeToolCall, Scope: &session.EventScope{TurnID: "old-turn"}, Message: &message, Tool: &session.EventTool{ID: "A", Name: "Write"}}})
	if err != nil {
		t.Fatal(err)
	}
	result := tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: []byte(`{"full":9007199254740993123456789}`)}}}
	original, err := files.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest), Event: &session.Event{ID: "original-A", Type: session.EventTypeToolResult, Scope: intent.Scope, Tool: &session.EventTool{ID: "A", Name: "Write", Result: &result}, Message: &model.Message{Role: model.RoleTool, Parts: []model.Part{{Kind: model.PartKindToolResult, ToolResult: &model.ToolResultPart{ToolUseID: "A", Name: "Write", Content: tool.ModelParts(result.Content)}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest), Event: &session.Event{ID: "later-user", Type: session.EventTypeUser, Message: ptrMessage(model.NewTextMessage(model.RoleUser, "later input"))}}); err != nil {
		t.Fatal(err)
	}
	existing, _ := files.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	for _, fact := range existing {
		if fact.ID == original.ID {
			original = fact
		}
	}
	rt, _ := New(Config{Sessions: files, AgentFactory: chat.Factory{}})
	probe := &recoveryCaptureModel{}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "resume", AgentSpec: agent.AgentSpec{Model: probe}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	events, err := files.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := session.PairToolFacts(events)
	if err != nil || len(pairs) != 3 {
		t.Fatalf("pairs=%d %v", len(pairs), err)
	}
	if !reflect.DeepEqual(pairs[0].Result, original) {
		t.Fatal("existing result changed")
	}
	for _, pair := range pairs[1:] {
		if pair.Result == nil || pair.Result.Tool.Outcome != session.ToolOutcomeUnknown || pair.Result.Tool.Result == nil {
			t.Fatal("legacy sibling not paired with complete interruption")
		}
	}
	if len(probe.messages) < 6 || len(probe.messages[0].ToolCalls()) != 3 || probe.messages[1].ToolResults()[0].ToolUseID != "A" || probe.messages[3].ToolResults()[0].ToolUseID != "C" {
		t.Fatalf("late result lost original batch: %+v", probe.messages)
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	next, _ := New(Config{Sessions: reopened, AgentFactory: chat.Factory{}})
	after := &recoveryCaptureModel{}
	run, err = next.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "again", AgentSpec: agent.AgentSpec{Model: after}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(probe.messages, after.messages[:len(probe.messages)]) {
		t.Fatal("complete old Session context changed after reopen")
	}
}

func TestToolFactsRepeatedProviderIDsAcrossBatchesAndTurns(t *testing.T) {
	store, active := newToolFactTestSession(t, "repeated-provider-id")
	var effects int
	target := tool.NamedTool{Def: tool.Definition{Name: "Write"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		effects++
		return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: factJSON(map[string]int{"effect": effects})}}}, nil
	}}
	rt, _ := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	for range 2 {
		driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
			if i < 2 {
				return []model.ToolCall{{ID: "same", Name: "Write", Args: `{}`}}
			}
			return nil
		}}
		run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "two batches", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{target}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := run.Handle.WaitCompletion(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	events, _ := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	pairs, err := session.PairToolFacts(events)
	if err != nil || len(pairs) != 4 || effects != 4 {
		t.Fatalf("pairs=%d effects=%d %v", len(pairs), effects, err)
	}
	seen := map[string]bool{}
	for i, pair := range pairs {
		key := pair.Call.Tool.Execution.TurnID + "/" + pair.Call.Tool.Execution.ItemID
		if seen[key] || pair.Result == nil || string(pair.Result.Tool.Result.Content[0].(tool.StructuredContent).Value) != string(factJSON(map[string]int{"effect": i + 1})) {
			t.Fatal("reused provider ID mixed occurrences")
		}
		seen[key] = true
	}
}

func TestToolFactsCheckpointCoverageDoesNotReviveCoveredCalls(t *testing.T) {
	files, active, root := codeModeStore(t)
	guard := session.ControlMutationGuard(session.ControlMutationPurposeTest)
	appendFact := func(event *session.Event) *session.Event {
		t.Helper()
		got, err := files.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, MutationGuard: guard, Event: event})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	call := func(id, turn, occurrence string) *session.Event {
		protocol := model.ToolCall{ID: id, Name: "Write", Args: `{}`}
		message := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{protocol}, "")
		return &session.Event{ID: occurrence, Type: session.EventTypeToolCall, Scope: &session.EventScope{TurnID: turn}, Message: &message, Tool: &session.EventTool{ID: id, Name: "Write", BatchID: occurrence, Execution: tool.InvocationContext{SessionID: active.SessionID, TurnID: turn, ItemID: occurrence}}}
	}
	covered := appendFact(call("same", "old", "covered-with-late-result"))
	missing := appendFact(call("covered-missing", "old", "covered-missing"))
	checkpoint := appendFact(&session.Event{ID: "checkpoint", Type: session.EventTypeCompact, Text: asyncFixtureSummary, Meta: map[string]any{session.CheckpointMetadataKey: session.CheckpointDataValue(session.CheckpointData{ContractVersion: 1, SummarizedThroughSeq: missing.Seq, SummarizedThroughID: missing.ID})}})
	appendFact(&session.Event{ID: "tail-user", Type: session.EventTypeUser, Message: ptrMessage(model.NewTextMessage(model.RoleUser, "tail"))})
	tail := appendFact(call("same", "tail-turn", "tail-occurrence"))
	late := session.CloneEvent(covered)
	late.ID = "late-covered-result"
	late.Seq = 0
	late.Type = session.EventTypeToolResult
	late.Message = ptrMessage(model.NewMessage(model.RoleTool, model.NewToolResultJSONPart("same", "Write", map[string]any{"covered": true}, false)))
	full := tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: late.Message.ToolResults()[0].Content[0].JSONValue()}}}
	late.Tool.Result = &full
	late.Tool.Outcome = session.ToolOutcomeSucceeded
	appendFact(late)
	probe := &recoveryCaptureModel{}
	rt, _ := New(Config{Sessions: files, AgentFactory: chat.Factory{}})
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "resume", AgentSpec: agent.AgentSpec{Model: probe}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	events, _ := files.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	pairs, err := session.PairToolFacts(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range pairs {
		switch pair.Call.ID {
		case missing.ID:
			if pair.Result != nil {
				t.Fatal("repair expanded checkpoint coverage")
			}
		case tail.ID:
			if pair.Result == nil || pair.Result.Tool.Outcome != session.ToolOutcomeUnknown {
				t.Fatal("uncovered call not repaired")
			}
		}
	}
	var results int
	for _, message := range probe.messages {
		results += len(message.ToolResults())
	}
	if results != 1 {
		t.Fatalf("covered orphan entered model tail: %d", results)
	}
	for _, event := range events {
		if event.ID == checkpoint.ID && string(factJSON(event)) != string(factJSON(checkpoint)) {
			t.Fatal("checkpoint changed during repair")
		}
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	next, _ := New(Config{Sessions: reopened, AgentFactory: chat.Factory{}})
	after := &recoveryCaptureModel{}
	run, err = next.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "again", AgentSpec: agent.AgentSpec{Model: after}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(probe.messages, after.messages[:len(probe.messages)]) {
		t.Fatal("checkpoint context changed after full reopen")
	}
}

type repeatedToolContinuation struct{}

func (repeatedToolContinuation) Invoke(ctx context.Context, _ hook.ToolContext, next hook.ToolNext) (tool.Result, error) {
	first, err := next(ctx)
	if err != nil {
		return first, err
	}
	_, err = next(ctx)
	return first, err
}

func TestToolFactsRepeatedHookContinuationCannotReplayEffect(t *testing.T) {
	store, active := newToolFactTestSession(t, "repeated-continuation")
	var effects atomic.Int32
	target := tool.NamedTool{Def: tool.Definition{Name: "Write"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		effects.Add(1)
		return tool.Result{Content: []tool.ContentBlock{tool.Text("full result")}}, nil
	}}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "same", Name: "Write", Args: `{}`}}
		}
		return nil
	}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolInvoke: []hook.ToolInvoke{{Name: "repeat", Apply: repeatedToolContinuation{}.Invoke}}}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "one effect", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{target}}})
	if err != nil {
		t.Fatal(err)
	}
	completionErr := run.Handle.WaitCompletion(t.Context())
	if effects.Load() != 1 {
		t.Fatalf("effects=%d", effects.Load())
	}
	events, _ := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	pairs, err := session.PairToolFacts(events)
	if err != nil || len(pairs) != 1 || pairs[0].Result == nil || pairs[0].Result.Tool.Result == nil {
		t.Fatalf("paired result lost: %v", err)
	}
	r := pairs[0].Result.Tool
	if completionErr == nil || r.Outcome != session.ToolOutcomeSucceeded || r.ProcessingFailure == nil || r.ProcessingFailure.Phase != "tool_invoke" || r.ProcessingFailure.Name != "repeat" || r.ID != "same" || r.Name != "Write" {
		t.Fatalf("actual identity/disposition lost: %+v completion=%v", r, completionErr)
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "full result") || !strings.Contains((model.Message{Parts: tool.ModelParts(r.Result.Content)}).TextContent(), "Do not repeat") {
		t.Fatalf("failed processing retained business result: %s", raw)
	}

}

type conflictingFactRepairStore struct {
	*inmemory.Store
	injected atomic.Bool
	attempts atomic.Int32
}

func (s *conflictingFactRepairStore) AppendEvents(ctx context.Context, req session.AppendEventsRequest) ([]*session.Event, error) {
	if req.ExpectedRevision != nil && len(req.Events) > 0 && req.Events[0].Type == session.EventTypeToolResult {
		s.attempts.Add(1)
		if s.injected.CompareAndSwap(false, true) {
			if _, err := s.AppendEvent(ctx, session.AppendEventRequest{SessionRef: req.SessionRef, MutationGuard: req.MutationGuard, Event: &session.Event{ID: "concurrent-receipt", Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal}}); err != nil {
				return nil, err
			}
		}
	}
	return s.Store.AppendEvents(ctx, req)
}
func TestToolFactsRepairRecomputesAfterRevisionConflict(t *testing.T) {
	base, active := newToolFactTestSession(t, "repair-cas")
	store := &conflictingFactRepairStore{Store: base}
	call := model.ToolCall{ID: "original-protocol-id", Name: "Write", Args: `{}`}
	message := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, "")
	if _, err := base.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest), Event: &session.Event{ID: "original-call", Type: session.EventTypeToolCall, Message: &message, Scope: &session.EventScope{TurnID: "old-turn"}, Tool: &session.EventTool{ID: call.ID, Name: call.Name, BatchID: "original-batch", Execution: tool.InvocationContext{SessionID: active.SessionID, TurnID: "old-turn", ItemID: "original-occurrence"}}}}); err != nil {
		t.Fatal(err)
	}
	fence, err := base.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "repair-owner"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := session.ContextWithRuntimeFence(t.Context(), fence)
	rt, _ := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err := rt.repairToolPairs(ctx, active.SessionRef); err != nil {
		t.Fatal(err)
	}
	if store.attempts.Load() != 2 {
		t.Fatalf("repair attempts=%d", store.attempts.Load())
	}
	events, _ := base.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef})
	pairs, err := session.PairToolFacts(events)
	if err != nil || len(pairs) != 1 || pairs[0].Result == nil || pairs[0].Result.Tool.ID != call.ID || pairs[0].Result.Tool.Execution.ItemID != "original-occurrence" {
		t.Fatalf("original pair lost: %+v %v", pairs, err)
	}
	if err := rt.repairToolPairs(ctx, active.SessionRef); err != nil {
		t.Fatal(err)
	}
	again, _ := base.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef})
	if !reflect.DeepEqual(events, again) {
		t.Fatal("repeat repair wrote another result")
	}
}

type planFinalizationStore struct {
	*inmemory.Store
	cleanup session.MutationGuard
}

func (s *planFinalizationStore) AppendEventsAndUpdateState(ctx context.Context, req session.AppendEventsAndUpdateStateRequest) ([]*session.Event, error) {
	if strings.HasPrefix(req.TransactionID, "turn-plan:") {
		s.cleanup = req.MutationGuard
	}
	return s.Store.AppendEventsAndUpdateState(ctx, req)
}
func TestToolFactsPlanClearsAfterActualDrainUnderOriginalFence(t *testing.T) {
	base, active := newToolFactTestSession(t, "plan-drain")
	store := &planFinalizationStore{Store: base}
	started, release := make(chan struct{}), make(chan struct{})
	var released atomic.Bool
	releaseOnce := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer releaseOnce()
	slow := tool.NamedTool{Def: tool.Definition{Name: "Slow"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		close(started)
		<-release
		return tool.Result{Content: []tool.ContentBlock{tool.Text("complete late result")}}, nil
	}}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return []model.ToolCall{{ID: "plan", Name: plan.ToolName, Args: `{"entries":[{"content":"Wait for producer","status":"in_progress"}]}`}}
		case 1:
			return []model.ToolCall{{ID: "slow", Name: "Slow", Args: `{}`}}
		default:
			return nil
		}
	}}
	rt, _ := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, TurnID: "owning-turn", Input: "drain plan", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{plan.New(), slow}}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	original, err := base.SessionFence(t.Context(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	run.Handle.Cancel()
	wait, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	err = run.Handle.WaitCompletion(wait)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("live producer finished before drain: %v", err)
	}
	state, _ := store.SnapshotState(t.Context(), active.SessionRef)
	if current, ok := state["plan"].(map[string]any); !ok || current["turn_id"] != "owning-turn" {
		t.Fatal("plan cleared before actual drain")
	}
	if _, err := base.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "another-owner"}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("original fence not retained: %v", err)
	}
	releaseOnce()
	if err := run.Handle.WaitCompletion(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("completion=%v", err)
	}
	state, _ = store.SnapshotState(t.Context(), active.SessionRef)
	if _, ok := state["plan"]; ok {
		t.Fatal("owning plan survived actual drain")
	}
	if store.cleanup.FenceID != original.FenceID || store.cleanup.OwnerID != original.OwnerID {
		t.Fatal("finalization replaced the original fence")
	}
}

func TestToolFactsRepeatedProviderIDsCreateDistinctCommandOperations(t *testing.T) {
	for _, approve := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "approval"}[approve], func(t *testing.T) {
			files, active, _ := codeModeStore(t)
			running := false
			backend := newCommandStartProbe(&yieldProbeSandboxSession{statusRunning: &running}, nil)
			registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
				if approve {
					return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{}}, nil
				}
				return policy.Decision{Action: policy.ActionAllow}, nil
			}}}
			rt, err := New(Config{Sessions: files, TaskStore: sessionfile.NewTaskStore(files), AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: registry}, DefaultPolicyMode: "mcp-test"})
			if err != nil {
				t.Fatal(err)
			}
			requester := approvalRequesterFunc(func(context.Context, approval.Request) (approval.Response, error) {
				return approval.Response{Approved: true, Outcome: "selected", OptionID: "allow_once"}, nil
			})
			for range 2 {
				driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
					if i < 2 {
						return []model.ToolCall{{ID: "same-provider-id", Name: shell.RunCommandToolName, Args: `{"command":"echo once","yield_time_ms":0}`}}
					}
					return nil
				}}
				run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "two occurrences", ApprovalRequester: requester, AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{mustRuntimeRunCommandTool(t, backend)}}})
				if err != nil {
					t.Fatal(err)
				}
				if err := run.Handle.WaitCompletion(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if backend.startCalls() != 4 {
				t.Fatalf("command effects=%d; provider ID reused an old operation", backend.startCalls())
			}
			entries, err := rt.tasks.store.ListSession(t.Context(), active.SessionRef)
			if err != nil || len(entries) != 4 {
				t.Fatalf("command entries=%d %v", len(entries), err)
			}
			seen := map[string]bool{}
			for _, entry := range entries {
				operation := taskSpecString(entry.Spec, "operation_id")
				if operation == "" || seen[operation] || taskSpecString(entry.Spec, "parent_call") != "same-provider-id" {
					t.Fatal("command occurrence or original protocol relation lost")
				}
				seen[operation] = true
			}
		})
	}
}

func TestResumeSkipsRoutedMCPHistoryFromV0841(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(map[bool]string{true: "completed", false: "interrupted"}[complete], func(t *testing.T) {
			root := t.TempDir()
			// Register the file locator, then reopen a routed batch in the v0.84.1 wire shape.
			seed := sessionfile.NewStore(sessionfile.Config{RootDir: root,
				SessionIDGenerator: func() string { return "routed-mcp" },
				Clock:              func() time.Time { return time.Unix(1770000000, 0).UTC() },
			})
			_, err := seed.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "reader", Workspace: session.WorkspaceRef{Key: "ws", CWD: "/workspace"}})
			if err != nil {
				t.Fatal(err)
			}
			for _, suffix := range []string{".json", ".events.jsonl"} {
				path := filepath.Join("ws", "2026", "02", "02", "rollout-2026-02-02T02-40-00-routed-mcp"+suffix)
				data, err := os.ReadFile(filepath.Join("testdata", "routed-mcp-v0.84.1", path))
				if err != nil {
					t.Fatal(err)
				}
				if suffix == ".events.jsonl" && !complete {
					call, _, ok := bytes.Cut(data, []byte{'\n'})
					if !ok {
						t.Fatal("missing routed call fixture")
					}
					data = append(call, '\n')
				}
				if err := os.WriteFile(filepath.Join(root, path), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			ref := session.SessionRef{SessionID: "routed-mcp"}
			ids := []string{"proposal"}
			if complete {
				ids = append(ids, "receipt")
			}
			for i, want := range ids {
				page, err := store.EventsPage(t.Context(), session.EventPageRequest{SessionRef: ref, AfterSeq: uint64(i), Limit: 1, Visibility: session.EventPageAllDurable})
				if err != nil || len(page.Events) != 1 || page.Events[0].ID != want || page.Events[0].Tool.Name != "docs__read" {
					t.Fatalf("page %d=%+v, %v", i, page, err)
				}
			}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}
			probe := &recoveryCaptureModel{}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: ref, Input: "continue", AgentSpec: agent.AgentSpec{Model: probe, Tools: []tool.Tool{
				tool.NamedTool{Def: tool.Definition{Name: "docs__read"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
					t.Error("resume dispatched a historical tool")
					return tool.Result{}, nil
				}},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, message := range probe.messages {
				if len(message.ToolCalls()) != 0 || len(message.ToolResults()) != 0 {
					t.Fatalf("retired routed batch reached the model: %+v", message)
				}
			}
			last := probe.messages[len(probe.messages)-1]
			if last.Role != model.RoleUser || last.TextContent() != "continue" {
				t.Fatalf("resumed model input = %+v", last)
			}
		})
	}
}

func TestResumeLegacyGroupedResults(t *testing.T) {
	for _, scenario := range []string{"complete", "missing-sibling", "previously-repaired"} {
		t.Run(scenario, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			appendFact := func(e *session.Event) {
				t.Helper()
				e.Scope = &session.EventScope{TurnID: "old-turn"}
				if _, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: e}); err != nil {
					t.Fatal(err)
				}
			}
			calls := []model.ToolCall{{ID: "a", Name: "Read", Args: "{}"}, {ID: "b", Name: "Read", Args: "{}"}}
			if scenario == "missing-sibling" {
				calls = append(calls, model.ToolCall{ID: "c", Name: "Read", Args: "{}"})
			}
			batch := model.MessageFromToolCalls(model.RoleAssistant, calls, "")
			appendFact(&session.Event{ID: "batch", Type: session.EventTypeToolCall, Message: &batch, Tool: &session.EventTool{ID: "a", Name: "Read"}})
			appendFact(&session.Event{ID: "b", Type: session.EventTypeToolCall, Tool: &session.EventTool{ID: "b", Name: "Read"}})
			original := model.NewMessage(model.RoleTool, model.NewToolResultJSONPart("b", "Read", map[string]any{"data": "B"}, true), model.NewToolResultJSONPart("a", "Read", map[string]any{"data": "A"}, false))
			appendFact(&session.Event{ID: "grouped", Type: session.EventTypeToolResult, Message: &original})
			wantRepairs := 0
			switch scenario {
			case "previously-repaired":
				for i, call := range calls {
					item := fmt.Sprintf("legacy:batch:%d", i)
					id := "interrupted:old-turn:" + item
					appendFact(&session.Event{ID: id, IdempotencyKey: id, Type: session.EventTypeToolResult, Tool: &session.EventTool{
						ID: call.ID, Name: call.Name, Execution: tool.InvocationContext{SessionID: active.SessionID, TurnID: "old-turn", ItemID: item},
						Outcome: session.ToolOutcomeUnknown, ErrorCode: "execution_interrupted",
					}})
				}
				wantRepairs = len(calls)
			case "missing-sibling":
				wantRepairs = 1
			}
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
			rt, err := New(Config{Sessions: reopened, AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}
			probe := &recoveryCaptureModel{}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "resume", AgentSpec: agent.AgentSpec{Model: probe}})
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			var results []model.Message
			for _, m := range probe.messages {
				if len(m.ToolResults()) > 0 {
					results = append(results, m)
				}
			}
			if len(results) == 0 || !reflect.DeepEqual(results[0].ToolResults(), original.ToolResults()) {
				t.Fatalf("resumed tool messages: %+v", results)
			}
			if scenario == "missing-sibling" {
				if len(results) != 2 || len(results[1].ToolResults()) != 1 || results[1].ToolResults()[0].ToolUseID != "c" || !results[1].ToolResults()[0].IsError {
					t.Fatalf("missing sibling's result: %+v", results)
				}
			} else if len(results) != 1 {
				t.Fatalf("unexpected extra outcomes: %+v", results)
			}
			events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			repairs := 0
			for _, event := range events {
				if event.Tool != nil && event.Tool.ErrorCode == "execution_interrupted" {
					repairs++
				}
			}
			if repairs != wantRepairs {
				t.Fatalf("durable interruptions = %d, want %d", repairs, wantRepairs)
			}
		})
	}
}
