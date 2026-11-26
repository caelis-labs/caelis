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
			var service session.ManagedService
			if storeKind == "file" {
				service = newLogicalFileStore(t, sessionfile.Config{RootDir: t.TempDir()})
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

			result := &session.Event{
				IdempotencyKey: "tool_result:run-plan:turn-plan:1:plan-call",
				Scope:          &session.EventScope{TurnID: "turn-plan"},
			}
			update := plan.Args{
				Entries:     []plan.Entry{{Content: "Inspect", Status: plan.StatusCompleted}},
				Explanation: "explanation one",
			}
			if _, err := core.commitPlan(t.Context(), active.SessionRef, "turn-plan", result, update); err != nil {
				t.Fatalf("commitPlan(first) = %v", err)
			}
			if _, err := core.commitPlan(t.Context(), active.SessionRef, "turn-plan", result, update); err != nil {
				t.Fatalf("commitPlan(idempotent retry) = %v", err)
			}
			update.Explanation = "different explanation"
			_, err = core.commitPlan(t.Context(), active.SessionRef, "turn-plan", result, update)
			var conflict *session.EventConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("commitPlan(changed explanation) = %v; want EventConflictError", err)
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
