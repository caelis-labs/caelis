package file

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStoreCurrentDocumentRoundTripsSealedACPPlacement(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctx := context.Background()
	store := newLogicalTestStore(t, Config{RootDir: root})
	active, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "current-user",
		PreferredSessionID: "current-session",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	sealed, err := placement.Seal(placement.Placement{
		Kind:              placement.KindAgent,
		ProfileID:         "profile/current-acp",
		Agent:             "current-agent",
		Model:             "current-model",
		ReasoningEffort:   "xhigh",
		ConfigFingerprint: "current-config",
	})
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	binding := session.ParticipantBinding{
		ID:                   "current-acp",
		Kind:                 session.ParticipantKindACP,
		Role:                 session.ParticipantRoleSidecar,
		AgentName:            "current-agent",
		Placement:            sealed,
		SessionID:            "remote-current",
		AttachmentGeneration: "generation-1",
	}
	if _, err := store.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: active.SessionRef,
		Binding:    binding,
	}); err != nil {
		t.Fatalf("PutParticipant() error = %v", err)
	}

	loaded, err := newLogicalTestStore(t, Config{RootDir: root}).LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := loaded.Session.Participants, []session.ParticipantBinding{session.CloneParticipantBinding(binding)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("round-tripped participants = %#v, want %#v", got, want)
	}

	path, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatalf("resolveWritePath() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		t.Fatalf("Unmarshal(header) error = %v", err)
	}
	if header.Version != documentVersion {
		t.Fatalf("current document version = %d, want %d", header.Version, documentVersion)
	}
}

func TestStoreUnreadableSessionLeavesOriginalBytes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		data   string
		log    bool
		legacy bool
	}{
		{"v1 document", `{"kind":"caelis.sdk.session","version":1,"session":{"session_id":"old"}}`, false, true},
		{"v1 retired field shape", `{"state":[],"kind":"caelis.sdk.session","version":1}`, false, true},
		{"future document", `{"kind":"caelis.sdk.session","version":3}`, false, false},
		{"malformed document", `{`, false, false},
		{"malformed log", "{\n", true, false},
		{"future log", "{\"schema\":99}\n", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			store := newLogicalTestStore(t, Config{RootDir: t.TempDir()})
			req := session.StartSessionRequest{AppName: "app", UserID: "user", PreferredSessionID: "old"}
			old, err := store.StartSession(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			path := store.newDocumentPath(old)
			if tc.log {
				path = eventLogPath(path)
			}
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.LoadSession(ctx, session.LoadSessionRequest{SessionRef: old.SessionRef}); !errors.Is(err, session.ErrUnreadableSession) || errors.Is(err, session.ErrUnsupportedLegacyFormat) != tc.legacy {
				t.Fatalf("LoadSession damaged data = %v", err)
			}
			if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: old.SessionRef, Event: &session.Event{Type: session.EventTypeLifecycle}}); !errors.Is(err, session.ErrUnreadableSession) {
				t.Fatalf("AppendEvent to damaged data = %v", err)
			}
			req.PreferredSessionID = "fresh"
			if _, err := store.StartSession(ctx, req); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(raw, []byte(tc.data)) {
				t.Fatalf("unreadable bytes changed: %q, %v", raw, err)
			}
		})
	}
}

func TestUnreadableIndexRowPreservesPaginationAndExactReadFailure(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newLogicalTestStore(t, Config{RootDir: t.TempDir()})
	for _, id := range []string{"healthy", "bad-metadata", "bad-path"} {
		if _, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "app", UserID: "user", PreferredSessionID: id}); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.openSessionIndex()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions SET path='../outside' WHERE session_id='bad-path'; UPDATE sessions SET metadata_json='{' WHERE session_id='bad-metadata'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reader := newLogicalTestStore(t, Config{RootDir: store.rootDir})
	cursor := ""
	for _, id := range []string{"bad-path", "bad-metadata", "healthy"} {
		page, err := reader.ListSessions(ctx, session.ListSessionsRequest{Limit: 1, Cursor: cursor})
		if err != nil || len(page.Sessions) != 1 || page.Sessions[0].SessionID != id {
			t.Fatalf("expected summary %q, got %#v, %v", id, page, err)
		}
		_, err = reader.Session(ctx, page.Sessions[0].SessionRef)
		if id == "healthy" && err != nil || id != "healthy" && !errors.Is(err, session.ErrUnreadableSession) {
			t.Fatalf("exact read for %q: %v", id, err)
		}
		cursor = page.NextCursor
	}
	if cursor != "" {
		t.Fatal("unexpected next page")
	}
}

func TestCanonicalLogIsAppendOnlyAcrossReopen(t *testing.T) {
	t.Parallel()
	store, active := newEventPageIndexFixture(t, 2)
	document, err := store.resolveDocumentPath(active.SessionID, active.WorkspaceKey)
	if err != nil {
		t.Fatal(err)
	}
	path := eventLogPath(document)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	appendLifecycleEvents(t, store, active.SessionRef, 3, 1)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) <= len(before) || !bytes.HasPrefix(after, before) {
		t.Fatal("log is not append-only")
	}
	reopened := newLogicalTestStore(t, Config{RootDir: store.rootDir})
	again, err := reopened.resolveDocumentPath(active.SessionID, active.WorkspaceKey)
	if err != nil || eventLogPath(again) != path {
		t.Fatalf("path changed %s %v", again, err)
	}
	wrong := active.SessionRef
	wrong.UserID = "other-user"
	if _, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: wrong}); err == nil {
		t.Fatal("event history ignored Session ownership")
	}
	events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil || len(events) != 3 {
		t.Fatalf("roundtrip events=%d err=%v", len(events), err)
	}
}

func TestStoreListCWDNormalizesStoredPathsBeforePagination(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	sep := string(filepath.Separator)
	testStoreListCWDVariants(t, workspace, []string{
		workspace,
		workspace + sep,
		workspace + sep + ".",
		workspace + sep + "child" + sep + "..",
	})
}

func TestWindowsStoreListCWDMatchesPathSpellings(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("Windows path comparison")
	}
	for _, workspace := range []string{`C:\Work\项目\Äpp`, `\\Server\Share\项目\Äpp`} {
		t.Run(workspace, func(t *testing.T) {
			testStoreListCWDVariants(t, workspace, []string{
				workspace,
				strings.ToLower(workspace),
				strings.ToUpper(workspace),
				strings.ReplaceAll(workspace, `\`, "/"),
			})
		})
	}
}

func testStoreListCWDVariants(t *testing.T, workspace string, variants []string) {
	t.Helper()
	for _, backend := range []string{"file", "memory"} {
		t.Run(backend, func(t *testing.T) {
			ctx := t.Context()
			now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
			clock := func() time.Time { return now }
			var store, reader session.Lifecycle
			if backend == "file" {
				root := t.TempDir()
				store = newLogicalTestStore(t, Config{RootDir: root, Clock: clock})
				// Listing must also recognize spellings in an existing index after
				// reopening, without rewriting the durable Session's CWD or key.
				reader = newLogicalTestStore(t, Config{RootDir: root})
			} else {
				store = inmemory.NewStore(inmemory.Config{Clock: clock})
				reader = store
			}
			for i, cwd := range append(append([]string(nil), variants...), workspace+"-other", workspace) {
				userID := "user-1"
				if i == len(variants)+1 {
					userID = "other-user"
				}
				if _, err := store.StartSession(ctx, session.StartSessionRequest{
					AppName: "caelis", UserID: userID,
					Workspace:          session.WorkspaceRef{Key: fmt.Sprintf("workspace-%d", i), CWD: cwd},
					PreferredSessionID: fmt.Sprintf("session-%d", i),
				}); err != nil {
					t.Fatal(err)
				}
				now = now.Add(time.Second)
			}
			for _, query := range variants {
				cursor := ""
				for i := len(variants) - 1; i >= 0; i-- {
					page, err := reader.ListSessions(ctx, session.ListSessionsRequest{
						UserID: "user-1", CWD: query, Limit: 1, Cursor: cursor,
					})
					if err != nil {
						t.Fatal(err)
					}
					wantID := fmt.Sprintf("session-%d", i)
					if len(page.Sessions) != 1 || page.Sessions[0].SessionID != wantID {
						t.Fatalf("ListSessions(CWD=%q, cursor=%q) = %#v, want %s", query, cursor, page, wantID)
					}
					summary := page.Sessions[0]
					loaded, err := reader.LoadSession(ctx, session.LoadSessionRequest{SessionRef: summary.SessionRef})
					if err != nil {
						t.Fatal(err)
					}
					if summary.CWD != variants[i] || loaded.Session.CWD != variants[i] || loaded.Session.WorkspaceKey != fmt.Sprintf("workspace-%d", i) {
						t.Fatalf("listing/loading changed durable workspace: summary=%#v, loaded=%#v", summary, loaded.Session)
					}
					if (page.NextCursor != "") != (i > 0) {
						t.Fatalf("NextCursor = %q with %d matches remaining", page.NextCursor, i)
					}
					cursor = page.NextCursor
				}
			}
		})
	}
}

func TestLargeJournalReceiptSurvivesColdAppendAndReopen(t *testing.T) {
	t.Parallel()
	store, active := newLargeJournalFixture(t, 1)
	root := store.rootDir
	store = newLogicalTestStore(t, Config{RootDir: root})
	// Exercise the same cold compact-index path as a log crossing 32 MiB,
	// without making the correctness fixture depend on machine throughput.
	store.eventLogCacheMaxBytes = 1024
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	receipt := session.NewModelInvocationReceipt(model.Invocation{
		ID: "large-journal-receipt", Provider: "test", Model: "test", Outcome: "completed",
	}, "chat")
	persisted, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: receipt})
	if err != nil {
		t.Fatal(err)
	}
	message := model.NewTextMessage(model.RoleAssistant, "final reply after the screenshot")
	if _, err := store.AppendEvent(t.Context(), session.AppendEventRequest{
		SessionRef: active.SessionRef, Event: &session.Event{Type: session.EventTypeAssistant, Message: &message},
	}); err != nil {
		t.Fatal(err)
	}
	reopened := newLogicalTestStore(t, Config{RootDir: root})
	loaded, err := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Events) != 3 || loaded.Events[1].ID != persisted.ID || !session.IsModelInvocationReceipt(loaded.Events[1]) {
		t.Fatal("cold append or reopen lost the journal, receipt or final reply")
	}
	if !bytes.Equal(loaded.Events[0].Tool.Result.Content[0].(tool.StructuredContent).Value, largeJournalResult()) {
		t.Fatal("journal result changed across persistence")
	}
	got, ok := session.ModelMessageOf(loaded.Events[2])
	want, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil || !ok || !bytes.Equal(raw, want) {
		t.Fatal("reopened final reply differs from the produced model message")
	}
	// Retrying the original receipt must return its original durable identity.
	again, err := reopened.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: receipt})
	if err != nil || again.ID != persisted.ID || again.Seq != persisted.Seq {
		t.Fatalf("receipt retry = %#v, %v", again, err)
	}
}

func newLargeJournalFixture(t testing.TB, count int) (*Store, session.Session) {
	t.Helper()
	store, active := newEventPageIndexFixture(t, 0)
	result := largeJournalResult()
	events := make([]*session.Event, 0, count)
	for i := range count {
		resultValue := tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: result}}}
		events = append(events, &session.Event{ID: fmt.Sprintf("tool-result-%d", i), Type: session.EventTypeToolResult, Visibility: session.VisibilityMirror, Tool: &session.EventTool{ID: fmt.Sprintf("call-%d", i), Name: "screenshot", Result: &resultValue}})

	}
	if _, err := store.AppendEvents(context.Background(), session.AppendEventsRequest{SessionRef: active.SessionRef, Events: events}); err != nil {
		t.Fatal(err)
	}
	return store, active
}

func largeJournalResult() json.RawMessage {
	return json.RawMessage(`{"image":"` + strings.Repeat("A", 1<<20) + `","integer":900719925474099312345678901234567890,"decimal":1.00}`)
}

func BenchmarkColdReceiptAppendLargeJournal(b *testing.B) {
	store, active := newLargeJournalFixture(b, 33)
	path, err := store.resolveWritePath(active)
	if err != nil {
		b.Fatal(err)
	}
	info, err := os.Stat(eventLogPath(path))
	if err != nil {
		b.Fatal(err)
	}
	if info.Size() <= maxEventLogCacheBytes {
		b.Fatal("fixture must exceed the production full-history cache limit")
	}
	b.SetBytes(info.Size())
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		cold := NewStore(Config{RootDir: store.rootDir})
		receipt := session.NewModelInvocationReceipt(model.Invocation{
			ID: fmt.Sprintf("receipt-%d", i), Provider: "test", Model: "test", Outcome: "completed",
		}, "chat")
		if _, err := cold.AppendEvent(context.Background(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: receipt}); err != nil {
			b.Fatal(err)
		}
	}
}
