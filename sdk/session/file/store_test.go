package file

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
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

	session, err := store.GetOrCreate(ctx, sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	if _, err := store.AppendEvent(ctx, session.SessionRef, &sdksession.Event{
		Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "hello")),
		Text:    "hello",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if _, err := store.AppendEvent(ctx, session.SessionRef, sdksession.MarkNotice(&sdksession.Event{
		Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleSystem, "warn: retrying")),
	}, "warn", "retrying")); err != nil {
		t.Fatalf("AppendEvent(notice) error = %v", err)
	}

	events, err := store.Events(ctx, sdksession.EventsRequest{SessionRef: session.SessionRef})
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
	text := string(data)
	if !strings.Contains(text, "\"hello\"") {
		t.Fatal("persisted file must contain canonical event text")
	}
	if strings.Contains(text, "retrying") {
		t.Fatal("persisted file must not contain transient notice text")
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

	session, err := service.StartSession(ctx, sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	if err := service.UpdateState(ctx, session.SessionRef, func(state map[string]any) (map[string]any, error) {
		state["mode"] = "default"
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}

	session, err = service.PutParticipant(ctx, sdksession.PutParticipantRequest{
		SessionRef: session.SessionRef,
		Binding: sdksession.ParticipantBinding{
			ID:            "part-1",
			Kind:          sdksession.ParticipantKindSubagent,
			Role:          sdksession.ParticipantRoleDelegated,
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

	if got, want := len(session.Participants), 1; got != want {
		t.Fatalf("len(participants) = %d, want %d", got, want)
	}
	if got := session.Participants[0].SessionID; got != "child-1" {
		t.Fatalf("participant session_id = %q, want %q", got, "child-1")
	}

	state, err := service.SnapshotState(ctx, session.SessionRef)
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
	session, err := store.GetOrCreate(ctx, sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}
	docPath := rolloutDocumentPath(root, "ws-1", at, session.SessionID)
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
	req := sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
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

	list, err := NewStore(Config{RootDir: root}).List(ctx, sdksession.ListSessionsRequest{
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

	first, err := store.GetOrCreate(ctx, sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate(first) error = %v", err)
	}
	if _, err := store.AppendEvent(ctx, first.SessionRef, &sdksession.Event{
		Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "first")),
		Text:    "first",
	}); err != nil {
		t.Fatalf("AppendEvent(first) error = %v", err)
	}

	now = now.Add(2 * time.Hour)
	second, err := store.GetOrCreate(ctx, sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate(second) error = %v", err)
	}
	if _, err := store.AppendEvent(ctx, second.SessionRef, &sdksession.Event{
		Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "second")),
		Text:    "second",
	}); err != nil {
		t.Fatalf("AppendEvent(second) error = %v", err)
	}

	list, err := NewStore(Config{RootDir: root}).List(ctx, sdksession.ListSessionsRequest{
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
	first, err := store.GetOrCreate(ctx, sdksession.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "user-1",
		PreferredSessionID: "default",
		Workspace: sdksession.WorkspaceRef{
			Key: "ws-a",
			CWD: "/tmp/ws-a",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate(ws-a) error = %v", err)
	}
	second, err := store.GetOrCreate(ctx, sdksession.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "user-1",
		PreferredSessionID: "default",
		Workspace: sdksession.WorkspaceRef{
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

	session, err := service.StartSession(ctx, sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "acp",
		Workspace: sdksession.WorkspaceRef{
			Key: "/tmp/ws",
			CWD: "/tmp/ws",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	loaded, err := service.Session(ctx, sdksession.SessionRef{
		AppName:   "caelis",
		UserID:    "acp",
		SessionID: session.SessionID,
	})
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if loaded.SessionID != session.SessionID {
		t.Fatalf("loaded session id = %q, want %q", loaded.SessionID, session.SessionID)
	}
	if loaded.WorkspaceKey != session.WorkspaceKey {
		t.Fatalf("loaded workspace key = %q, want %q", loaded.WorkspaceKey, session.WorkspaceKey)
	}
}

func TestStorePreservesLegacyFlatSessionDocuments(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	legacyPath := filepath.Join(root, "session-1.json")
	writePersistedDocument(t, legacyPath, persistedDocument{
		Kind:    documentKind,
		Version: documentVersion,
		Session: sdksession.Session{
			SessionRef: sdksession.SessionRef{
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
	if _, err := store.GetOrCreate(context.Background(), sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
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

func ptrMessage(message sdkmodel.Message) *sdkmodel.Message {
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
