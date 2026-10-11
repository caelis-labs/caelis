package subagent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/internal/acpdraft"
	"github.com/caelis-labs/caelis/internal/acptest/draftpeer"
)

func TestDraftChildStructuredFileDiffSurvivesStoreAndReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const patch = "diff --git a/old b/new\r\n--- a/old\r\n+++ b/new\r\n@@ -1 +1 @@\r\n-before\r\n+after\r\n"
	diff := map[string]any{
		"type": "diff", "_meta": map[string]any{"display": map[string]any{"label": "original"}},
		"changes": []any{
			map[string]any{"operation": "move", "path": "/tmp/new file ", "oldPath": "/tmp/old file ", "fileType": "text", "mimeType": "text/plain", "_meta": map[string]any{"source": "peer"}},
			map[string]any{"operation": "modify", "path": "/tmp/second.bin", "fileType": "binary", "mimeType": nil},
		},
		"patch": map[string]any{"format": "git_patch", "text": patch},
	}
	rawDiff, err := json.Marshal(diff)
	if err != nil {
		t.Fatal(err)
	}
	var want session.ProtocolToolCallContent
	if err := json.Unmarshal(rawDiff, &want); err != nil {
		t.Fatal(err)
	}
	peer := &draftpeer.Peer{SessionID: "child-remote"}
	peer.OnPrompt = func(ctx context.Context, req v2.PromptRequest, activity, message string) (v2.PromptResponse, error) {
		if err := peer.State(ctx, req.SessionId, activity, "running", ""); err != nil {
			return v2.PromptResponse{}, err
		}
		if err := peer.Echo(ctx, req.SessionId, message, "input"); err != nil {
			return v2.PromptResponse{}, err
		}
		if err := peer.Update(ctx, req.SessionId, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "edit", "status": "completed", "content": []any{diff}}); err != nil {
			return v2.PromptResponse{}, err
		}
		if err := peer.State(ctx, req.SessionId, activity, "idle", "end_turn"); err != nil {
			return v2.PromptResponse{}, err
		}
		return v2.PromptResponse{MessageId: v2.MessageId(message), Meta: acpdraft.ActivityMeta(activity)}, nil
	}
	runner := replacementDraftRunner(t, peer)
	events := make(chan childInputTestEvent, 64)
	spawn := childInputSpawnContext(t, "diff-task", events)
	if _, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "input"}); err != nil {
		t.Fatal(err)
	}
	var observed *session.Event
	for observed == nil {
		select {
		case frame := <-events:
			if frame.Frame == nil || frame.Frame.Event == nil {
				continue
			}
			update := session.ProtocolUpdateOf(frame.Frame.Event)
			if update != nil && update.ToolCallID == "edit" {
				observed = frame.Frame.Event
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	assertChildDiffFacts(t, observed, want)
	raw, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	var restored session.Event
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	assertChildDiffFacts(t, &restored, want)
	// Live child tool observations are transient. Retaining their normalized
	// protocol facts for client replay uses the mirror lane, never parent model
	// truth or a fabricated native tool execution result.
	restored.Visibility = session.VisibilityMirror
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: &restored}); err != nil {
		t.Fatal(err)
	}
	fresh := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	loaded, err := fresh.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil || len(loaded.Events) != 1 {
		t.Fatalf("physical reload = %#v, %v", loaded, err)
	}
	assertChildDiffFacts(t, loaded.Events[0], want)
	for _, event := range []*session.Event{observed, loaded.Events[0]} {
		updates, err := projection.ProjectEvent(event)
		if err != nil || len(updates) != 1 {
			t.Fatalf("diff projection = %#v, %v", updates, err)
		}
		raw, err := wirev1.MarshalEnvelope(eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: active.SessionID, Update: updates[0]})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := wirev1.UnmarshalEnvelope(raw)
		if err != nil {
			t.Fatal(err)
		}
		content := decoded.Update.(eventstream.ToolCallUpdate).Content
		if len(content) != 1 || !reflect.DeepEqual(content[0].Changes, want.Changes) || !reflect.DeepEqual(content[0].Patch, want.Patch) || !reflect.DeepEqual(content[0].Meta, want.Meta) {
			t.Fatalf("live/replayed public diff lost facts: %s", raw)
		}
	}
}

func assertChildDiffFacts(t *testing.T, event *session.Event, want session.ProtocolToolCallContent) {
	t.Helper()
	content := session.ProtocolToolCallContentOf(session.ProtocolUpdateOf(event))
	if len(content) != 1 || !reflect.DeepEqual(content[0], want) {
		t.Fatalf("canonical child diff = %#v, want %#v", content, want)
	}
	if _, visible := session.ModelMessageOf(event); visible || session.IsMainInvocationVisibleEvent(event) {
		t.Fatalf("remote display diff gained parent model-context authority: %#v", event)
	}
}
