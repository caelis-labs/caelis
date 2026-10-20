package file

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func newSharedMediaFixture(t testing.TB, count int) (*Store, session.Session, []*session.Event) {
	t.Helper()
	store, active := newEventPageIndexFixture(t, 0)
	image := model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: strings.Repeat("YWJj", 256<<10)}, "image/png", "screen")
	result, err := json.Marshal(struct {
		Name    string       `json:"name"`
		Content []model.Part `json:"content"`
	}{"screen", []model.Part{image}})
	if err != nil {
		t.Fatal(err)
	}
	events := make([]*session.Event, count)
	for i := range count {
		id := fmt.Sprintf("call-%d", i)
		record := session.NormalizeToolExecution(session.ToolExecution{
			Key:      session.ExecutionKey{SessionID: active.SessionID, RunID: "run", TurnID: "turn", StepID: id, ToolCallID: id},
			Revision: 1, Status: session.ToolExecutionSucceeded, ToolName: "screen", Result: result,
		})
		message := model.Message{Role: model.RoleTool, Parts: []model.Part{{Kind: model.PartKindToolResult, ToolResult: &model.ToolResultPart{ToolUseID: id, Name: "screen", Content: []model.Part{image}}}}}
		events[i] = &session.Event{ID: id, Type: session.EventTypeToolResult, Message: &message,
			Journal: &session.ExecutionJournalEntry{Schema: session.ExecutionJournalSchemaVersion, Kind: session.JournalKindToolExecution, ToolExecution: &record}}
	}
	return store, active, events
}

func TestSharedMediaSurvivesWALRecoveryPagingAndIdempotentRetry(t *testing.T) {
	store, active, events := newSharedMediaFixture(t, 2)
	root := store.rootDir
	path, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatal(err)
	}
	store.transactionFault = func(phase string) error {
		if phase == "after_commit" {
			return errors.New("crash after durable WAL")
		}
		return nil
	}
	_, err = store.AppendEvents(t.Context(), session.AppendEventsRequest{SessionRef: active.SessionRef, Events: events})
	if !session.IsCommitted(err) {
		t.Fatalf("append = %v, want committed WAL fault", err)
	}
	wal, err := os.ReadFile(transactionPath(path))
	if err != nil {
		t.Fatal(err)
	}
	image := events[0].Message.Parts[0].ToolResult.Content[0].Media.Source.Data
	if got := bytes.Count(wal, []byte(image)); got != 2 {
		t.Fatalf("WAL contains %d image copies, want one per event", got)
	}
	for _, name := range []string{"recovery", "cold reopen"} {
		t.Run(name, func(t *testing.T) {
			reopened := NewStore(Config{RootDir: root})
			// Exercise cold indexed history instead of relying on append caches.
			reopened.eventLogCacheMaxBytes = 1024
			loaded, err := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil || len(loaded.Events) != len(events) {
				t.Fatalf("load events = %d, %v", len(loaded.Events), err)
			}
			for i, event := range loaded.Events {
				if !reflect.DeepEqual(event.Message, events[i].Message) || !bytes.Equal(event.Journal.ToolExecution.Result, events[i].Journal.ToolExecution.Result) {
					t.Fatal("message or recovery receipt changed")
				}
			}
			page, err := reopened.EventsPage(t.Context(), session.EventPageRequest{SessionRef: active.SessionRef, Limit: 1})
			if err != nil || len(page.Events) != 1 || !reflect.DeepEqual(page.Events[0].Message, events[0].Message) {
				t.Fatalf("page = %v, %v", len(page.Events), err)
			}
			checkpoint, err := reopened.EventCheckpoint(t.Context(), active.SessionRef)
			if err != nil || checkpoint.ThroughSeq != 2 {
				t.Fatalf("checkpoint = %+v, %v", checkpoint, err)
			}
			if _, err := reopened.AppendEvents(t.Context(), session.AppendEventsRequest{SessionRef: active.SessionRef, Events: events}); err != nil {
				t.Fatalf("original full-payload retry: %v", err)
			}
		})
	}
	if _, err := os.Stat(transactionPath(path)); !os.IsNotExist(err) {
		t.Fatalf("WAL not cleaned after recovery: %v", err)
	}
	log, err := os.ReadFile(eventLogPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(log, []byte(image)) != 2 {
		t.Fatal("JSONL must store one image per event")
	}
	// A retained event never depends on another event's media or a temporary
	// asset: each record still decodes in isolation after dropping its prefix.
	lines := bytes.Split(bytes.TrimSpace(log), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("log has %d lines, want 2", len(lines))
	}
	isolated, err := decodeIndexedEvent(lines[1], "isolated", 1)
	if err != nil || !reflect.DeepEqual(isolated.Message, events[1].Message) {
		t.Fatalf("retained event depends on discarded prefix: %v", err)
	}
	// A changed payload must still conflict with the original durable call.
	changed := session.CloneEvent(events[0])
	changed.Message.Parts[0].ToolResult.Content[0].Media.Source.Data = strings.Repeat("Z", len(image))
	if _, err := NewStore(Config{RootDir: root}).AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: changed}); err == nil {
		t.Fatal("changed media accepted as idempotent retry")
	}
}

func TestSharedMediaLegacyLogAndMixedAppend(t *testing.T) {
	store, active, events := newSharedMediaFixture(t, 2)
	persisted, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: events[0]})
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatal(err)
	}
	// Model a schema-1 writer's ordinary full-payload record, not a packed
	// record with only its schema field changed.
	persisted.Schema = 1
	raw, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eventLogPath(path), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened := NewStore(Config{RootDir: store.rootDir})
	if _, err := reopened.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: events[0]}); err != nil {
		t.Fatalf("legacy full-payload retry: %v", err)
	}
	if _, err := reopened.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: events[1]}); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewStore(Config{RootDir: store.rootDir}).LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil || len(loaded.Events) != 2 {
		t.Fatalf("mixed history = %d, %v", len(loaded.Events), err)
	}
	for i, event := range loaded.Events {
		if !reflect.DeepEqual(event.Message, events[i].Message) || !bytes.Equal(event.Journal.ToolExecution.Result, events[i].Journal.ToolExecution.Result) {
			t.Fatal("legacy or current media changed")
		}
	}
}

func BenchmarkColdSharedMediaLoad(b *testing.B) {
	for _, shared := range []bool{false, true} {
		name := "expanded"
		if shared {
			name = "shared"
		}
		b.Run(name, func(b *testing.B) {
			store, active, events := newSharedMediaFixture(b, 14)
			persisted, err := store.AppendEvents(b.Context(), session.AppendEventsRequest{SessionRef: active.SessionRef, Events: events})
			if err != nil {
				b.Fatal(err)
			}
			path, err := store.resolveWritePath(active)
			if err != nil {
				b.Fatal(err)
			}
			if !shared {
				var log bytes.Buffer
				for _, event := range persisted {
					raw, err := json.Marshal(event)
					if err != nil {
						b.Fatal(err)
					}
					log.Write(raw)
					log.WriteByte('\n')
				}
				if err := os.WriteFile(eventLogPath(path), log.Bytes(), 0o600); err != nil {
					b.Fatal(err)
				}
			}
			info, err := os.Stat(eventLogPath(path))
			if err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			b.ReportMetric(float64(info.Size()), "bytes/log")
			b.ReportAllocs()
			for range b.N {
				cold := NewStore(Config{RootDir: store.rootDir})
				if _, err := cold.LoadSession(b.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestSharedMediaCorruptionFailsClosed(t *testing.T) {
	store, active, events := newSharedMediaFixture(t, 1)
	if _, err := store.AppendEvents(t.Context(), session.AppendEventsRequest{SessionRef: active.SessionRef, Events: events}); err != nil {
		t.Fatal(err)
	}
	path, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(eventLogPath(path))
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(log, &record); err != nil {
		t.Fatal(err)
	}
	delete(record, "media")
	broken, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eventLogPath(path), append(broken, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened := NewStore(Config{RootDir: store.rootDir})
	if _, err := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true}); err == nil {
		t.Fatal("load accepted dangling media")
	}
	if _, err := reopened.EventsPage(t.Context(), session.EventPageRequest{SessionRef: active.SessionRef}); err == nil {
		t.Fatal("page accepted dangling media")
	}
	if _, err := reopened.EventCheckpoint(t.Context(), active.SessionRef); err == nil {
		t.Fatal("checkpoint accepted dangling media")
	}
}
