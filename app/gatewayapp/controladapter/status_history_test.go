package controladapter

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

type statusHistoryReader struct {
	*inmemory.Store
	reads int
}

func (r *statusHistoryReader) Events(ctx context.Context, req session.EventsRequest) ([]*session.Event, error) {
	r.reads++
	return r.Store.Events(ctx, req)
}

func (r *statusHistoryReader) EventsPage(ctx context.Context, req session.EventPageRequest) (session.EventPage, error) {
	r.reads++
	return r.Store.EventsPage(ctx, req)
}

func TestStatusSharesParentHistoryCutAndRefreshesUsage(t *testing.T) {
	store := &statusHistoryReader{Store: inmemory.NewStore(inmemory.Config{})}
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	appendUsage := func(id string, tokens int) {
		message := model.NewTextMessage(model.RoleAssistant, "result")
		_, err := store.AppendEvent(t.Context(), session.AppendEventRequest{
			SessionRef: active.SessionRef,
			Event: &session.Event{
				ID: id, Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical,
				Message: &message, Invocation: &session.EventInvocation{Provider: "fixture", Model: "main"},
				Meta: map[string]any{"prompt_tokens": tokens, "total_tokens": tokens},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	appendUsage("first", 10)
	readsAtProjection := 0
	deps := &runtimeDeps{
		Session: SessionRuntimeDeps{Store: store},
		Model: ModelRuntimeDeps{
			SessionUsageSnapshotFn: func(_ context.Context, _ session.SessionRef, _ string, projection *runtime.ContextUsageProjection) (compact.UsageSnapshot, error) {
				readsAtProjection = store.reads
				return projection.Snapshot(128000, runtime.CompactionConfig{}, "fixture", "main"), nil
			},
		},
	}
	driver, err := newAssemblerForSession(t.Context(), deps, active, "tui", "")
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range []int{10, 30} {
		if index == 1 {
			appendUsage("second", 20)
		}
		events, err := store.Store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
		if err != nil {
			t.Fatal(err)
		}
		expectedContext := runtime.ComputeUsageSnapshotForModel(events, nil, 128000, runtime.CompactionConfig{}, "fixture", "main")
		result, err := driver.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if store.reads != index+1 || readsAtProjection != store.reads {
			t.Fatalf("status read history %d times; context read at %d", store.reads, readsAtProjection)
		}
		if result.Usage.TotalTokens != expectedContext.TotalTokens || result.Usage.SessionUsageTotal.TotalTokens != want || len(result.Usage.SessionUsageByModel) != 1 {
			t.Fatalf("usage=%+v, want context and total %d", result.Usage, want)
		}
	}
}
