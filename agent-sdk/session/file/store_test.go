package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
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

func TestEventLogMigratesRawNestedJournalBeforeTypedDecode(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewStore(Config{RootDir: root, SessionIDGenerator: func() string { return "nested-migration" }})
	active, err := store.GetOrCreate(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user", Workspace: session.WorkspaceRef{Key: "ws", CWD: root},
	})
	if err != nil {
		t.Fatal(err)
	}
	record := session.ExecutionRecord{
		Kind: session.JournalKindRun, SessionID: active.SessionID, RunID: "run-1",
		Revision: 1, Status: session.ExecutionPrepared,
	}
	record.Identity = session.ExecutionRecordIdentity(record)
	raw, err := json.Marshal(&session.Event{
		ID: "nested-event", SessionID: active.SessionID, Schema: session.EventSchemaVersion,
		Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal,
		Journal: &session.ExecutionJournalEntry{Kind: session.JournalKindRun, Execution: &record},
	})
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	journal := object["journal"].(map[string]any)
	journal["future_journal"] = map[string]any{"sentinel": "journal"}
	journal["execution"].(map[string]any)["future_execution"] = map[string]any{"sentinel": "execution"}
	raw, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	documentPath, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatal(err)
	}
	userMessage := model.NewTextMessage(model.RoleUser, "model truth survives journal migration")
	userRaw, err := json.Marshal(&session.Event{
		ID: "user-event", SessionID: active.SessionID, Schema: session.EventSchemaVersion,
		Type: session.EventTypeUser, Message: &userMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	logData := append(append(append([]byte(nil), userRaw...), '\n'), raw...)
	logData = append(logData, '\n')
	if err := os.WriteFile(eventLogPath(documentPath), logData, 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 2 || events[1].Journal == nil || events[1].Journal.Schema != session.ExecutionJournalSchemaVersion || events[1].Journal.Execution == nil || events[1].Journal.Execution.Schema != session.RunSchemaVersion {
		t.Fatalf("events = %#v, want migrated nested journal", events)
	}
	modelEvents, err := store.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(modelEvents) != 1 || modelEvents[0].Message == nil || !reflect.DeepEqual(*modelEvents[0].Message, userMessage) {
		t.Fatalf("model events = %#v, want exact user message round trip", modelEvents)
	}
}

func TestCommittedTransactionMigratesRawEventsBeforeTypedDecode(t *testing.T) {
	t.Parallel()

	record := session.ExecutionRecord{
		Kind: session.JournalKindRun, SessionID: "session", RunID: "run",
		Revision: 1, Status: session.ExecutionPrepared,
	}
	record.Identity = session.ExecutionRecordIdentity(record)
	event := map[string]any{
		"schema":     session.EventSchemaVersion,
		"id":         "event",
		"session_id": "session",
		"type":       session.EventTypeLifecycle,
		"visibility": session.VisibilityJournal,
		"journal": map[string]any{
			"schema": 0, "kind": session.JournalKindRun,
			"future_journal": map[string]any{"sentinel": "journal"},
			"execution": map[string]any{
				"schema": 0, "kind": record.Kind, "session_id": record.SessionID,
				"run_id": record.RunID, "identity": record.Identity, "revision": record.Revision,
				"status": record.Status, "future_execution": map[string]any{"sentinel": "execution"},
			},
		},
	}
	raw, err := json.Marshal(map[string]any{
		"kind": transactionKind, "version": transactionVersion,
		"document": map[string]any{"kind": documentKind, "version": documentVersion, "session": map[string]any{"session_id": "session"}},
		"events":   []any{event},
	})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := decodePersistedTransaction(raw)
	if err != nil {
		t.Fatalf("decodePersistedTransaction() error = %v", err)
	}
	if len(transaction.Events) != 1 || transaction.Events[0].Journal == nil || transaction.Events[0].Journal.Schema != session.ExecutionJournalSchemaVersion || transaction.Events[0].Journal.Execution == nil || transaction.Events[0].Journal.Execution.Schema != session.RunSchemaVersion {
		t.Fatalf("transaction events = %#v, want migrated nested journal", transaction.Events)
	}
}

func TestStoreAppendRejectsProtocolOnlyCoreToolResult(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-1" },
	})
	ctx := context.Background()
	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	_, err = store.AppendEvent(ctx, createdSession.SessionRef, &session.Event{
		Type: session.EventTypeToolResult,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
			ToolCallID:    "call-1",
			Kind:          "RUN_COMMAND",
			RawOutput:     map[string]any{"stdout": "ok"},
		}},
	})
	if err == nil {
		t.Fatal("AppendEvent() error = nil, want protocol-only tool result rejected")
	}
	if detail := session.EventValidationDetail(err); !strings.Contains(detail, "Event.Tool") {
		t.Fatalf("validation detail = %q, want missing Event.Tool", detail)
	}
}

func TestStoreEventsRejectsLegacySemanticEventLog(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-1" },
	})
	ctx := context.Background()
	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}
	writeRawEventLogForTest(t, store, createdSession, `{"id":"evt-legacy","type":"user","visibility":"canonical","user_message":{"role":"user","parts":[{"kind":"text","text":"hello"}]}}`)

	_, err = store.Events(ctx, session.EventsRequest{SessionRef: createdSession.SessionRef})
	if !errors.Is(err, session.ErrUnsupportedLegacyFormat) {
		t.Fatalf("Events() error = %v, want ErrUnsupportedLegacyFormat", err)
	}
	if !strings.Contains(err.Error(), "legacy semantic field") {
		t.Fatalf("Events() error = %v, want legacy semantic field detail", err)
	}
}

func TestStoreEventsRejectsLegacyEmbeddedDocumentEvents(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-1" },
	})
	ctx := context.Background()
	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}
	path, err := store.resolveWritePath(createdSession)
	if err != nil {
		t.Fatalf("resolveWritePath() error = %v", err)
	}
	raw := map[string]any{
		"kind":    documentKind,
		"version": documentVersion,
		"session": createdSession,
		"events": []any{
			map[string]any{"id": "evt-embedded", "type": "user"},
		},
		"state": map[string]any{},
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("Marshal(legacy doc) error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(legacy doc) error = %v", err)
	}

	_, err = store.Events(ctx, session.EventsRequest{SessionRef: createdSession.SessionRef})
	if !errors.Is(err, session.ErrUnsupportedLegacyFormat) {
		t.Fatalf("Events() error = %v, want ErrUnsupportedLegacyFormat", err)
	}
	if !strings.Contains(err.Error(), "legacy embedded events") {
		t.Fatalf("Events() error = %v, want embedded events detail", err)
	}
}

func TestStoreEventsUpgradesLegacyCustomPluginContextEvent(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-1" },
	})
	ctx := context.Background()
	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}
	text := "[Plugin context: prompt-plugin]\nlegacy hook context"
	message := model.NewTextMessage(model.RoleUser, text)
	legacy := session.Event{
		ID:         "evt-legacy-plugin-context",
		SessionID:  createdSession.SessionID,
		Type:       session.EventTypeCustom,
		Visibility: session.VisibilityCanonical,
		Message:    &message,
		Text:       text,
		Meta: map[string]any{
			"source":                 "plugin_hook",
			"hidden_from_transcript": true,
		},
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("Marshal(legacy event) error = %v", err)
	}
	writeRawEventLogForTest(t, store, createdSession, string(raw))

	events, err := store.Events(ctx, session.EventsRequest{SessionRef: createdSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("Events() len = %d, want 1", len(events))
	}
	if events[0].Type != session.EventTypeContext {
		t.Fatalf("legacy event type = %q, want context", events[0].Type)
	}
	if events[0].Message == nil || events[0].Message.TextContent() != text {
		t.Fatalf("legacy event message = %#v, want preserved plugin context", events[0].Message)
	}

	loaded, err := NewService(store).LoadSession(ctx, session.LoadSessionRequest{SessionRef: createdSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if len(loaded.Events) != 1 || loaded.Events[0].Type != session.EventTypeContext {
		t.Fatalf("LoadSession() events = %#v, want upgraded context event", loaded.Events)
	}
}

func TestStoreEventsRejectsInvalidEventLog(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-1" },
	})
	ctx := context.Background()
	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}
	writeRawEventLogForTest(t, store, createdSession, `{"id":"evt-invalid","type":"user","visibility":"canonical"}`)

	_, err = store.Events(ctx, session.EventsRequest{SessionRef: createdSession.SessionRef})
	if !errors.Is(err, session.ErrInvalidEvent) {
		t.Fatalf("Events() error = %v, want ErrInvalidEvent", err)
	}
	if detail := session.EventValidationDetail(err); !strings.Contains(detail, "Event.Message") {
		t.Fatalf("validation detail = %q, want missing Event.Message", detail)
	}
}

func TestStoreAppendSkipsHiddenPluginContextForGeneratedTitle(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-1" },
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

	hookText := "[Plugin context: prompt-plugin]\nHOOK PREFIX"
	if _, err := store.AppendEvent(ctx, createdSession.SessionRef, &session.Event{
		Type:       session.EventTypeContext,
		Visibility: session.VisibilityCanonical,
		Message:    ptrMessage(model.NewTextMessage(model.RoleUser, hookText)),
		Text:       hookText,
		Meta: map[string]any{
			"source":                 "plugin_hook",
			"hidden_from_transcript": true,
		},
	}); err != nil {
		t.Fatalf("AppendEvent(hook context) error = %v", err)
	}
	if _, err := store.AppendEvent(ctx, createdSession.SessionRef, &session.Event{
		Message: ptrMessage(model.NewTextMessage(model.RoleUser, "真实的用户消息")),
	}); err != nil {
		t.Fatalf("AppendEvent(user) error = %v", err)
	}

	list, err := store.List(ctx, session.ListSessionsRequest{
		AppName:      "caelis",
		UserID:       "user-1",
		WorkspaceKey: "ws-1",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got, want := list.Sessions[0].Title, "真实的用户消息"; got != want {
		t.Fatalf("List title = %q, want %q", got, want)
	}
}

func TestStoreAppendUsesDisplayTextForGeneratedTitle(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-1" },
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

	modelText := "The user referenced these resources. Treat them as explicit instructions for this turn:\n- Load skill `cmpctl` before taking task actions, then follow its instructions.\n\nUser request:\narchive preflight"
	displayText := "$cmpctl archive preflight"
	if _, err := store.AppendEvent(ctx, createdSession.SessionRef, &session.Event{
		Type:       session.EventTypeUser,
		Visibility: session.VisibilityCanonical,
		Message:    ptrMessage(model.NewTextMessage(model.RoleUser, modelText)),
		Text:       displayText,
	}); err != nil {
		t.Fatalf("AppendEvent(user) error = %v", err)
	}

	gotSession, err := store.Get(ctx, createdSession.SessionRef)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if gotSession.Title != displayText {
		t.Fatalf("session title = %q, want display text %q", gotSession.Title, displayText)
	}
}

func TestStoreLoadRejectsToolResultNameMismatch(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "s-hxiurg3hq57a" },
	})
	ctx := context.Background()
	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	message := model.Message{
		Role: model.RoleTool,
		Parts: []model.Part{{
			Kind: model.PartKindToolResult,
			ToolResult: &model.ToolResultPart{
				ToolUseID: "call-1",
				Name:      "WRITE",
				Content:   []model.Part{model.NewJSONPart([]byte(`{"result":"ok"}`))},
			},
		}},
	}
	legacy := &session.Event{
		ID:         "event-55",
		Type:       session.EventTypeToolResult,
		Visibility: session.VisibilityCanonical,
		Tool: &session.EventTool{
			ID:     "call-1",
			Name:   "Write",
			Status: "completed",
			Output: map[string]any{"result": "ok"},
		},
		Message: &message,
		Meta: map[string]any{
			"caelis": map[string]any{
				"version": 1,
				"runtime": map[string]any{
					"tool": map[string]any{
						"name": "WRITE",
					},
				},
			},
		},
	}
	path, err := store.resolveWritePath(createdSession)
	if err != nil {
		t.Fatalf("resolveWritePath() error = %v", err)
	}
	if err := store.appendEventLog(path, []*session.Event{legacy}); err != nil {
		t.Fatalf("appendEventLog() error = %v", err)
	}

	_, err = NewService(store).LoadSession(ctx, session.LoadSessionRequest{SessionRef: createdSession.SessionRef})
	if !errors.Is(err, session.ErrInvalidEvent) {
		t.Fatalf("LoadSession() error = %v, want ErrInvalidEvent", err)
	}
	if detail := session.EventValidationDetail(err); !strings.Contains(detail, "name") {
		t.Fatalf("validation detail = %q, want name mismatch detail", detail)
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
	if !strings.HasPrefix(first.ID, "event-") {
		t.Fatalf("first event id = %q, want durable event- prefix", first.ID)
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

func TestServiceAppendEventCASAndStableIDConflict(t *testing.T) {
	t.Parallel()

	service := NewService(NewStore(Config{RootDir: t.TempDir(), SessionIDGenerator: func() string { return "sess-cas" }}))
	ctx := context.Background()
	created, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	message := model.NewTextMessage(model.RoleUser, "stable")
	zero := uint64(0)
	first, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:       created.SessionRef,
		ExpectedRevision: &zero,
		Event:            &session.Event{ID: "event-stable", Type: session.EventTypeUser, Message: &message},
	})
	if err != nil {
		t.Fatalf("AppendEvent(first) error = %v", err)
	}
	if first.Seq != 1 {
		t.Fatalf("first.Seq = %d, want 1", first.Seq)
	}
	one := uint64(1)
	reopened := NewService(NewStore(Config{RootDir: service.store.rootDir}))
	retried, err := reopened.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:       created.SessionRef,
		ExpectedRevision: &one,
		Event: &session.Event{
			ID:      "event-stable",
			Type:    session.EventTypeUser,
			Message: &message,
			Text:    message.TextContent(),
		},
	})
	if err != nil {
		t.Fatalf("AppendEvent(retry after reopen) error = %v", err)
	}
	if retried.Seq != first.Seq {
		t.Fatalf("retry seq = %d, want %d", retried.Seq, first.Seq)
	}
	different := model.NewTextMessage(model.RoleUser, "different")
	_, err = reopened.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:       created.SessionRef,
		ExpectedRevision: &one,
		Event:            &session.Event{ID: "event-stable", Type: session.EventTypeUser, Message: &different},
	})
	if !errors.Is(err, session.ErrEventConflict) {
		t.Fatalf("AppendEvent(conflict) error = %v, want ErrEventConflict", err)
	}
	loaded, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: created.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if loaded.Session.Revision != 1 || len(loaded.Events) != 1 {
		t.Fatalf("loaded after conflict = revision %d events %d, want 1/1", loaded.Session.Revision, len(loaded.Events))
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

func TestStoreListSurfacesCorruptSessionIndex(t *testing.T) {
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

	_, err = store.List(ctx, session.ListSessionsRequest{
		AppName:      "caelis",
		UserID:       "user-1",
		WorkspaceKey: "ws-1",
	})
	if err == nil {
		t.Fatal("List() error = nil, want corrupt SQLite index failure")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "session index") {
		t.Fatalf("List() error = %v, want session index failure", err)
	}
}

func TestStoreListSurfacesSessionIndexOpenError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctx := context.Background()
	indexPath := filepath.Join(root, indexFilename)
	if err := os.Mkdir(indexPath, 0o700); err != nil {
		t.Fatalf("Mkdir(index path) error = %v", err)
	}

	_, err := NewStore(Config{RootDir: root}).List(ctx, session.ListSessionsRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err == nil {
		t.Fatal("List() error = nil, want index open failure")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "database") {
		t.Fatalf("List() error = %v, want database open failure to be surfaced", err)
	}
}

func TestStoreWriteSurfacesCorruptSessionIndexBeforeUpsert(t *testing.T) {
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
	}, session.ControllerBinding{ControllerID: "controller-1"}); err == nil {
		t.Fatal("BindController() error = nil, want corrupt SQLite index failure")
	} else if !strings.Contains(strings.ToLower(err.Error()), "session index") {
		t.Fatalf("BindController() error = %v, want session index failure", err)
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

func TestStoreConcurrentReadersAndWritersAcrossStoreInstances(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	baseTime := time.Date(2026, time.April, 19, 11, 22, 33, 0, time.UTC)
	baseStore := NewStore(Config{
		RootDir: root,
		Clock:   func() time.Time { return baseTime },
	})
	createdSession, err := baseStore.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
		Title: "shared session",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	const workers = 24
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			store := NewStore(Config{
				RootDir: root,
				Clock: func() time.Time {
					return baseTime.Add(time.Duration(i+1) * time.Second)
				},
			})
			switch i % 3 {
			case 0:
				_, err := store.List(ctx, session.ListSessionsRequest{
					AppName:      "caelis",
					UserID:       "user-1",
					WorkspaceKey: "ws-1",
				})
				errs <- err
			case 1:
				_, err := store.AppendEvent(ctx, createdSession.SessionRef, &session.Event{
					Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, fmt.Sprintf("event %02d", i))),
				})
				errs <- err
			default:
				errs <- store.UpdateState(ctx, createdSession.SessionRef, func(state map[string]any) (map[string]any, error) {
					state[fmt.Sprintf("worker_%02d", i)] = true
					return state, nil
				})
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent store operation error = %v", err)
		}
	}

	list, err := NewStore(Config{RootDir: root}).List(ctx, session.ListSessionsRequest{
		AppName:      "caelis",
		UserID:       "user-1",
		WorkspaceKey: "ws-1",
	})
	if err != nil {
		t.Fatalf("List() after concurrent operations error = %v", err)
	}
	if got := len(list.Sessions); got != 1 {
		t.Fatalf("len(List().Sessions) = %d, want 1", got)
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

func TestStorePutParticipantWithEventRejectsInvalidEventAtomically(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-participant-atomic" },
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

	_, _, err = store.PutParticipantWithEvent(ctx, session.PutParticipantWithEventRequest{
		SessionRef: createdSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:        "reviewer",
			Kind:      session.ParticipantKindACP,
			Role:      session.ParticipantRoleSidecar,
			AgentName: "reviewer",
			Label:     "@reviewer",
		},
		Event: &session.Event{
			Type:       session.EventTypeUser,
			Visibility: session.VisibilityCanonical,
			Text:       "missing durable message",
		},
	})
	if err == nil {
		t.Fatal("PutParticipantWithEvent() error = nil, want invalid event rejection")
	}
	loaded, err := store.Get(ctx, createdSession.SessionRef)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(loaded.Participants) != 0 {
		t.Fatalf("Participants after failed atomic put = %#v, want none", loaded.Participants)
	}
	events, err := store.Events(ctx, session.EventsRequest{SessionRef: createdSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events after failed atomic put = %#v, want none", events)
	}
}

func TestStoreBindControllerWithEventDoesNotSplitOnPrecommitFailure(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{RootDir: t.TempDir(), SessionIDGenerator: func() string { return "sess-handoff-atomic" }})
	ctx := context.Background()
	created, err := store.GetOrCreate(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}
	store.writeDocumentFault = func() error { return errors.New("precommit failure") }
	message := model.NewTextMessage(model.RoleSystem, "handoff")
	zero := uint64(0)
	_, _, err = store.BindControllerWithEvent(ctx, session.BindControllerWithEventRequest{
		SessionRef:       created.SessionRef,
		ExpectedRevision: &zero,
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: "reviewer", EpochID: "epoch-1",
		},
		Event: &session.Event{
			ID: "handoff-1", Type: session.EventTypeHandoff, Message: &message,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "precommit failure") {
		t.Fatalf("BindControllerWithEvent() error = %v, want precommit failure", err)
	}
	store.writeDocumentFault = nil
	loaded, err := NewService(store).LoadSession(ctx, session.LoadSessionRequest{SessionRef: created.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if loaded.Session.Controller.ControllerID != "" || loaded.Session.Revision != 0 || len(loaded.Events) != 0 {
		t.Fatalf("split handoff state after failure: session=%#v events=%#v", loaded.Session, loaded.Events)
	}
}

func TestStorePutParticipantWithEventDoesNotAppendLogWhenDocumentWriteFails(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-participant-write-failure" },
		EventIDGenerator:   func() string { return "evt-participant" },
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

	store.writeDocumentFault = func() error {
		return errors.New("forced document write failure")
	}
	lifecycleMessage := model.NewTextMessage(model.RoleSystem, "participant attached")
	_, _, err = store.PutParticipantWithEvent(ctx, session.PutParticipantWithEventRequest{
		SessionRef: createdSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:        "reviewer",
			Kind:      session.ParticipantKindACP,
			Role:      session.ParticipantRoleSidecar,
			AgentName: "reviewer",
			Label:     "@reviewer",
		},
		Event: &session.Event{
			Type:       session.EventTypeLifecycle,
			Visibility: session.VisibilityCanonical,
			Message:    &lifecycleMessage,
			Text:       lifecycleMessage.TextContent(),
			Lifecycle:  &session.EventLifecycle{Status: "attached"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "forced document write failure") {
		t.Fatalf("PutParticipantWithEvent() error = %v, want forced write failure", err)
	}
	store.writeDocumentFault = nil

	loaded, err := store.Get(ctx, createdSession.SessionRef)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(loaded.Participants) != 0 {
		t.Fatalf("Participants after failed atomic put = %#v, want none", loaded.Participants)
	}
	events, err := store.Events(ctx, session.EventsRequest{SessionRef: createdSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events after failed document write = %#v, want none", events)
	}
}

func TestStoreAppendEventDoesNotAppendLogWhenDocumentWriteFails(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-append-write-failure" },
		EventIDGenerator:   func() string { return "evt-append" },
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

	store.writeDocumentFault = func() error {
		return errors.New("forced document write failure")
	}
	message := model.NewTextMessage(model.RoleUser, "hello")
	_, err = store.AppendEvent(ctx, createdSession.SessionRef, &session.Event{
		Type:    session.EventTypeUser,
		Message: &message,
		Text:    message.TextContent(),
	})
	if err == nil || !strings.Contains(err.Error(), "forced document write failure") {
		t.Fatalf("AppendEvent() error = %v, want forced write failure", err)
	}
	store.writeDocumentFault = nil

	events, err := store.Events(ctx, session.EventsRequest{SessionRef: createdSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events after failed append write = %#v, want none", events)
	}
}

func TestStoreAppendEventKeepsLogWhenDocumentWriteFailsAfterCommit(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewStore(Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-late-document-failure" },
		EventIDGenerator:   func() string { return "evt-late-document-failure" },
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

	indexPath := filepath.Join(root, indexFilename)
	if err := os.Remove(indexPath); err != nil {
		t.Fatalf("Remove(index) error = %v", err)
	}
	if err := os.Mkdir(indexPath, 0o700); err != nil {
		t.Fatalf("Mkdir(index path) error = %v", err)
	}

	message := model.NewTextMessage(model.RoleUser, "hello after commit")
	_, err = store.AppendEvent(ctx, createdSession.SessionRef, &session.Event{
		Type:    session.EventTypeUser,
		Message: &message,
		Text:    message.TextContent(),
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "session index") {
		t.Fatalf("AppendEvent() error = %v, want late session index failure", err)
	}

	events, err := store.Events(ctx, session.EventsRequest{SessionRef: createdSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("Events after late document write failure = %#v, want committed event", events)
	}
	if events[0].ID != "evt-late-document-failure" {
		t.Fatalf("Event ID after late document write failure = %q, want generated ID", events[0].ID)
	}
	loaded, err := store.Get(ctx, createdSession.SessionRef)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if loaded.Title == "" {
		t.Fatal("Session title after late document write failure is empty, want committed document update")
	}
}

func TestStoreWALRecoversCommittedEventAndStateAfterCrashPoints(t *testing.T) {
	for _, phase := range []string{"after_commit", "after_event_log"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			store := NewStore(Config{RootDir: root, SessionIDGenerator: func() string { return "sess-wal" }})
			ctx := context.Background()
			created, err := store.GetOrCreate(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
			if err != nil {
				t.Fatalf("GetOrCreate() error = %v", err)
			}
			store.transactionFault = func(current string) error {
				if current == phase {
					return errors.New("simulated crash at " + phase)
				}
				return nil
			}
			message := model.NewTextMessage(model.RoleUser, "durable through WAL")
			zero := uint64(0)
			_, err = store.AppendEventsAndUpdateState(ctx, session.AppendEventsAndUpdateStateRequest{
				SessionRef:       created.SessionRef,
				ExpectedRevision: &zero,
				TransactionID:    "transaction-wal",
				Events: []*session.Event{{
					ID: "event-wal", Type: session.EventTypeUser, Message: &message,
				}},
				UpdateState: func(_ []*session.Event, state map[string]any) (map[string]any, error) {
					state["cursor"] = "committed"
					return state, nil
				},
			})
			var committed *CommittedError
			if !errors.As(err, &committed) {
				t.Fatalf("AppendEventsAndUpdateState() error = %v, want *CommittedError", err)
			}

			reopened := NewService(NewStore(Config{RootDir: root}))
			loaded, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: created.SessionRef})
			if err != nil {
				t.Fatalf("LoadSession(recovery) error = %v", err)
			}
			if loaded.Session.Revision != 1 || len(loaded.Events) != 1 || loaded.Events[0].ID != "event-wal" || loaded.Events[0].Seq != 1 {
				t.Fatalf("recovered session/events = revision %d events %#v", loaded.Session.Revision, loaded.Events)
			}
			if got := loaded.State["cursor"]; got != "committed" {
				t.Fatalf("recovered state cursor = %v, want committed", got)
			}
			replayed, ok := session.ModelMessageOf(loaded.Events[0])
			if !ok || !reflect.DeepEqual(replayed, message) {
				t.Fatalf("replayed model context = %#v, want runtime message %#v", replayed, message)
			}

			one := uint64(1)
			retried, err := reopened.AppendEvent(ctx, session.AppendEventRequest{
				SessionRef:       created.SessionRef,
				ExpectedRevision: &one,
				Event:            &session.Event{ID: "event-wal", Type: session.EventTypeUser, Message: &message},
			})
			if err != nil || retried.Seq != 1 {
				t.Fatalf("idempotent retry = event %#v error %v, want existing seq 1", retried, err)
			}
			events, err := reopened.Events(ctx, session.EventsRequest{SessionRef: created.SessionRef})
			if err != nil || len(events) != 1 {
				t.Fatalf("Events(after retry) = %#v, %v, want one", events, err)
			}
		})
	}
}

func TestStoreCompoundCommittedErrorRetryDoesNotReapplyState(t *testing.T) {
	root := t.TempDir()
	store := NewStore(Config{RootDir: root, SessionIDGenerator: func() string { return "sess-compound-committed" }})
	service := NewService(store)
	created, err := service.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	store.transactionFault = func(phase string) error {
		if phase == "after_commit" {
			store.transactionFault = nil
			return errors.New("simulated committed compound failure")
		}
		return nil
	}
	message := model.NewTextMessage(model.RoleUser, "committed compound retry")
	event := &session.Event{ID: "event-compound-committed", IdempotencyKey: "fact:compound-committed", Type: session.EventTypeUser, Message: &message}
	stateCalls := 0
	request := func() session.AppendEventsAndUpdateStateRequest {
		return session.AppendEventsAndUpdateStateRequest{
			SessionRef: created.SessionRef, TransactionID: "transaction-compound-committed", Events: []*session.Event{event},
			UpdateState: func(_ []*session.Event, state map[string]any) (map[string]any, error) {
				stateCalls++
				state["count"] = float64(stateCalls)
				return state, nil
			},
		}
	}
	if _, err := service.AppendEventsAndUpdateState(context.Background(), request()); !session.IsCommitted(err) {
		t.Fatalf("first AppendEventsAndUpdateState() error = %v, want committed error", err)
	}

	reopened := NewService(NewStore(Config{RootDir: root}))
	if _, err := reopened.AppendEventsAndUpdateState(context.Background(), request()); err != nil {
		t.Fatalf("retry AppendEventsAndUpdateState() error = %v", err)
	}
	loaded, err := reopened.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: created.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if stateCalls != 1 || loaded.State["count"] != float64(1) || len(loaded.Events) != 1 || loaded.Session.Revision != 1 {
		t.Fatalf("retry outcome = calls %d revision %d state %#v events %#v, want one complete transaction", stateCalls, loaded.Session.Revision, loaded.State, loaded.Events)
	}
}

func TestStoreAppendEventsDoesNotAppendLogWhenDocumentWriteFails(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-batch-write-failure" },
		EventIDGenerator:   func() string { return "evt-batch" },
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

	store.writeDocumentFault = func() error {
		return errors.New("forced document write failure")
	}
	prompt := model.NewTextMessage(model.RoleUser, "review this change")
	assistant := model.NewTextMessage(model.RoleAssistant, "allow")
	_, err = store.AppendEvents(ctx, session.AppendEventsRequest{
		SessionRef: createdSession.SessionRef,
		Events: []*session.Event{
			{Type: session.EventTypeUser, Message: &prompt, Text: prompt.TextContent()},
			{Type: session.EventTypeAssistant, Message: &assistant, Text: assistant.TextContent()},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "forced document write failure") {
		t.Fatalf("AppendEvents() error = %v, want forced write failure", err)
	}
	store.writeDocumentFault = nil

	events, err := store.Events(ctx, session.EventsRequest{SessionRef: createdSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events after failed batch write = %#v, want none", events)
	}
}

func TestStoreAppendEventsAndUpdateStateDoesNotAppendLogWhenStateUpdateFails(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-batch-state-failure" },
		EventIDGenerator:   func() string { return "evt-batch-state" },
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

	prompt := model.NewTextMessage(model.RoleUser, "review this change")
	assistant := model.NewTextMessage(model.RoleAssistant, "allow")
	_, err = store.AppendEventsAndUpdateState(ctx, session.AppendEventsAndUpdateStateRequest{
		SessionRef:    createdSession.SessionRef,
		TransactionID: "transaction-state-failure",
		Events: []*session.Event{
			{Type: session.EventTypeUser, Message: &prompt, Text: prompt.TextContent()},
			{Type: session.EventTypeAssistant, Message: &assistant, Text: assistant.TextContent()},
		},
		UpdateState: func([]*session.Event, map[string]any) (map[string]any, error) {
			return nil, errors.New("forced state update failure")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "forced state update failure") {
		t.Fatalf("AppendEventsAndUpdateState() error = %v, want forced state update failure", err)
	}

	events, err := store.Events(ctx, session.EventsRequest{SessionRef: createdSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events after failed batch state update = %#v, want none", events)
	}
	state, err := store.SnapshotState(ctx, createdSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if len(state) != 0 {
		t.Fatalf("State after failed batch state update = %#v, want empty", state)
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

func TestStoreSnapshotStateRepairsMissingDocumentState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	at := time.Date(2026, time.April, 19, 11, 22, 33, 0, time.UTC)
	store := NewStore(Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-1" },
		Clock:              func() time.Time { return at },
	})
	ref, err := store.GetOrCreate(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws-1",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}
	docPath, err := store.resolveDocumentPath(ref.SessionID, ref.WorkspaceKey)
	if err != nil {
		t.Fatalf("resolveDocumentPath() error = %v", err)
	}
	data, err := json.MarshalIndent(map[string]any{
		"kind":    documentKind,
		"version": documentVersion,
		"session": session.Session{
			SessionRef: ref.SessionRef,
			CreatedAt:  at,
			UpdatedAt:  at,
		},
	}, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(docPath, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("WriteFile(doc) error = %v", err)
	}

	reloaded := NewStore(Config{
		RootDir: root,
		Clock:   func() time.Time { return at.Add(time.Minute) },
	})
	state, err := reloaded.SnapshotState(context.Background(), ref.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if state == nil || len(state) != 0 {
		t.Fatalf("SnapshotState() = %#v, want repaired empty state", state)
	}

	repairedData, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("ReadFile(repaired doc) error = %v", err)
	}
	var repaired persistedDocument
	if err := json.Unmarshal(repairedData, &repaired); err != nil {
		t.Fatalf("Unmarshal(repaired doc) error = %v", err)
	}
	if repaired.State == nil || len(repaired.State) != 0 {
		t.Fatalf("repaired State = %#v, want empty map", repaired.State)
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
	if len(first.SessionID) > 32 || len(second.SessionID) > 32 {
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

func TestStoreGetOrCreateRejectsSameSessionIDAcrossWorkspaces(t *testing.T) {
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
	_, err = store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "user-1",
		PreferredSessionID: "default",
		Workspace: session.WorkspaceRef{
			Key: "ws-b",
			CWD: "/tmp/ws-b",
		},
	})
	if !errors.Is(err, session.ErrInvalidSession) {
		t.Fatalf("GetOrCreate(ws-b) error = %v, want ErrInvalidSession", err)
	}

	if first.WorkspaceKey != "ws-a" {
		t.Fatalf("first.WorkspaceKey = %q, want ws-a", first.WorkspaceKey)
	}

	firstPath := rolloutDocumentPath(root, "ws-a", at, "default")
	firstDoc := readPersistedDocument(t, firstPath)
	if firstDoc.Session.WorkspaceKey != "ws-a" {
		t.Fatalf("first document workspace = %q, want ws-a", firstDoc.Session.WorkspaceKey)
	}
	secondPath := rolloutDocumentPath(root, "ws-b", at, "default")
	if _, err := os.Stat(secondPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second document stat error = %v, want not exist", err)
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

func TestStoreGlobalSessionIDResolvesAcrossStoreReopen(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewStore(Config{RootDir: root})
	ctx := context.Background()
	created, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "user-1",
		PreferredSessionID: "shared",
		Workspace: session.WorkspaceRef{
			Key: "ws-a",
			CWD: "/tmp/ws-a",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate(shared) error = %v", err)
	}

	reloaded := NewService(NewStore(Config{RootDir: root}))
	loaded, err := reloaded.Session(ctx, session.SessionRef{
		AppName:   "caelis",
		UserID:    "user-1",
		SessionID: "shared",
	})
	if err != nil {
		t.Fatalf("Session(without workspace) error = %v", err)
	}
	if loaded.SessionID != created.SessionID {
		t.Fatalf("loaded session id = %q, want %q", loaded.SessionID, created.SessionID)
	}
	if got := loaded.WorkspaceKey; got != "ws-a" {
		t.Fatalf("loaded workspace = %q, want ws-a", got)
	}
}

func TestStoreIgnoresLegacyFlatSessionDocuments(t *testing.T) {
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
	if _, err := store.Get(context.Background(), session.SessionRef{
		AppName:      "caelis",
		UserID:       "user-1",
		SessionID:    "session-1",
		WorkspaceKey: "ws-1",
	}); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("Get(legacy flat) error = %v, want ErrSessionNotFound", err)
	}

	list, err := store.List(context.Background(), session.ListSessionsRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list.Sessions) != 0 {
		t.Fatalf("List() = %#v, want legacy flat document ignored", list.Sessions)
	}
}

func writeRawEventLogForTest(t *testing.T, store *Store, sess session.Session, lines ...string) {
	t.Helper()
	path, err := store.resolveWritePath(sess)
	if err != nil {
		t.Fatalf("resolveWritePath() error = %v", err)
	}
	logPath := eventLogPath(path)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatalf("MkdirAll(event log dir) error = %v", err)
	}
	data := strings.Join(lines, "\n")
	if data != "" {
		data += "\n"
	}
	if err := os.WriteFile(logPath, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile(event log) error = %v", err)
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
