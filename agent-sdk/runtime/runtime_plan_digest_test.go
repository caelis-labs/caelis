package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/plan"
)

func TestPlanCompoundDigestIncludesPersistedExplanation(t *testing.T) {
	t.Parallel()

	for _, storeKind := range []string{"memory", "file"} {
		storeKind := storeKind
		t.Run(storeKind, func(t *testing.T) {
			t.Parallel()
			var service session.Service
			if storeKind == "file" {
				service = sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
			} else {
				service = inmemory.NewStore(inmemory.Config{})
			}
			active, err := service.StartSession(context.Background(), session.StartSessionRequest{
				AppName: "caelis", UserID: "plan-digest", PreferredSessionID: "plan-digest-" + storeKind,
			})
			if err != nil {
				t.Fatal(err)
			}
			core, err := New(Config{Sessions: service, AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}

			first := planDigestToolResult("explanation one")
			if _, handled, err := core.handlePlanEvent(context.Background(), active.SessionRef, "turn-plan", first); err != nil || !handled {
				t.Fatalf("handlePlanEvent(first) = handled %v, error %v", handled, err)
			}
			if _, handled, err := core.handlePlanEvent(context.Background(), active.SessionRef, "turn-plan", planDigestToolResult("explanation one")); err != nil || !handled {
				t.Fatalf("handlePlanEvent(idempotent retry) = handled %v, error %v", handled, err)
			}
			_, handled, err := core.handlePlanEvent(context.Background(), active.SessionRef, "turn-plan", planDigestToolResult("different explanation"))
			var conflict *session.EventConflictError
			if !handled || !errors.As(err, &conflict) {
				t.Fatalf("handlePlanEvent(changed explanation) = handled %v, error %v; want EventConflictError", handled, err)
			}
			state, err := service.SnapshotState(context.Background(), active.SessionRef)
			if err != nil {
				t.Fatal(err)
			}
			persistedPlan, _ := state["plan"].(map[string]any)
			if persistedPlan["explanation"] != "explanation one" {
				t.Fatalf("persisted plan = %#v, changed retry must not replace state", persistedPlan)
			}
		})
	}
}

func planDigestToolResult(explanation string) *session.Event {
	return &session.Event{
		IdempotencyKey: "tool_result:run-plan:turn-plan:1:plan-call",
		Type:           session.EventTypeToolResult,
		Visibility:     session.VisibilityCanonical,
		Scope:          &session.EventScope{TurnID: "turn-plan"},
		Tool: &session.EventTool{
			ID: "plan-call", Name: plan.ToolName, Status: "completed",
			Output: map[string]any{
				"entries":     []map[string]any{{"content": "Inspect", "status": "completed"}},
				"explanation": explanation,
			},
		},
	}
}
