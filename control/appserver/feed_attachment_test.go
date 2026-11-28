package appserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestFeedAttachmentExactAndCanonicalReplay(t *testing.T) {
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root, Attachments: true})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user", PreferredSessionID: "session-1", Workspace: session.WorkspaceRef{CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	broker, _ := newTestFeedBroker(t, store, FeedBrokerConfig{})
	_, cursor := broker.Boundary()
	exact, err := broker.Subscribe(t.Context(), SubscribeRequest{SessionID: active.SessionID, Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	defer exact.Subscription.Close()
	assertFeedDelivery(t, exact.Subscription.Deliveries(), FeedDeliverySync)

	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	data := append(picture.Bytes(), make([]byte, 32<<10)...)
	result := tool.Result{Content: []tool.ContentBlock{tool.Image{Data: data, MIMEType: "image/png"}}}
	message := model.NewMessage(model.RoleTool, model.Part{Kind: model.PartKindToolResult, ToolResult: &model.ToolResultPart{
		ToolUseID: "direct-picture", Name: "Picture", Content: tool.ModelParts(result.Content),
	}})
	_, err = store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{
		ID: "picture-receipt", Type: session.EventTypeToolResult, Message: &message,
		Tool: &session.EventTool{ID: "direct-picture", Name: "Picture", Status: "completed", Outcome: session.ToolOutcomeSucceeded, Result: &result},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.Prime(t.Context()); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	assertImage := func(value any, want string) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil || !bytes.Contains(raw, []byte(want)) || bytes.Contains(raw, []byte("caelis_session_image")) {
			t.Fatalf("image delivery bytes=%d contains expected=%v err=%v", len(raw), bytes.Contains(raw, []byte(want)), err)
		}
	}
	delivery := assertFeedDelivery(t, exact.Subscription.Deliveries(), FeedDeliveryAppendPage)
	assertImage(delivery.Events, encoded)
	if err := broker.Seal(t.Context()); err != nil {
		t.Fatal(err)
	}

	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root, Attachments: true})
	fresh := subscribeSealedCanonicalReplacementReader(t, reopened)
	_, _, _, events := collectCanonicalReplacement(t, fresh.Subscription)
	assertImage(events, encoded)
	if _, err := reopened.PruneAttachments(t.Context(), active.SessionID, time.Now().Add(time.Hour), true); err != nil {
		t.Fatal(err)
	}
	pruned := subscribeSealedCanonicalReplacementReader(t, reopened)
	_, _, _, events = collectCanonicalReplacement(t, pruned.Subscription)
	assertImage(events, "[Image attachment unavailable]")
}
