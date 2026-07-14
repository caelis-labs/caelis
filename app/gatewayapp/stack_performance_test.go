package gatewayapp

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func BenchmarkNewLocalStackFirstFrameBoundary(b *testing.B) {
	for _, fixture := range []struct {
		name          string
		sessionCount  int
		eventsPerSess int
	}{
		{name: "empty"},
		{name: "50_sessions_20_events", sessionCount: 50, eventsPerSess: 20},
		{name: "200_sessions_500_events", sessionCount: 200, eventsPerSess: 500},
		{name: "1_session_5000_events", sessionCount: 1, eventsPerSess: 5000},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			storeDir := b.TempDir()
			benchmarkStackSessions(b, filepath.Join(storeDir, "sessions"), fixture.sessionCount, fixture.eventsPerSess)
			cfg := Config{
				AppName: "caelis", UserID: "performance-user", StoreDir: storeDir,
				WorkspaceKey: "performance-workspace", WorkspaceCWD: "/tmp/performance-workspace",
				DisableBuiltInAgentProfiles: true,
			}
			b.ReportMetric(float64(fixture.sessionCount), "sessions")
			b.ReportMetric(float64(fixture.sessionCount*fixture.eventsPerSess), "events")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := NewLocalStack(cfg); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkNewSessionControlPath(b *testing.B) {
	stack, err := NewLocalStack(Config{
		AppName: "caelis", UserID: "performance-user", StoreDir: b.TempDir(),
		WorkspaceKey: "performance-workspace", WorkspaceCWD: "/tmp/performance-workspace",
		DisableBuiltInAgentProfiles: true,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := stack.StartSession(context.Background(), fmt.Sprintf("new-session-%06d", i), "benchmark-tui"); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkStackSessions(b *testing.B, root string, sessionCount int, eventsPerSession int) {
	b.Helper()
	nextID := 0
	service := sessionfile.NewStore(sessionfile.Config{
		RootDir: root,
		SessionIDGenerator: func() string {
			nextID++
			return fmt.Sprintf("session-%04d", nextID)
		},
	})
	events := make([]*session.Event, eventsPerSession)
	for i := range events {
		events[i] = &session.Event{
			Type: session.EventTypeLifecycle, Visibility: session.VisibilityCanonical,
			Lifecycle: &session.EventLifecycle{Status: "completed", Reason: "performance_fixture"},
		}
	}
	for i := 0; i < sessionCount; i++ {
		active, err := service.StartSession(context.Background(), session.StartSessionRequest{
			AppName: "caelis", UserID: "performance-user",
			Workspace: session.WorkspaceRef{Key: "performance-workspace", CWD: "/tmp/performance-workspace"},
			Title:     fmt.Sprintf("fixture %04d", i),
		})
		if err != nil {
			b.Fatal(err)
		}
		if len(events) == 0 {
			continue
		}
		if _, err := service.AppendEvents(context.Background(), session.AppendEventsRequest{
			SessionRef: active.SessionRef, Events: events,
		}); err != nil {
			b.Fatal(err)
		}
	}
}
