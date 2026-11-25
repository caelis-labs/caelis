package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/plan"
)

func seedOrphanPlan(t *testing.T, files *sessionfile.Store, active session.Session, owner string, fact bool) map[string]any {
	t.Helper()
	value := map[string]any{"version": 1, "entries": []any{map[string]any{"content": "orphan work", "status": "in_progress"}}}
	if owner != "" {
		value["turn_id"] = owner
	}
	if fact {
		_, err := files.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{ID: "original-plan", Type: session.EventTypePlan, Visibility: session.VisibilityCanonical, Scope: &session.EventScope{TurnID: "old-turn"}, PlanPayload: &session.EventPlanPayload{Entries: []session.EventPlanEntry{{Content: "orphan work", Status: "in_progress"}}}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := files.UpdateState(t.Context(), session.UpdateStateRequest{SessionRef: active.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeConfiguration), Update: func(s map[string]any) (map[string]any, error) { s["plan"] = value; return s, nil }})
	if err != nil {
		t.Fatal(err)
	}
	state, err := files.SnapshotState(t.Context(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	return state["plan"].(map[string]any)
}

// Observe the real input append boundary, before any model/compaction work.
type planInputStore struct{ *sessionfile.Store }

func (s *planInputStore) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if req.Event.Type == session.EventTypeUser {
		state, err := s.SnapshotState(ctx, req.SessionRef)
		if err != nil {
			return nil, err
		}
		if state["plan"] != nil {
			return nil, errors.New("new input reached Store before orphan Plan cleanup")
		}
	}
	return s.Store.AppendEvent(ctx, req)
}

func TestTurnPlanFileReopenReconcilesBeforeInputAndContinuity(t *testing.T) {
	for _, scenario := range []string{"owned", "legacy_fact", "legacy_state_only"} {
		t.Run(scenario, func(t *testing.T) {
			files, active, root := codeModeStore(t)
			owner := ""
			if scenario == "owned" {
				owner = "old-turn"
			}
			seedOrphanPlan(t, files, active, owner, scenario != "legacy_state_only")
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
			rt, err := New(Config{Sessions: &planInputStore{Store: reopened}, AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}
			driver := &recoveryCaptureModel{}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, TurnID: "new-turn", Input: "new work", AgentSpec: agent.AgentSpec{Model: driver}})
			if err != nil {
				t.Fatal(err)
			}
			if err = run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			state, err := reopened.SnapshotState(t.Context(), active.SessionRef)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := state["plan"]; ok || activePlanContinuity(state) != nil {
				t.Fatalf("orphan remains: %+v", state)
			}
			// The model/continuity snapshot is captured after cleanup, before this Turn.
			for _, message := range driver.messages {
				for _, part := range message.Parts {
					if part.Text != nil && strings.Contains(part.Text.Text, "Current Plan And Progress") {
						t.Fatal("old active plan reached new model context")
					}
				}
			}
			again := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
			assertSubagentSagaModelRoundTrip(t, again, active.SessionRef)
			state, err = again.SnapshotState(t.Context(), active.SessionRef)
			if err != nil || state["plan"] != nil {
				t.Fatalf("cleanup lost on reopen: %v %v", state, err)
			}
		})
	}
}

type orphanPlanFaultStore struct {
	*sessionfile.Store
	mode     string
	attempts []session.AppendEventsAndUpdateStateRequest
}

func (s *orphanPlanFaultStore) AppendEventsAndUpdateState(ctx context.Context, req session.AppendEventsAndUpdateStateRequest) ([]*session.Event, error) {
	if !strings.HasPrefix(req.TransactionID, "turn-plan:") {
		return s.Store.AppendEventsAndUpdateState(ctx, req)
	}
	s.attempts = append(s.attempts, req)
	if s.mode == "failure" {
		return nil, errors.New("fixture: cleanup unavailable")
	}
	if len(s.attempts) == 1 && (s.mode == "cas" || s.mode == "changed_plan") {
		_, err := s.UpdateState(ctx, session.UpdateStateRequest{SessionRef: req.SessionRef, MutationGuard: req.MutationGuard, Update: func(state map[string]any) (map[string]any, error) {
			state["unrelated"] = "preserved"
			if s.mode == "changed_plan" {
				state["plan"] = map[string]any{"turn_id": "another-turn", "entries": []any{map[string]any{"content": "another plan", "status": "in_progress"}}}
			}
			return state, nil
		}})
		if err != nil {
			return nil, err
		}
		return nil, session.ErrRevisionConflict
	}
	return s.Store.AppendEventsAndUpdateState(ctx, req)
}
func TestTurnPlanCleanupCASAndFailureRetryOriginalIdentity(t *testing.T) {
	for _, mode := range []string{"cas", "failure", "changed_plan"} {
		t.Run(mode, func(t *testing.T) {
			files, active, _ := codeModeStore(t)
			original := seedOrphanPlan(t, files, active, "old-turn", true)
			store := &orphanPlanFaultStore{Store: files, mode: mode}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}
			driver := &recoveryCaptureModel{}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, TurnID: "new-turn", Input: "must wait for cleanup", AgentSpec: agent.AgentSpec{Model: driver}})
			if mode == "cas" {
				if err != nil {
					t.Fatal(err)
				}
				if err = run.Handle.WaitCompletion(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("cleanup failure admitted new input")
				}
				events, loadErr := files.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				for _, e := range events {
					if e.Type == session.EventTypeUser {
						t.Fatal("new input appended before cleanup")
					}
				}
				if len(driver.messages) != 0 {
					t.Fatal("model ran before cleanup")
				}
				state, loadErr := files.SnapshotState(t.Context(), active.SessionRef)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				if mode == "changed_plan" {
					if state["plan"].(map[string]any)["turn_id"] != "another-turn" {
						t.Fatal("replaced plan cleared")
					}
					return
				}
				if !reflect.DeepEqual(state["plan"], original) {
					t.Fatal("failed cleanup changed original Plan")
				}
				store.mode = ""
				run, err = rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, TurnID: "retry-turn", Input: "retry original cleanup", AgentSpec: agent.AgentSpec{Model: driver}})
				if err != nil {
					t.Fatal(err)
				}
				if err = run.Handle.WaitCompletion(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if len(store.attempts) != 2 || store.attempts[0].TransactionID != store.attempts[1].TransactionID || store.attempts[0].MutationDigest != store.attempts[1].MutationDigest {
				t.Fatal("cleanup retry changed original Plan identity")
			}
			for _, req := range store.attempts {
				if req.ExpectedRevision == nil || req.MutationGuard.FenceID == "" {
					t.Fatal("cleanup lacks original cut/authority")
				}
			}
			state, err := files.SnapshotState(t.Context(), active.SessionRef)
			if err != nil || state["plan"] != nil {
				t.Fatalf("cleanup not committed: %v %v", state, err)
			}
			if mode == "cas" && state["unrelated"] != "preserved" {
				t.Fatal("CAS retry lost unrelated state")
			}
		})
	}
}

type planWithoutFence struct{ session.Service }

func TestTurnPlanCleanupRejectsMissingFence(t *testing.T) {
	files, active, _ := codeModeStore(t)
	original := seedOrphanPlan(t, files, active, "", false)
	rt, err := New(Config{Sessions: planWithoutFence{Service: files}, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "do not append", AgentSpec: agent.AgentSpec{Model: &recoveryCaptureModel{}}}); err == nil {
		t.Fatal("unowned Plan cleanup admitted")
	}
	state, err := files.SnapshotState(t.Context(), active.SessionRef)
	if err != nil || !reflect.DeepEqual(state["plan"], original) {
		t.Fatalf("unowned Plan changed: %v %v", state, err)
	}
}

func TestTurnPlanActiveProducerNeverCleanedByAnotherRuntime(t *testing.T) {
	files, active, _ := codeModeStore(t)
	started, release := make(chan struct{}), make(chan struct{})
	var released atomic.Bool
	unblock := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer unblock()
	slow := tool.NamedTool{Def: tool.Definition{Name: "Slow"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		close(started)
		<-release
		return tool.Result{}, nil
	}}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "plan", Name: plan.ToolName, Args: `{"entries":[{"content":"active plan","status":"in_progress"}]}`}, {ID: "slow", Name: "Slow", Args: "{}"}}
		}
		return nil
	}}
	rt, _ := New(Config{Sessions: files, AgentFactory: chat.Factory{}})
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, TurnID: "live-turn", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{plan.New(), slow}}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	state, err := files.SnapshotState(t.Context(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	original := session.CloneState(state)
	other, _ := New(Config{Sessions: files, AgentFactory: chat.Factory{}})
	if _, err = other.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "cannot acquire", AgentSpec: agent.AgentSpec{Model: &recoveryCaptureModel{}}}); err == nil {
		t.Fatal("another Runtime cleaned a live producer")
	}
	state, err = files.SnapshotState(t.Context(), active.SessionRef)
	if err != nil || !reflect.DeepEqual(state, original) {
		t.Fatalf("live Plan changed: %v %v", state, err)
	}
	unblock()
	if err = run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
}
