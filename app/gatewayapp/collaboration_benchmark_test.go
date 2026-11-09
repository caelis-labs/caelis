package gatewayapp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/task"
)

// The same three recipient lookups are used before and after the state-only
// read optimization. Fixture creation and durable writes are outside the timer.
func BenchmarkCollaborationThreeRecipients(b *testing.B) {
	for _, resultBytes := range []int{0, 32 << 10} {
		b.Run(fmt.Sprintf("result_%d", resultBytes), func(b *testing.B) {
			store := sessionfile.NewStore(sessionfile.Config{RootDir: b.TempDir()})
			active, err := store.StartSession(b.Context(), session.StartSessionRequest{
				AppName: "benchmark", UserID: "user", Workspace: session.WorkspaceRef{Key: "work", CWD: b.TempDir()},
			})
			if err != nil {
				b.Fatal(err)
			}
			tasks := sessionfile.NewTaskStore(store)
			for i := range 105 {
				id := fmt.Sprintf("task-%03d", i)
				if err := tasks.Upsert(b.Context(), &task.Entry{
					TaskID: id, Kind: task.KindSubagent, Session: active.SessionRef, State: task.StateCompleted,
					Spec:     map[string]any{"prompt": "synthetic benchmark"},
					Result:   map[string]any{"final_message": strings.Repeat("x", resultBytes)},
					Metadata: map[string]any{"supports_steering": true},
				}); err != nil {
					b.Fatal(err)
				}
				if i < 3 {
					if _, err := store.PutParticipant(b.Context(), session.PutParticipantRequest{
						SessionRef: active.SessionRef,
						Binding: session.ParticipantBinding{ID: "participant-" + id, Kind: session.ParticipantKindSubagent,
							DelegationID: id, SessionID: "child-" + id, AgentName: id, Label: "@" + id},
					}); err != nil {
						b.Fatal(err)
					}
				}
			}
			persisted, err := tasks.Get(b.Context(), "task-000")
			if err != nil {
				b.Fatal(err)
			}
			if output, _ := persisted.Result["final_message"].(string); len(output) != resultBytes {
				b.Fatalf("persisted fixture result bytes = %d, want %d", len(output), resultBytes)
			}
			backend := &collaborationBackend{sessions: store, tasks: tasks, router: &hostedChildInputRouter{}}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				for range 3 {
					threads, err := backend.List(b.Context(), active.SessionID)
					if err != nil || len(threads) != 4 {
						b.Fatalf("recipient lookup: %v, %d threads", err, len(threads))
					}
				}
			}
		})
	}
}
