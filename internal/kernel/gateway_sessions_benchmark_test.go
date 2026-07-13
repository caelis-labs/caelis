package kernel

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func BenchmarkResumeKnownSessionID5000Events(b *testing.B) {
	ctx := context.Background()
	service := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir: b.TempDir(), SessionIDGenerator: func() string { return "large-session" },
	}))
	active, err := service.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "performance-user", Workspace: session.WorkspaceRef{Key: "performance-workspace"},
	})
	if err != nil {
		b.Fatal(err)
	}
	events := make([]*session.Event, 5000)
	for i := range events {
		events[i] = &session.Event{
			Type: session.EventTypeLifecycle, Visibility: session.VisibilityCanonical,
			Lifecycle: &session.EventLifecycle{Status: "completed", Reason: "performance_fixture"},
		}
	}
	if _, err := service.AppendEvents(ctx, session.AppendEventsRequest{SessionRef: active.SessionRef, Events: events}); err != nil {
		b.Fatal(err)
	}
	gw, err := New(Config{Sessions: service, Runtime: mockRuntime{}, Resolver: staticResolver{}})
	if err != nil {
		b.Fatal(err)
	}
	request := ResumeSessionRequest{
		AppName: "caelis", UserID: "performance-user", Workspace: session.WorkspaceRef{Key: "performance-workspace"},
		SessionID: active.SessionID, Limit: 1, BindingKey: "benchmark-tui",
	}
	for _, benchmark := range []struct {
		name         string
		metadataOnly bool
	}{
		{name: "history_load"},
		{name: "metadata_only", metadataOnly: true},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			request.MetadataOnly = benchmark.metadataOnly
			b.ReportMetric(5000, "events")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := gw.ResumeSession(ctx, request); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
