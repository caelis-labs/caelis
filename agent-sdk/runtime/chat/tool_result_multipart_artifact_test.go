package chat

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/artifact"
)

func TestCanonicalMultipartResultRetainsCompleteArtifactAndMedia(t *testing.T) {
	t.Parallel()
	media := tool.Image{Data: []byte("complete-inline-image-data"), MIMEType: "image/png", Name: "evidence"}
	large := strings.Repeat(`{"method":"find","path":"C:\\验收\\item","description":"<value>"}`+"\n", 1200)
	link := tool.Resource{URI: "resource://fixture/item", Name: "item", Title: "Item", MIMEType: "text/plain", Description: "fixture resource", Size: new(int64(0))}
	emptyDocument := link
	emptyDocument.Text = new(string)
	document := link
	document.Text = &large
	for name, parts := range map[string][]tool.ContentBlock{
		"text-and-structured":   {tool.Text(large), tool.StructuredContent{Value: mustJSON(map[string]any{"schema": large})}},
		"text-and-media":        {tool.Text(large), media},
		"text-media-structured": {tool.Text(large), media, tool.StructuredContent{Value: mustJSON(map[string]any{"schema": large})}},
		"link-and-embedded":     {tool.Text(large), link, emptyDocument},
		"embedded-only":         {document},
		"embedded-then-text":    {document, tool.Text("end of result")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := artifact.NewFileStore(t.TempDir())
			result := tool.Result{Content: parts}
			before, _ := tool.CloneResult(result, nil)
			canonical, meta := (tool.ResultProjection{Artifacts: store}).Project(t.Context(), result)
			var path string
			if _, ok := canonical.Structured(); ok {
				path = artifactPathFromJSONMetadata(t, firstToolResultJSONMap(t, canonical))
			} else {
				for _, block := range canonical.Content {
					if text, ok := block.(tool.Text); ok && strings.Contains(string(text), "Caelis runtime artifact: ") {
						path = artifactPathFromHint(t, string(text))
					}
				}
			}
			if path == "" {
				t.Fatal("multipart truncation lost its complete raw artifact reference")
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var recovered tool.Result
			if err := json.Unmarshal(raw, &recovered); err != nil || !reflect.DeepEqual(recovered.Content, before.Content) {
				t.Fatalf("artifact lost typed blocks: recovered %d of %d, decode error: %v", len(recovered.Content), len(before.Content), err)
			}
			if !reflect.DeepEqual(before, result) {
				t.Fatal("canonicalization changed the original result")
			}
			var gotMedia []tool.Image
			for _, block := range canonical.Content {
				if image, ok := block.(tool.Image); ok {
					gotMedia = append(gotMedia, image)
				}
			}
			if strings.Contains(name, "media") && !reflect.DeepEqual(gotMedia, []tool.Image{media}) {
				t.Fatal("canonicalization changed the model-visible image")
			}
			if again, info := tool.TruncateResultWithInfo(canonical, tool.DefaultTruncationPolicy()); info.Truncated || !reflect.DeepEqual(again, canonical) {
				t.Fatal("canonical multipart result still needs truncation")
			}
			call := model.ToolCall{ID: "multipart", Name: "fixture", Args: `{}`}
			message := toolResultMessageFromCanonical(call, canonical, nil)
			event := session.CanonicalizeEvent(toolResultEvent(call, canonical, nil, &message, meta))
			if err := session.ValidateDurableCoreEvent(event); err != nil {
				t.Fatal(err)
			}
			sessions := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
			active, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
				AppName: "caelis", UserID: "user", Workspace: session.WorkspaceRef{Key: "ws", CWD: t.TempDir()},
			})
			if err != nil {
				t.Fatal(err)
			}
			assistant := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, "")
			for _, entry := range append(modelToolCallEvents(assistant, &model.Response{Message: assistant}, "", nil), event) {
				if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: entry}); err != nil {
					t.Fatal(err)
				}
			}
			loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			replayed := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: context.Background(), Session: active, Events: loaded.Events}))
			if !reflect.DeepEqual(replayed, []model.Message{assistant, message}) {
				t.Fatal("file-store reload changed multipart content or its artifact reference")
			}
			assertFileContent(t, path, raw)
		})
	}
}

func TestCanonicalMultipartResultArtifactFailureStaysBounded(t *testing.T) {
	t.Parallel()
	store := artifact.NewFileStore("")
	result := tool.Result{Content: []tool.ContentBlock{
		tool.Text(strings.Repeat("large content", 10000)),
		tool.StructuredContent{Value: mustJSON(map[string]any{"status": "complete"})},
	}}
	canonical, meta := (tool.ResultProjection{Artifacts: store}).Project(t.Context(), result)
	if tool.ResultNeedsTruncation(canonical, tool.DefaultTruncationPolicy()) {
		t.Fatal("artifact failure bypassed result budget")
	}
	for _, part := range tool.ModelParts(canonical.Content) {
		if part.Text != nil && strings.Contains(part.Text.Text, "Caelis runtime artifact: ") {
			t.Fatal("reference to an unwritten artifact")
		}
		if part.JSON != nil && strings.Contains(string(part.JSON.Value), `"_caelis"`) {
			t.Fatal("reference to an unwritten artifact")
		}
	}
	call := model.ToolCall{ID: "quota", Name: "fixture", Args: `{}`}
	message := toolResultMessageFromCanonical(call, canonical, nil)
	if err := session.ValidateDurableCoreEvent(session.CanonicalizeEvent(toolResultEvent(call, canonical, nil, &message, meta))); err != nil {
		t.Fatal(err)
	}
}
