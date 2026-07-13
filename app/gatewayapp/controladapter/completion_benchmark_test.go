package controladapter

import (
	"context"
	"fmt"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/ports/gateway"
)

type completionBenchmarkGateway struct {
	sessions session.Service
}

func (g completionBenchmarkGateway) ResumeSession(context.Context, gateway.ResumeSessionRequest) (session.LoadedSession, error) {
	return session.LoadedSession{}, nil
}

func (g completionBenchmarkGateway) ListSessions(ctx context.Context, req gateway.ListSessionsRequest) (session.SessionList, error) {
	return g.sessions.ListSessions(ctx, session.ListSessionsRequest{
		AppName:      req.AppName,
		UserID:       req.UserID,
		WorkspaceKey: req.WorkspaceKey,
		Cursor:       req.Cursor,
		Limit:        req.Limit,
	})
}

func (g completionBenchmarkGateway) ReplayEvents(context.Context, gateway.ReplayEventsRequest) (gateway.ReplayEventsResult, error) {
	return gateway.ReplayEventsResult{}, nil
}

func BenchmarkResumeCompletion200Sessions500Events(b *testing.B) {
	ctx := context.Background()
	nextID := 0
	service := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir: b.TempDir(),
		SessionIDGenerator: func() string {
			nextID++
			return fmt.Sprintf("session-%04d", nextID)
		},
	}))
	events := make([]*session.Event, 500)
	for i := range events {
		events[i] = &session.Event{
			Type:       session.EventTypeLifecycle,
			Visibility: session.VisibilityCanonical,
			Lifecycle:  &session.EventLifecycle{Status: "completed", Reason: "performance_fixture"},
		}
	}
	for i := 0; i < 200; i++ {
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
		if _, err := service.AppendEvents(ctx, session.AppendEventsRequest{SessionRef: active.SessionRef, Events: events}); err != nil {
			b.Fatal(err)
		}
	}

	gatewayService := completionBenchmarkGateway{sessions: service}
	driver := &Adapter{stack: &RuntimeStack{
		Gateway: GatewayRuntimeDeps{SessionServiceFn: func() GatewaySessionService { return gatewayService }},
		Session: SessionRuntimeDeps{
			Store: service, AppName: "caelis", UserID: "performance-user",
			Workspace: session.WorkspaceRef{Key: "performance-workspace", CWD: "/tmp/performance-workspace"},
		},
	}}

	b.ReportMetric(200, "sessions")
	b.ReportMetric(100000, "events")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		candidates, err := driver.ListSessions(ctx, 200)
		if err != nil {
			b.Fatal(err)
		}
		if len(candidates) != 200 {
			b.Fatalf("ListSessions() returned %d candidates", len(candidates))
		}
	}
}
