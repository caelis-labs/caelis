package controlclient

import (
	"context"
	"fmt"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func BenchmarkSweepAbandonedApprovals(b *testing.B) {
	fixtures := []struct {
		name          string
		sessionCount  int
		eventsPerSess int
	}{
		{name: "empty", sessionCount: 0, eventsPerSess: 0},
		{name: "50_sessions_20_events", sessionCount: 50, eventsPerSess: 20},
		{name: "200_sessions_500_events", sessionCount: 200, eventsPerSess: 500},
		{name: "1_session_5000_events", sessionCount: 1, eventsPerSess: 5000},
	}
	for _, backend := range []string{"file", "memory"} {
		for _, fixture := range fixtures {
			b.Run(backend+"/"+fixture.name, func(b *testing.B) {
				service := benchmarkRecoveryService(b, backend, fixture.sessionCount, fixture.eventsPerSess)
				b.ReportMetric(float64(fixture.sessionCount), "sessions")
				b.ReportMetric(float64(fixture.sessionCount*fixture.eventsPerSess), "events")
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := SweepAbandonedApprovals(context.Background(), service); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkSweepAbandonedApprovalsPagedFallback(b *testing.B) {
	for _, fixture := range []struct {
		name          string
		sessionCount  int
		eventsPerSess int
	}{
		{name: "200_sessions_500_events", sessionCount: 200, eventsPerSess: 500},
		{name: "1_session_5000_events", sessionCount: 1, eventsPerSess: 5000},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			service := benchmarkRecoveryService(b, "file", fixture.sessionCount, fixture.eventsPerSess)
			paged := pagedRecoveryStore{store: service}
			b.ReportMetric(float64(fixture.sessionCount), "sessions")
			b.ReportMetric(float64(fixture.sessionCount*fixture.eventsPerSess), "events")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := SweepAbandonedApprovals(context.Background(), paged); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// pagedRecoveryStore intentionally hides ApprovalRecoveryReader so the
// compatibility paging path remains benchmarked and regression-covered.
type pagedRecoveryStore struct {
	store ApprovalRecoveryStore
}

func (s pagedRecoveryStore) ListSessions(ctx context.Context, req session.ListSessionsRequest) (session.SessionList, error) {
	return s.store.ListSessions(ctx, req)
}

func (s pagedRecoveryStore) EventsPage(ctx context.Context, req session.EventPageRequest) (session.EventPage, error) {
	return s.store.EventsPage(ctx, req)
}

func (s pagedRecoveryStore) Session(ctx context.Context, ref session.SessionRef) (session.Session, error) {
	return s.store.Session(ctx, ref)
}

func (s pagedRecoveryStore) SettlePendingApproval(
	ctx context.Context,
	req session.SettlePendingApprovalRequest,
) (session.SettlePendingApprovalResult, error) {
	return s.store.SettlePendingApproval(ctx, req)
}

type recoveryBenchmarkService interface {
	ApprovalRecoveryStore
	StartSession(context.Context, session.StartSessionRequest) (session.Session, error)
	AppendEvents(context.Context, session.AppendEventsRequest) ([]*session.Event, error)
}

func benchmarkRecoveryService(b *testing.B, backend string, sessionCount int, eventsPerSession int) recoveryBenchmarkService {
	b.Helper()
	nextID := 0
	idGenerator := func() string {
		nextID++
		return fmt.Sprintf("session-%04d", nextID)
	}
	var service recoveryBenchmarkService
	switch backend {
	case "file":
		service = sessionfile.NewStore(sessionfile.Config{
			RootDir:            b.TempDir(),
			SessionIDGenerator: idGenerator,
		})
	case "memory":
		service = sessionmemory.NewStore(sessionmemory.Config{
			SessionIDGenerator: idGenerator,
		})
	default:
		b.Fatalf("unknown backend %q", backend)
	}

	ctx := context.Background()
	events := make([]*session.Event, eventsPerSession)
	for i := range events {
		events[i] = &session.Event{
			Type:       session.EventTypeLifecycle,
			Visibility: session.VisibilityCanonical,
			Lifecycle:  &session.EventLifecycle{Status: "completed", Reason: "performance_fixture"},
		}
	}
	for i := 0; i < sessionCount; i++ {
		active, err := service.StartSession(ctx, session.StartSessionRequest{
			AppName: "caelis",
			UserID:  "performance-user",
			Workspace: session.WorkspaceRef{
				Key: "performance-workspace",
				CWD: "/tmp/performance-workspace",
			},
			Title: fmt.Sprintf("fixture %04d", i),
		})
		if err != nil {
			b.Fatal(err)
		}
		if len(events) == 0 {
			continue
		}
		if _, err := service.AppendEvents(ctx, session.AppendEventsRequest{
			SessionRef: active.SessionRef,
			Events:     events,
		}); err != nil {
			b.Fatal(err)
		}
	}
	return service
}
