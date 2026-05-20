package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestStoreAppendAndPersistCanonicalEvents(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	at := time.Date(2026, time.April, 19, 11, 22, 33, 0, time.UTC)
	store := NewStore(Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-1" },
		EventIDGenerator:   func() string { return "evt-1" },
		Clock:              func() time.Time { return at },
	})
	ctx := context.Background()

	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	if _, err := store.AppendEvent(ctx, createdSession.SessionRef, &session.Event{
		Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "hello")),
		Text:    "hello",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if _, err := store.AppendEvent(ctx, createdSession.SessionRef, session.MarkNotice(&session.Event{
		Message: ptrMessage(model.NewTextMessage(model.RoleSystem, "warn: retrying")),
	}, "warn", "retrying")); err != nil {
		t.Fatalf("AppendEvent(notice) error = %v", err)
	}

	events, err := store.Events(ctx, session.EventsRequest{SessionRef: createdSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if got, want := len(events), 1; got != want {
		t.Fatalf("len(events) = %d, want %d", got, want)
	}

	data, err := os.ReadFile(rolloutDocumentPath(root, "ws-1", at, "sess-1"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("Unmarshal(document) error = %v", err)
	}
	if events, ok := persisted["events"].([]any); ok && len(events) > 0 {
		t.Fatalf("session document must not store canonical events: %#v", events)
	}
	logData, err := os.ReadFile(rolloutEventLogPath(root, "ws-1", at, "sess-1"))
	if err != nil {
		t.Fatalf("ReadFile(event log) error = %v", err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "\"hello\"") {
		t.Fatal("event log must contain canonical event text")
	}
	if strings.Contains(logText, "retrying") {
		t.Fatal("event log must not contain transient notice text")
	}
}

func TestStoreAppendRegeneratesDuplicateEventIDAcrossProcesses(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	at := time.Date(2026, time.May, 17, 8, 30, 0, 0, time.UTC)
	firstStore := NewStore(Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-1" },
		Clock:              func() time.Time { return at },
	})
	ctx := context.Background()
	createdSession, err := firstStore.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}
	first, err := firstStore.AppendEvent(ctx, createdSession.SessionRef, &session.Event{
		Message: ptrMessage(model.NewTextMessage(model.RoleUser, "first")),
	})
	if err != nil {
		t.Fatalf("AppendEvent(first) error = %v", err)
	}
	if first.ID != "event-1" {
		t.Fatalf("first event id = %q, want event-1", first.ID)
	}

	secondStore := NewStore(Config{
		RootDir: root,
		Clock:   func() time.Time { return at.Add(time.Second) },
	})
	second, err := secondStore.AppendEvent(ctx, createdSession.SessionRef, &session.Event{
		Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "second")),
	})
	if err != nil {
		t.Fatalf("AppendEvent(second) error = %v", err)
	}
	if second.ID == "" || second.ID == first.ID {
		t.Fatalf("second event id = %q, want non-empty id distinct from %q", second.ID, first.ID)
	}
	events, err := secondStore.Events(ctx, session.EventsRequest{SessionRef: createdSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	seen := map[string]struct{}{}
	for _, event := range events {
		if event.ID == "" {
			t.Fatalf("event with empty id: %#v", event)
		}
		if _, ok := seen[event.ID]; ok {
			t.Fatalf("duplicate persisted event id %q in %#v", event.ID, events)
		}
		seen[event.ID] = struct{}{}
	}
}

func TestStoreListUsesSessionMetadataIndex(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	at := time.Date(2026, time.April, 19, 11, 22, 33, 0, time.UTC)
	store := NewStore(Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-1" },
		Clock:              func() time.Time { return at },
	})
	ctx := context.Background()

	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
		Title: "indexed session",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}
	docPath := rolloutDocumentPath(root, "ws-1", at, createdSession.SessionID)
	if err := os.WriteFile(docPath, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("WriteFile(corrupt doc) error = %v", err)
	}

	list, err := store.List(ctx, session.ListSessionsRequest{
		AppName:      "caelis",
		UserID:       "user-1",
		WorkspaceKey: "ws-1",
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got, want := len(list.Sessions), 1; got != want {
		t.Fatalf("len(List().Sessions) = %d, want %d", got, want)
	}
	if got := list.Sessions[0].Title; got != "indexed session" {
		t.Fatalf("List title = %q, want indexed session", got)
	}
}

func TestStoreListRebuildsFromDocumentsWhenSessionIndexIsCorrupt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	at := time.Date(2026, time.April, 19, 11, 22, 33, 0, time.UTC)
	store := NewStore(Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-1" },
		Clock:              func() time.Time { return at },
	})
	ctx := context.Background()

	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
		Title: "valid document",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}
	if createdSession.SessionID != "sess-1" {
		t.Fatalf("SessionID = %q, want sess-1", createdSession.SessionID)
	}
	if err := os.WriteFile(filepath.Join(root, indexFilename), []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("WriteFile(index) error = %v", err)
	}

	list, err := store.List(ctx, session.ListSessionsRequest{
		AppName:      "caelis",
		UserID:       "user-1",
		WorkspaceKey: "ws-1",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got, want := len(list.Sessions), 1; got != want {
		t.Fatalf("len(List().Sessions) = %d, want %d", got, want)
	}
	if got := list.Sessions[0].Title; got != "valid document" {
		t.Fatalf("List title = %q, want valid document", got)
	}
}

func TestStoreWriteRebuildsCorruptSessionIndexBeforeUpsert(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	at := time.Date(2026, time.April, 19, 11, 22, 33, 0, time.UTC)
	store := NewStore(Config{
		RootDir: root,
		Clock: func() time.Time {
			return at
		},
	})
	ctx := context.Background()
	for _, id := range []string{"sess-1", "sess-2"} {
		if _, err := store.GetOrCreate(ctx, session.StartSessionRequest{
			AppName:            "caelis",
			UserID:             "user-1",
			PreferredSessionID: id,
			Workspace: session.WorkspaceRef{
				Key: "ws-1",
				CWD: "/tmp/ws",
			},
			Title: id,
		}); err != nil {
			t.Fatalf("GetOrCreate(%q) error = %v", id, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, indexFilename), []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("WriteFile(index) error = %v", err)
	}

	if _, err := store.BindController(ctx, session.SessionRef{
		AppName:      "caelis",
		UserID:       "user-1",
		SessionID:    "sess-2",
		WorkspaceKey: "ws-1",
	}, session.ControllerBinding{ControllerID: "controller-1"}); err != nil {
		t.Fatalf("BindController() error = %v", err)
	}
	list, err := store.List(ctx, session.ListSessionsRequest{
		AppName:      "caelis",
		UserID:       "user-1",
		WorkspaceKey: "ws-1",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got, want := len(list.Sessions), 2; got != want {
		t.Fatalf("len(List().Sessions) = %d, want %d", got, want)
	}
}

func TestStoreEventsIgnoresPartialFinalEventLogRecord(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	at := time.Date(2026, time.April, 19, 11, 22, 33, 0, time.UTC)
	nextEventID := 0
	store := NewStore(Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-1" },
		EventIDGenerator: func() string {
			nextEventID++
			return "evt-" + strconv.Itoa(nextEventID)
		},
		Clock: func() time.Time { return at },
	})
	ctx := context.Background()

	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}
	for _, text := range []string{"first", "second"} {
		if _, err := store.AppendEvent(ctx, createdSession.SessionRef, &session.Event{
			Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, text)),
			Text:    text,
		}); err != nil {
			t.Fatalf("AppendEvent(%q) error = %v", text, err)
		}
	}
	logPath := rolloutEventLogPath(root, "ws-1", at, "sess-1")
	file, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("OpenFile(event log) error = %v", err)
	}
	if _, err := file.WriteString("{\"id\":\"evt-partial\""); err != nil {
		file.Close()
		t.Fatalf("WriteString(partial) error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(event log) error = %v", err)
	}

	events, err := store.Events(ctx, session.EventsRequest{SessionRef: createdSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if got, want := len(events), 2; got != want {
		t.Fatalf("len(Events()) = %d, want %d", got, want)
	}
}

func TestStoreAppendTruncatesPartialFinalEventLogRecordBeforeWriting(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	at := time.Date(2026, time.April, 19, 11, 22, 33, 0, time.UTC)
	nextEventID := 0
	store := NewStore(Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-1" },
		EventIDGenerator: func() string {
			nextEventID++
			return "evt-" + strconv.Itoa(nextEventID)
		},
		Clock: func() time.Time { return at },
	})
	ctx := context.Background()

	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}
	if _, err := store.AppendEvent(ctx, createdSession.SessionRef, &session.Event{
		Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "first")),
		Text:    "first",
	}); err != nil {
		t.Fatalf("AppendEvent(first) error = %v", err)
	}
	logPath := rolloutEventLogPath(root, "ws-1", at, "sess-1")
	file, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("OpenFile(event log) error = %v", err)
	}
	if _, err := file.WriteString("{\"id\":\"evt-partial\""); err != nil {
		file.Close()
		t.Fatalf("WriteString(partial) error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(event log) error = %v", err)
	}

	if _, err := store.AppendEvent(ctx, createdSession.SessionRef, &session.Event{
		Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "second")),
		Text:    "second",
	}); err != nil {
		t.Fatalf("AppendEvent(second) error = %v", err)
	}
	events, err := store.Events(ctx, session.EventsRequest{SessionRef: createdSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if got, want := len(events), 2; got != want {
		t.Fatalf("len(Events()) = %d, want %d", got, want)
	}
	if logData, err := os.ReadFile(logPath); err != nil {
		t.Fatalf("ReadFile(event log) error = %v", err)
	} else if strings.Contains(string(logData), "evt-partial") {
		t.Fatalf("event log retained partial record: %q", string(logData))
	}
}

func TestStoreConcurrentWritersPreserveSessionIndexAcrossStoreInstances(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	const writers = 32
	baseTime := time.Date(2026, time.April, 19, 11, 22, 33, 0, time.UTC)
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			store := NewStore(Config{
				RootDir: root,
				Clock: func() time.Time {
					return baseTime.Add(time.Duration(i) * time.Second)
				},
			})
			_, err := store.GetOrCreate(ctx, session.StartSessionRequest{
				AppName:            "caelis",
				UserID:             "user-1",
				PreferredSessionID: fmt.Sprintf("sess-%02d", i),
				Workspace: session.WorkspaceRef{
					Key: "ws-1",
					CWD: "/tmp/ws",
				},
				Title: fmt.Sprintf("session %02d", i),
			})
			if err != nil {
				errs <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent GetOrCreate() error = %v", err)
		}
	}

	list, err := NewStore(Config{RootDir: root}).List(ctx, session.ListSessionsRequest{
		AppName:      "caelis",
		UserID:       "user-1",
		WorkspaceKey: "ws-1",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got := len(list.Sessions); got != writers {
		t.Fatalf("len(List().Sessions) = %d, want %d", got, writers)
	}
}

func TestStoreLargeEventListRoundTrip(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewStore(Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-large" },
	})
	ctx := context.Background()
	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}
	const eventCount = 40
	for i := 0; i < eventCount; i++ {
		msg := model.NewTextMessage(model.RoleUser, "large event "+strings.Repeat("x", 128))
		if _, err := store.AppendEvent(ctx, createdSession.SessionRef, &session.Event{
			Type:       session.EventTypeUser,
			Visibility: session.VisibilityCanonical,
			Message:    &msg,
			Text:       msg.TextContent(),
		}); err != nil {
			t.Fatalf("AppendEvent(%d) error = %v", i, err)
		}
	}

	reloaded := NewStore(Config{RootDir: root})
	events, err := reloaded.Events(ctx, session.EventsRequest{SessionRef: createdSession.SessionRef})
	if err != nil {
		t.Fatalf("Events(reloaded) error = %v", err)
	}
	if len(events) != eventCount {
		t.Fatalf("len(events) = %d, want %d", len(events), eventCount)
	}
	if got := session.EventText(events[len(events)-1]); !strings.Contains(got, "large event") {
		t.Fatalf("last event text = %q, want large event payload", got)
	}
}

func TestStoreUpdateStateAndParticipantAnchor(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	at := time.Date(2026, time.April, 19, 11, 22, 33, 0, time.UTC)
	service := NewService(NewStore(Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-1" },
		Clock:              func() time.Time { return at },
	}))
	ctx := context.Background()

	createdSession, err := service.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	if err := service.UpdateState(ctx, createdSession.SessionRef, func(state map[string]any) (map[string]any, error) {
		state["mode"] = "default"
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}

	createdSession, err = service.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: createdSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:            "part-1",
			Kind:          session.ParticipantKindSubagent,
			Role:          session.ParticipantRoleDelegated,
			Label:         "spark-explorer",
			SessionID:     "child-1",
			Source:        "spawn",
			DelegationID:  "dlg-1",
			ControllerRef: "ep-1",
		},
	})
	if err != nil {
		t.Fatalf("PutParticipant() error = %v", err)
	}

	if got, want := len(createdSession.Participants), 1; got != want {
		t.Fatalf("len(participants) = %d, want %d", got, want)
	}
	if got := createdSession.Participants[0].SessionID; got != "child-1" {
		t.Fatalf("participant session_id = %q, want %q", got, "child-1")
	}

	state, err := service.SnapshotState(ctx, createdSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if got := state["mode"]; got != "default" {
		t.Fatalf("state[mode] = %v, want %q", got, "default")
	}

	data, err := os.ReadFile(rolloutDocumentPath(root, "", at, "sess-1"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "\"session_id\": \"child-1\"") {
		t.Fatal("persisted participant anchor must include child session id")
	}
}

func TestServiceLoadSessionReadsOneDocumentSnapshot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	at := time.Date(2026, time.April, 19, 11, 22, 33, 0, time.UTC)
	service := NewService(NewStore(Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-1" },
		EventIDGenerator:   func() string { return "evt-1" },
		Clock:              func() time.Time { return at },
	}))
	ctx := context.Background()

	createdSession, err := service.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: createdSession.SessionRef,
		Event: &session.Event{
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "hello")),
			Text:    "hello",
		},
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := service.UpdateState(ctx, createdSession.SessionRef, func(state map[string]any) (map[string]any, error) {
		state["mode"] = "manual"
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}

	loaded, err := service.LoadSession(ctx, session.LoadSessionRequest{SessionRef: createdSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := loaded.Session.SessionID, createdSession.SessionID; got != want {
		t.Fatalf("loaded session id = %q, want %q", got, want)
	}
	if got, want := len(loaded.Events), 1; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}
	if got := loaded.State["mode"]; got != "manual" {
		t.Fatalf("loaded state mode = %v, want manual", got)
	}
	loaded.State["mode"] = "mutated"
	state, err := service.SnapshotState(ctx, createdSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if got := state["mode"]; got != "manual" {
		t.Fatalf("stored state mode = %v, want clone to remain manual", got)
	}
}

func TestStoreWriteDocumentUsesSecurePermissions(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not reliable on windows")
	}

	root := t.TempDir()
	at := time.Date(2026, time.April, 19, 11, 22, 33, 0, time.UTC)
	store := NewStore(Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-1" },
		Clock:              func() time.Time { return at },
	})
	ctx := context.Background()
	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}
	docPath := rolloutDocumentPath(root, "ws-1", at, createdSession.SessionID)
	info, err := os.Stat(docPath)
	if err != nil {
		t.Fatalf("Stat(docPath) error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("document mode = %#o, want %#o", got, os.FileMode(0o600))
	}
	dirInfo, err := os.Stat(filepath.Dir(docPath))
	if err != nil {
		t.Fatalf("Stat(docDir) error = %v", err)
	}
	if got := dirInfo.Mode().Perm() & 0o077; got != 0 {
		t.Fatalf("document dir mode = %#o, want no group/world bits", dirInfo.Mode().Perm())
	}
}

func TestStoreGeneratesFreshCompactSessionIDsAcrossRestart(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctx := context.Background()
	req := session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
	}

	first, err := NewStore(Config{RootDir: root}).GetOrCreate(ctx, req)
	if err != nil {
		t.Fatalf("first GetOrCreate() error = %v", err)
	}
	second, err := NewStore(Config{RootDir: root}).GetOrCreate(ctx, req)
	if err != nil {
		t.Fatalf("second GetOrCreate() error = %v", err)
	}

	if !strings.HasPrefix(first.SessionID, "s-") {
		t.Fatalf("first session id = %q, want s- prefix", first.SessionID)
	}
	if !strings.HasPrefix(second.SessionID, "s-") {
		t.Fatalf("second session id = %q, want s- prefix", second.SessionID)
	}
	if len(first.SessionID) > 16 || len(second.SessionID) > 16 {
		t.Fatalf("expected compact session ids, got %q (%d) and %q (%d)", first.SessionID, len(first.SessionID), second.SessionID, len(second.SessionID))
	}
	if first.SessionID == second.SessionID {
		t.Fatalf("session ids collided across restart: %q", first.SessionID)
	}

	list, err := NewStore(Config{RootDir: root}).List(ctx, session.ListSessionsRequest{
		AppName:      "caelis",
		UserID:       "user-1",
		WorkspaceKey: "ws-1",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got, want := len(list.Sessions), 2; got != want {
		t.Fatalf("len(list.Sessions) = %d, want %d", got, want)
	}
}

func TestStoreListSessionsRecursesRolloutDirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctx := context.Background()
	now := time.Date(2026, time.April, 19, 10, 0, 0, 0, time.UTC)
	sessionIDs := []string{"sess-1", "sess-2"}
	store := NewStore(Config{
		RootDir: root,
		SessionIDGenerator: func() string {
			id := sessionIDs[0]
			sessionIDs = sessionIDs[1:]
			return id
		},
		Clock: func() time.Time { return now },
	})

	first, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate(first) error = %v", err)
	}
	if _, err := store.AppendEvent(ctx, first.SessionRef, &session.Event{
		Message: ptrMessage(model.NewTextMessage(model.RoleUser, "first")),
		Text:    "first",
	}); err != nil {
		t.Fatalf("AppendEvent(first) error = %v", err)
	}

	now = now.Add(2 * time.Hour)
	second, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate(second) error = %v", err)
	}
	if _, err := store.AppendEvent(ctx, second.SessionRef, &session.Event{
		Message: ptrMessage(model.NewTextMessage(model.RoleUser, "second")),
		Text:    "second",
	}); err != nil {
		t.Fatalf("AppendEvent(second) error = %v", err)
	}

	list, err := NewStore(Config{RootDir: root}).List(ctx, session.ListSessionsRequest{
		AppName:      "caelis",
		UserID:       "user-1",
		WorkspaceKey: "ws-1",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got, want := len(list.Sessions), 2; got != want {
		t.Fatalf("len(list.Sessions) = %d, want %d", got, want)
	}
	if got := list.Sessions[0].SessionID; got != second.SessionID {
		t.Fatalf("latest session = %q, want %q", got, second.SessionID)
	}
	if got := list.Sessions[1].SessionID; got != first.SessionID {
		t.Fatalf("older session = %q, want %q", got, first.SessionID)
	}
}

func TestStoreGetOrCreateIsolatesSameSessionIDAcrossWorkspaces(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	at := time.Date(2026, time.April, 19, 11, 22, 33, 0, time.UTC)
	store := NewStore(Config{
		RootDir: root,
		Clock:   func() time.Time { return at },
	})

	ctx := context.Background()
	first, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "user-1",
		PreferredSessionID: "default",
		Workspace: session.WorkspaceRef{
			Key: "ws-a",
			CWD: "/tmp/ws-a",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate(ws-a) error = %v", err)
	}
	second, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "user-1",
		PreferredSessionID: "default",
		Workspace: session.WorkspaceRef{
			Key: "ws-b",
			CWD: "/tmp/ws-b",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate(ws-b) error = %v", err)
	}

	if first.WorkspaceKey != "ws-a" {
		t.Fatalf("first.WorkspaceKey = %q, want ws-a", first.WorkspaceKey)
	}
	if second.WorkspaceKey != "ws-b" {
		t.Fatalf("second.WorkspaceKey = %q, want ws-b", second.WorkspaceKey)
	}

	firstPath := rolloutDocumentPath(root, "ws-a", at, "default")
	secondPath := rolloutDocumentPath(root, "ws-b", at, "default")
	firstDoc := readPersistedDocument(t, firstPath)
	secondDoc := readPersistedDocument(t, secondPath)
	if firstDoc.Session.WorkspaceKey != "ws-a" {
		t.Fatalf("first document workspace = %q, want ws-a", firstDoc.Session.WorkspaceKey)
	}
	if secondDoc.Session.WorkspaceKey != "ws-b" {
		t.Fatalf("second document workspace = %q, want ws-b", secondDoc.Session.WorkspaceKey)
	}
}

func TestStoreGetLoadsSessionWithoutWorkspaceKey(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	service := NewService(NewStore(Config{RootDir: root}))
	ctx := context.Background()

	createdSession, err := service.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "acp",
		Workspace: session.WorkspaceRef{
			Key: "/tmp/ws",
			CWD: "/tmp/ws",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	loaded, err := service.Session(ctx, session.SessionRef{
		AppName:   "caelis",
		UserID:    "acp",
		SessionID: createdSession.SessionID,
	})
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if loaded.SessionID != createdSession.SessionID {
		t.Fatalf("loaded session id = %q, want %q", loaded.SessionID, createdSession.SessionID)
	}
	if loaded.WorkspaceKey != createdSession.WorkspaceKey {
		t.Fatalf("loaded workspace key = %q, want %q", loaded.WorkspaceKey, createdSession.WorkspaceKey)
	}
}

func TestStoreRequiresWorkspaceKeyWhenSessionIDMatchesMultipleWorkspaces(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewStore(Config{RootDir: root})
	ctx := context.Background()
	for _, workspaceKey := range []string{"ws-a", "ws-b"} {
		if _, err := store.GetOrCreate(ctx, session.StartSessionRequest{
			AppName:            "caelis",
			UserID:             "user-1",
			PreferredSessionID: "shared",
			Workspace: session.WorkspaceRef{
				Key: workspaceKey,
				CWD: "/tmp/" + workspaceKey,
			},
		}); err != nil {
			t.Fatalf("GetOrCreate(%s) error = %v", workspaceKey, err)
		}
	}

	reloaded := NewService(NewStore(Config{RootDir: root}))
	_, err := reloaded.Session(ctx, session.SessionRef{
		AppName:   "caelis",
		UserID:    "user-1",
		SessionID: "shared",
	})
	if !errors.Is(err, session.ErrAmbiguousSession) {
		t.Fatalf("Session(without workspace) error = %v, want ErrAmbiguousSession", err)
	}

	loaded, err := reloaded.Session(ctx, session.SessionRef{
		AppName:      "caelis",
		UserID:       "user-1",
		SessionID:    "shared",
		WorkspaceKey: "ws-b",
	})
	if err != nil {
		t.Fatalf("Session(ws-b) error = %v", err)
	}
	if got := loaded.WorkspaceKey; got != "ws-b" {
		t.Fatalf("loaded workspace = %q, want ws-b", got)
	}
}

func TestStorePreservesLegacyFlatSessionDocuments(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	legacyPath := filepath.Join(root, "session-1.json")
	writePersistedDocument(t, legacyPath, persistedDocument{
		Kind:    documentKind,
		Version: documentVersion,
		Session: session.Session{
			SessionRef: session.SessionRef{
				AppName:      "caelis",
				UserID:       "user-1",
				SessionID:    "session-1",
				WorkspaceKey: "ws-1",
			},
		},
		State: map[string]any{},
	})

	store := NewStore(Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-2" },
	})
	if _, err := store.GetOrCreate(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
	}); err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy flat session document must be preserved, stat err = %v", err)
	}
}

func ptrMessage(message model.Message) *model.Message {
	return &message
}

func rolloutDocumentPath(root, workspaceKey string, at time.Time, sessionID string) string {
	if workspaceKey == "" {
		workspaceKey = "workspace"
	}
	return filepath.Join(
		root,
		strings.TrimSpace(workspaceKey),
		at.UTC().Format("2006"),
		at.UTC().Format("01"),
		at.UTC().Format("02"),
		"rollout-"+at.UTC().Format("2006-01-02T15-04-05")+"-"+sessionID+".json",
	)
}

func rolloutEventLogPath(root, workspaceKey string, at time.Time, sessionID string) string {
	return strings.TrimSuffix(rolloutDocumentPath(root, workspaceKey, at, sessionID), ".json") + ".events.jsonl"
}

func writePersistedDocument(t *testing.T, path string, doc persistedDocument) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func readPersistedDocument(t *testing.T, path string) persistedDocument {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	var doc persistedDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal(%q) error = %v", path, err)
	}
	return doc
}
