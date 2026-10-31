package chat

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestCanonicalMultipartResultRetainsCompleteArtifactAndMedia(t *testing.T) {
	t.Parallel()
	media := model.NewMediaPart(model.MediaModalityImage, model.MediaSource{
		Kind: model.MediaSourceInline, Data: "complete-inline-image-data",
	}, "image/png", "evidence")
	large := strings.Repeat(`{"method":"find","path":"C:\\验收\\item","description":"<value>"}`+"\n", 1200)
	for name, parts := range map[string][]model.Part{
		"text-and-structured":   {model.NewTextPart(large), model.NewJSONPart(mustJSON(map[string]any{"schema": large}))},
		"text-and-media":        {model.NewTextPart(large), media},
		"text-media-structured": {model.NewTextPart(large), media, model.NewJSONPart(mustJSON(map[string]any{"schema": large}))},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := testToolResultArtifactStore(t, 8, 4*1024*1024)
			result := tool.Result{ID: "multipart", Name: "fixture", Content: parts}
			before := model.CloneParts(result.Content)
			canonical, meta := canonicalToolResult(result, store)
			var path string
			for _, part := range canonical.Content {
				if part.JSON != nil {
					payload := firstToolResultJSONMap(t, tool.Result{Content: []model.Part{part}})
					path = artifactPathFromJSONMetadata(t, payload)
					break
				}
				if part.Text != nil && strings.Contains(part.Text.Text, toolResultArtifactHintPrefix) {
					path = artifactPathFromHint(t, part.Text.Text)
				}
			}
			if path == "" {
				t.Fatal("multipart truncation lost its complete raw artifact reference")
			}
			raw, err := json.Marshal(before)
			if err != nil {
				t.Fatal(err)
			}
			assertFileContent(t, path, raw)
			assertPrivateFileMode(t, path)
			if !reflect.DeepEqual(before, result.Content) {
				t.Fatal("canonicalization changed the original result")
			}
			var gotMedia []model.Part
			for _, part := range canonical.Content {
				if part.Media != nil {
					gotMedia = append(gotMedia, part)
				}
			}
			if strings.Contains(name, "media") && !reflect.DeepEqual(gotMedia, []model.Part{media}) {
				t.Fatal("canonicalization changed the model-visible image")
			}
			if again, info := tool.TruncateResultWithInfo(canonical, tool.DefaultTruncationPolicy()); info.Truncated || !reflect.DeepEqual(again, canonical) {
				t.Fatal("canonical multipart result still needs truncation")
			}
			call := model.ToolCall{ID: result.ID, Name: result.Name, Args: `{}`}
			message := toolResultMessageFromCanonical(call, canonical)
			event := session.CanonicalizeEvent(toolResultEvent(call, canonical, &message, meta))
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

func TestCanonicalMultipartResultArtifactQuotaFailureStaysBounded(t *testing.T) {
	t.Parallel()
	store := testToolResultArtifactStore(t, 1, 32)
	result := tool.Result{Content: []model.Part{
		model.NewTextPart(strings.Repeat("large content", 10000)),
		model.NewJSONPart(mustJSON(map[string]any{"status": "complete"})),
	}}
	canonical, meta := canonicalToolResult(result, store)
	if tool.ResultNeedsTruncation(canonical, tool.DefaultTruncationPolicy()) {
		t.Fatal("artifact failure bypassed result budget")
	}
	for _, part := range canonical.Content {
		if part.Text != nil && strings.Contains(part.Text.Text, toolResultArtifactHintPrefix) {
			t.Fatal("reference to an unwritten artifact")
		}
		if part.JSON != nil && strings.Contains(string(part.JSON.Value), `"_caelis"`) {
			t.Fatal("reference to an unwritten artifact")
		}
	}
	call := model.ToolCall{ID: "quota", Name: "fixture", Args: `{}`}
	message := toolResultMessageFromCanonical(call, canonical)
	if err := session.ValidateDurableCoreEvent(session.CanonicalizeEvent(toolResultEvent(call, canonical, &message, meta))); err != nil {
		t.Fatal(err)
	}
}
