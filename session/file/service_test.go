package file

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/session"
)

func tempService(t *testing.T) *Service {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "session-file-test", t.Name())
	t.Cleanup(func() { os.RemoveAll(dir) })
	svc, err := New(Config{RootDir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

func TestCreateAndGet(t *testing.T) {
	svc := tempService(t)
	ctx := context.Background()

	sess, err := svc.Create(ctx, session.CreateRequest{
		AppName: "app", UserID: "user", WorkspaceKey: "ws", Title: "test",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.Get(ctx, sess.Ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "test" {
		t.Errorf("got title %q, want %q", got.Title, "test")
	}
}

func TestGetNotFound(t *testing.T) {
	svc := tempService(t)
	_, err := svc.Get(context.Background(), session.Ref{AppName: "a", UserID: "u", WorkspaceKey: "w", SessionID: "missing"})
	if err == nil {
		t.Error("expected error for missing session")
	}
}

func TestList(t *testing.T) {
	svc := tempService(t)
	ctx := context.Background()
	svc.Create(ctx, session.CreateRequest{AppName: "app", UserID: "u", WorkspaceKey: "ws"})
	svc.Create(ctx, session.CreateRequest{AppName: "app", UserID: "u", WorkspaceKey: "ws"})

	resp, err := svc.List(ctx, session.ListRequest{AppName: "app", UserID: "u"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(resp.Sessions) != 2 {
		t.Errorf("got %d, want 2", len(resp.Sessions))
	}
}

func TestFork(t *testing.T) {
	svc := tempService(t)
	ctx := context.Background()
	orig, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "app", UserID: "u", WorkspaceKey: "ws", Title: "original",
	})
	svc.AppendEvent(ctx, orig.Ref, session.Event{
		Kind: session.EventKindUser, Visibility: session.VisibilityCanonical,
		UserPayload: &session.UserPayload{
			Parts: []session.EventPart{{Kind: session.PartKindText, Text: "hello"}},
		},
	})

	forked, err := svc.Fork(ctx, session.ForkRequest{Source: orig.Ref, Title: "forked"})
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if forked.Title != "forked" {
		t.Errorf("got %q, want %q", forked.Title, "forked")
	}

	evts, _ := svc.Events(ctx, session.EventsRequest{SessionRef: forked.Ref})
	if len(evts) != 1 {
		t.Errorf("got %d events, want 1", len(evts))
	}
}

func TestDelete(t *testing.T) {
	svc := tempService(t)
	ctx := context.Background()
	sess, _ := svc.Create(ctx, session.CreateRequest{AppName: "app", UserID: "u", WorkspaceKey: "ws"})
	if err := svc.Delete(ctx, sess.Ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := svc.Get(ctx, sess.Ref)
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestAppendAndListEvents(t *testing.T) {
	svc := tempService(t)
	ctx := context.Background()
	sess, _ := svc.Create(ctx, session.CreateRequest{AppName: "app", UserID: "u", WorkspaceKey: "ws"})

	svc.AppendEvent(ctx, sess.Ref, session.Event{
		Kind: session.EventKindUser, Visibility: session.VisibilityCanonical,
		UserPayload: &session.UserPayload{
			Parts: []session.EventPart{{Kind: session.PartKindText, Text: "a"}},
		},
	})
	svc.AppendEvent(ctx, sess.Ref, session.Event{
		Kind: session.EventKindAssistant, Visibility: session.VisibilityCanonical,
		AssistantPayload: &session.AssistantPayload{
			Parts: []session.EventPart{{Kind: session.PartKindText, Text: "b"}},
		},
	})
	svc.AppendEvent(ctx, sess.Ref, session.Event{
		Kind: session.EventKindUser, Visibility: session.VisibilityCanonical,
		UserPayload: &session.UserPayload{
			Parts: []session.EventPart{{Kind: session.PartKindText, Text: "c"}},
		},
	})

	evts, err := svc.Events(ctx, session.EventsRequest{SessionRef: sess.Ref})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evts) != 3 {
		t.Fatalf("got %d, want 3", len(evts))
	}
	if evts[0].TextContent() != "a" || evts[2].TextContent() != "c" {
		t.Errorf("event content mismatch")
	}

	// Filter by kind.
	userEvts, _ := svc.Events(ctx, session.EventsRequest{
		SessionRef: sess.Ref, Kinds: []session.EventKind{session.EventKindUser},
	})
	if len(userEvts) != 2 {
		t.Errorf("got %d user events, want 2", len(userEvts))
	}

	// With limit.
	limited, _ := svc.Events(ctx, session.EventsRequest{SessionRef: sess.Ref, Limit: 1})
	if len(limited) != 1 {
		t.Errorf("got %d, want 1", len(limited))
	}
}

func TestUpdateState(t *testing.T) {
	svc := tempService(t)
	ctx := context.Background()
	sess, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "app", UserID: "u", WorkspaceKey: "ws",
		State: session.State{"k1": "v1"},
	})
	err := svc.UpdateState(ctx, sess.Ref, func(s session.State) (session.State, error) {
		s["k2"] = "v2"
		return s, nil
	})
	if err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	got, _ := svc.Get(ctx, sess.Ref)
	if got.State["k2"] != "v2" {
		t.Errorf("got %q, want %q", got.State["k2"], "v2")
	}
}

func TestStructuredStateRoundTrip(t *testing.T) {
	svc := tempService(t)
	ctx := context.Background()
	sess, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "app", UserID: "u", WorkspaceKey: "ws",
	})

	want := map[string]any{
		"model": map[string]any{
			"id":      "m1",
			"enabled": true,
		},
		"count": float64(2),
	}
	if err := svc.ReplaceState(ctx, sess.Ref, want); err != nil {
		t.Fatalf("ReplaceState: %v", err)
	}

	got, err := svc.SnapshotState(ctx, sess.Ref)
	if err != nil {
		t.Fatalf("SnapshotState: %v", err)
	}
	if got["count"] != float64(2) {
		t.Fatalf("count = %#v, want 2", got["count"])
	}
	modelState, ok := got["model"].(map[string]any)
	if !ok {
		t.Fatalf("model state = %#v, want object", got["model"])
	}
	if modelState["id"] != "m1" || modelState["enabled"] != true {
		t.Fatalf("model state = %#v", modelState)
	}

	got["count"] = float64(99)
	again, err := svc.SnapshotState(ctx, sess.Ref)
	if err != nil {
		t.Fatalf("SnapshotState again: %v", err)
	}
	if again["count"] != float64(2) {
		t.Fatalf("snapshot was not isolated: count = %#v", again["count"])
	}
}

func TestStructuredStateSurvivesRestart(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "session-file-test", "structured-"+t.Name())
	defer os.RemoveAll(dir)
	ctx := context.Background()

	svc1, _ := New(Config{RootDir: dir})
	sess, _ := svc1.Create(ctx, session.CreateRequest{
		AppName: "app", UserID: "u", WorkspaceKey: "ws",
	})
	if err := svc1.ReplaceState(ctx, sess.Ref, map[string]any{"k": "v"}); err != nil {
		t.Fatalf("ReplaceState: %v", err)
	}

	svc2, _ := New(Config{RootDir: dir})
	got, err := svc2.SnapshotState(ctx, sess.Ref)
	if err != nil {
		t.Fatalf("SnapshotState after restart: %v", err)
	}
	if got["k"] != "v" {
		t.Fatalf("state = %#v, want k=v", got)
	}
}

// ─── Replay golden test ──────────────────────────────────────────────

// TestReplayGoldenRoundTrip is the critical test: write events to durable
// storage, read them back, rebuild model context, and verify it matches
// what the runtime would produce.
func TestReplayGoldenRoundTrip(t *testing.T) {
	svc := tempService(t)
	ctx := context.Background()

	sess, _ := svc.Create(ctx, session.CreateRequest{
		AppName: "app", UserID: "u", WorkspaceKey: "ws",
	})

	// Write a realistic multi-turn conversation with tool calls.
	events := []session.Event{
		// Turn 1: user asks a question.
		{
			Kind: session.EventKindUser, Visibility: session.VisibilityCanonical,
			UserPayload: &session.UserPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: "What files are in /tmp?"}},
			},
		},
		// Assistant responds with text + tool call.
		{
			Kind: session.EventKindAssistant, Visibility: session.VisibilityCanonical,
			AssistantPayload: &session.AssistantPayload{
				Parts: []session.EventPart{
					{Kind: session.PartKindText, Text: "Let me check."},
				},
			},
		},
		// Tool call event.
		{
			Kind: session.EventKindToolCall, Visibility: session.VisibilityCanonical,
			ToolCallPayload: &session.ToolCallPayload{
				CallID: "tc-1", Name: "LIST", Status: "pending",
				Args: map[string]any{"path": "/tmp"},
			},
		},
		// Tool result event.
		{
			Kind: session.EventKindToolResult, Visibility: session.VisibilityCanonical,
			ToolResultPayload: &session.ToolResultPayload{
				CallID: "tc-1", Name: "LIST", Status: "completed",
				Content: []session.EventPart{
					{Kind: session.PartKindText, Text: "file1.txt\nfile2.txt"},
				},
			},
		},
		// Assistant final response.
		{
			Kind: session.EventKindAssistant, Visibility: session.VisibilityCanonical,
			AssistantPayload: &session.AssistantPayload{
				Parts: []session.EventPart{
					{Kind: session.PartKindText, Text: "Found 2 files."},
				},
			},
		},
		// UI-only event — should NOT appear in model context.
		{
			Kind: session.EventKindNotice, Visibility: session.VisibilityUIOnly,
			NoticePayload: &session.NoticePayload{Level: "info", Text: "thinking..."},
		},
		// Mirror event — persisted but NOT in model context.
		{
			Kind: session.EventKindAssistant, Visibility: session.VisibilityMirror,
			AssistantPayload: &session.AssistantPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: "mirror"}},
			},
		},
	}

	// Write durable events to storage (skip transient events).
	for _, e := range events {
		if e.Visibility.IsTransient() {
			continue // transient events are not persisted
		}
		_, err := svc.AppendEvent(ctx, sess.Ref, e)
		if err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	// Read back from storage.
	durableEvents, err := svc.Events(ctx, session.EventsRequest{SessionRef: sess.Ref})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	// Build model context from durable events.
	modelMsgs := session.ModelContextFromEvents(durableEvents)

	// Expected: user → assistant(text) → assistant(tool_use) → tool → assistant(text)
	// Mirror and ui_only events are excluded.
	if len(modelMsgs) != 5 {
		t.Fatalf("got %d model messages, want 5", len(modelMsgs))
	}

	// Verify each message.
	if modelMsgs[0].Role != model.RoleUser {
		t.Errorf("msg 0: got role %q, want %q", modelMsgs[0].Role, model.RoleUser)
	}
	if modelMsgs[0].Content[0].Text != "What files are in /tmp?" {
		t.Errorf("msg 0 text: got %q", modelMsgs[0].Content[0].Text)
	}

	if modelMsgs[1].Role != model.RoleAssistant {
		t.Errorf("msg 1: got role %q, want %q", modelMsgs[1].Role, model.RoleAssistant)
	}
	if modelMsgs[1].Content[0].Text != "Let me check." {
		t.Errorf("msg 1 text: got %q", modelMsgs[1].Content[0].Text)
	}

	if modelMsgs[2].Role != model.RoleAssistant {
		t.Errorf("msg 2: got role %q, want %q", modelMsgs[2].Role, model.RoleAssistant)
	}
	if modelMsgs[2].Content[0].ToolUse == nil {
		t.Fatal("msg 2: expected tool_use part")
	}
	if modelMsgs[2].Content[0].ToolUse.CallID != "tc-1" {
		t.Errorf("msg 2 tool_use call_id: got %q, want %q", modelMsgs[2].Content[0].ToolUse.CallID, "tc-1")
	}

	if modelMsgs[3].Role != model.RoleTool {
		t.Errorf("msg 3: got role %q, want %q", modelMsgs[3].Role, model.RoleTool)
	}
	if modelMsgs[3].Content[0].ToolResult == nil {
		t.Fatal("msg 3: expected tool_result part")
	}
	if modelMsgs[3].Content[0].ToolResult.Content != "file1.txt\nfile2.txt" {
		t.Errorf("msg 3 content: got %q", modelMsgs[3].Content[0].ToolResult.Content)
	}

	if modelMsgs[4].Role != model.RoleAssistant {
		t.Errorf("msg 4: got role %q, want %q", modelMsgs[4].Role, model.RoleAssistant)
	}
	if modelMsgs[4].Content[0].Text != "Found 2 files." {
		t.Errorf("msg 4 text: got %q", modelMsgs[4].Content[0].Text)
	}

}

// TestPersistenceVerification verifies that events written to disk
// survive a service restart (new Service instance, same root).
func TestPersistenceVerification(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "session-file-test", "persist-"+t.Name())
	defer os.RemoveAll(dir)

	ctx := context.Background()

	// Write with first service instance.
	svc1, _ := New(Config{RootDir: dir})
	sess, _ := svc1.Create(ctx, session.CreateRequest{
		AppName: "app", UserID: "u", WorkspaceKey: "ws",
	})
	svc1.AppendEvent(ctx, sess.Ref, session.Event{
		Kind: session.EventKindUser, Visibility: session.VisibilityCanonical,
		UserPayload: &session.UserPayload{
			Parts: []session.EventPart{{Kind: session.PartKindText, Text: "persisted"}},
		},
	})

	// Read with second service instance (simulates restart).
	svc2, _ := New(Config{RootDir: dir})
	got, err := svc2.Get(ctx, sess.Ref)
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if got.Title != "" {
		t.Errorf("title: got %q", got.Title)
	}

	evts, err := svc2.Events(ctx, session.EventsRequest{SessionRef: sess.Ref})
	if err != nil {
		t.Fatalf("Events after restart: %v", err)
	}
	if len(evts) != 1 {
		t.Fatalf("got %d events, want 1", len(evts))
	}
	if evts[0].TextContent() != "persisted" {
		t.Errorf("event text: got %q, want %q", evts[0].TextContent(), "persisted")
	}
}

func TestAppendEventWaitsForStoreLockAcrossInstances(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "session-file-test", "lock-"+t.Name())
	defer os.RemoveAll(dir)
	ctx := context.Background()

	svc1, err := New(Config{RootDir: dir})
	if err != nil {
		t.Fatalf("New svc1: %v", err)
	}
	sess, err := svc1.Create(ctx, session.CreateRequest{AppName: "app", UserID: "u", WorkspaceKey: "ws"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	held, err := lockSessionStoreRoot(dir, storeRootLockExclusive)
	if err != nil {
		t.Fatalf("lockSessionStoreRoot: %v", err)
	}

	svc2, err := New(Config{RootDir: dir})
	if err != nil {
		t.Fatalf("New svc2: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := svc2.AppendEvent(ctx, sess.Ref, session.Event{
			Kind:       session.EventKindUser,
			Visibility: session.VisibilityCanonical,
			UserPayload: &session.UserPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: "blocked until lock release"}},
			},
		})
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("AppendEvent completed while root lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := unlockSessionStoreRoot(held); err != nil {
		t.Fatalf("unlockSessionStoreRoot: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("AppendEvent after unlock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AppendEvent did not complete after root lock release")
	}
}

func TestConcurrentAppendAcrossInstances(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "session-file-test", "concurrent-"+t.Name())
	defer os.RemoveAll(dir)
	ctx := context.Background()

	svc1, err := New(Config{RootDir: dir})
	if err != nil {
		t.Fatalf("New svc1: %v", err)
	}
	sess, err := svc1.Create(ctx, session.CreateRequest{AppName: "app", UserID: "u", WorkspaceKey: "ws"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	svc2, err := New(Config{RootDir: dir})
	if err != nil {
		t.Fatalf("New svc2: %v", err)
	}

	const n = 25
	var wg sync.WaitGroup
	errs := make(chan error, n*2)
	appendFrom := func(svc *Service, prefix string) {
		defer wg.Done()
		for i := 0; i < n; i++ {
			_, err := svc.AppendEvent(ctx, sess.Ref, session.Event{
				Kind:       session.EventKindUser,
				Visibility: session.VisibilityCanonical,
				UserPayload: &session.UserPayload{
					Parts: []session.EventPart{{Kind: session.PartKindText, Text: prefix}},
				},
			})
			errs <- err
		}
	}
	wg.Add(2)
	go appendFrom(svc1, "a")
	go appendFrom(svc2, "b")
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	evts, err := svc1.Events(ctx, session.EventsRequest{SessionRef: sess.Ref})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evts) != n*2 {
		t.Fatalf("got %d events, want %d", len(evts), n*2)
	}
	seen := make(map[string]bool, len(evts))
	for _, evt := range evts {
		if evt.ID == "" {
			t.Fatal("persisted event has empty id")
		}
		if seen[evt.ID] {
			t.Fatalf("duplicate event id %q", evt.ID)
		}
		seen[evt.ID] = true
	}
}

func TestEventsReturnsErrorForCorruptJSONL(t *testing.T) {
	svc := tempService(t)
	ctx := context.Background()
	sess, err := svc.Create(ctx, session.CreateRequest{AppName: "app", UserID: "u", WorkspaceKey: "ws"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.WriteFile(svc.eventsPath(sess.Ref), []byte("{not-json}\n"), 0o644); err != nil {
		t.Fatalf("write corrupt events log: %v", err)
	}
	_, err = svc.Events(ctx, session.EventsRequest{SessionRef: sess.Ref})
	if err == nil {
		t.Fatal("Events succeeded for corrupt JSONL")
	}
	if !strings.Contains(err.Error(), "corrupt JSONL") {
		t.Fatalf("Events error = %v, want corrupt JSONL", err)
	}
}
