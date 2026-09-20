package file

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func BenchmarkResumeMetadataIndex(b *testing.B) {
	for _, count := range []int{4000, 20000} {
		b.Run(fmt.Sprintf("events=%d", count), func(b *testing.B) {
			store := NewStore(Config{RootDir: b.TempDir()})
			active, err := store.StartSession(b.Context(), session.StartSessionRequest{AppName: "bench", UserID: "user"})
			if err != nil {
				b.Fatal(err)
			}
			events := make([]*session.Event, count)
			for n := range events {
				events[n] = &session.Event{Type: session.EventTypeLifecycle, Visibility: session.VisibilityCanonical, Scope: &session.EventScope{TurnID: fmt.Sprint(n / 4)}, Lifecycle: &session.EventLifecycle{Status: "completed", Reason: strings.Repeat("history ", 256)}}
			}
			if _, err := store.AppendEvents(b.Context(), session.AppendEventsRequest{SessionRef: active.SessionRef, Events: events}); err != nil {
				b.Fatal(err)
			}
			path, err := store.resolveWritePath(active)
			if err != nil {
				b.Fatal(err)
			}
			for _, persisted := range []bool{false, true} {
				b.Run(fmt.Sprintf("persisted=%v", persisted), func(b *testing.B) {
					req := session.EventPageRequest{SessionRef: active.SessionRef, Visibility: session.EventPageClientReplay, Limit: count}
					if _, err := store.EventMetadataPage(b.Context(), req); err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					b.ResetTimer()
					for n := 0; n < b.N; n++ {
						b.StopTimer()
						if !persisted {
							if err := os.Remove(eventLogPath(path) + ".metadata"); err != nil {
								b.Fatal(err)
							}
						}
						cold := NewStore(Config{RootDir: store.rootDir})
						b.StartTimer()
						page, err := cold.EventMetadataPage(b.Context(), req)
						if err != nil || page.NextSeq != uint64(count) {
							b.Fatalf("page=%+v error=%v", page, err)
						}
					}
				})
			}
		})
	}
}
