package chat

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/artifact"
)

func TestCanonicalToolResultUsesSerializedOutputBudget(t *testing.T) {
	t.Parallel()
	cases := map[string][]tool.ContentBlock{
		"json-text": {tool.Text(strings.Repeat(`{"method":"find","value":"C:\\workspace\\item"}`, 1500))},
		"text-and-structured": {
			tool.Text(strings.Repeat(`{"method":"find","selector":"label"}`, 1500)),
			tool.StructuredContent{Value: mustJSON(map[string]any{"schema": strings.Repeat("details", 8000)})},
		},
		"escaping": {tool.Text(strings.Repeat("\"\\\t\n\r<>&\u2028", 7000))},
		"multiple-text": {
			tool.Text(strings.Repeat("first\t", 5000)),
			tool.Text(strings.Repeat("second\t", 5000)),
		},
		"scalar-json-envelope": {tool.StructuredContent{Value: []byte(strconv.Quote(strings.Repeat("s", 39998)))}},
		"json-html-escaping":   {tool.StructuredContent{Value: []byte(`{"html":"` + strings.Repeat("<>&", 5000) + `"}`)}},
		"array-html-escaping":  {tool.StructuredContent{Value: []byte(`["` + strings.Repeat("<>&", 5000) + `"]`)}},
		"omission-notice": {
			tool.Text(strings.Repeat("a", 39999)),
			tool.Text("additional output"),
		},
	}
	for name, parts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := tool.Result{Content: parts}
			call := model.ToolCall{ID: "budget", Name: "fixture", Args: `{}`}
			canonical, meta := (tool.ResultProjection{}).Project(t.Context(), result)
			if tool.ResultNeedsTruncation(canonical, tool.DefaultTruncationPolicy()) {
				t.Fatal("canonical result still exceeds the shared content budget")
			}
			if again, info := tool.TruncateResultWithInfo(canonical, tool.DefaultTruncationPolicy()); info.Truncated || !reflect.DeepEqual(again, canonical) {
				t.Fatal("canonical result changed on a second truncation pass")
			}
			message := toolbinding.ResultMessage(call, canonical, nil)
			event := session.CanonicalizeEvent(toolResultEvent(call, canonical, nil, &message, meta))
			if err := session.ValidateDurableCoreEvent(event); err != nil {
				t.Fatal(err)
			}
			if nestedMap(meta, "caelis", "runtime", "tool", "truncation")["truncated"] != true {
				t.Fatal("missing canonical truncation provenance")
			}

			store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
			active, err := store.StartSession(context.Background(), session.StartSessionRequest{
				AppName: "caelis", UserID: "user", Workspace: session.WorkspaceRef{Key: "ws", CWD: t.TempDir()},
			})
			if err != nil {
				t.Fatal(err)
			}
			assistant := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, "")
			for _, entry := range append(modelToolCallEvents(assistant, &model.Response{Message: assistant}, "", nil), event) {
				if _, err := store.AppendEvent(context.Background(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: entry}); err != nil {
					t.Fatalf("AppendEvent() error = %v", err)
				}
			}
			loaded, err := store.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			replayed := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: context.Background(), Session: active, Events: loaded.Events}))
			if !reflect.DeepEqual(replayed, []model.Message{assistant, message}) {
				t.Fatal("file-store replay changed the canonical model messages")
			}
		})
	}
}

func TestCanonicalToolResultPreservesArtifactWhenOnlySerializedOutputExceedsBudget(t *testing.T) {
	t.Parallel()
	raw := strings.Repeat("<>&\"", 5000)
	if len(raw) >= tool.DefaultTruncationPolicy().ByteBudget() {
		t.Fatal("fixture must fit before JSON serialization")
	}
	store := artifact.NewFileStore(t.TempDir())
	canonical, meta := (tool.ResultProjection{Artifacts: store}).Project(t.Context(), textToolResult(raw))
	var path string
	for _, block := range canonical.Content {
		if text, ok := block.(tool.Text); ok && strings.Contains(string(text), "Caelis runtime artifact: ") {
			path = artifactPathFromHint(t, string(text))
		}
	}
	if path == "" {
		t.Fatal("serialization-only truncation lost the complete raw artifact")
	}
	assertFileContent(t, path, []byte(raw))
	call := model.ToolCall{ID: "artifact-budget", Name: "fixture", Args: `{}`}
	message := toolbinding.ResultMessage(call, canonical, nil)
	event := toolResultEvent(call, canonical, nil, &message, meta)
	if err := session.ValidateDurableCoreEvent(session.CanonicalizeEvent(event)); err != nil {
		t.Fatal(err)
	}
}
