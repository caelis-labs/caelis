package chat

import (
	"bytes"
	"context"
	"encoding/json"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/artifact"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestCanonicalTerminalToolResultWritesJSONArtifactWithoutChangingToolHint(t *testing.T) {
	t.Parallel()

	store := artifact.NewFileStore(t.TempDir())
	large := strings.Repeat("evidence line\n", tool.DefaultTruncationPolicy().ByteBudget()/8)
	raw := mustJSON(map[string]any{
		"result":      large,
		"system_hint": "Keep the original diagnostic hint.",
	})
	result := tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}}}
	canonical, truncationMeta := (tool.ResultProjection{Artifacts: store}).Project(t.Context(), result)
	if nestedMap(truncationMeta, "caelis", "runtime", "tool", "truncation")["truncated"] != true {
		t.Fatalf("truncation meta = %#v, want truncated", truncationMeta)
	}
	payload := firstToolResultJSONMap(t, canonical)
	hint, _ := payload["system_hint"].(string)
	if hint != "Keep the original diagnostic hint." {
		t.Fatalf("system_hint = %q, want the tool-owned value unchanged", hint)
	}
	path := artifactPathFromJSONMetadata(t, payload)
	assertFileContent(t, path, raw)
	if _, info := tool.TruncateResultWithInfo(canonical, tool.DefaultTruncationPolicy()); info.Truncated {
		t.Fatalf("canonical result still exceeds policy: %#v", info)
	}
	call := model.ToolCall{ID: "call-json", Name: "ECHO", Args: `{}`}
	message := toolbinding.ResultMessage(call, canonical, nil)
	response := message.ToolResponse()
	if response == nil {
		t.Fatal("ToolResponse() = nil, want provider-facing artifact metadata")
		return
	}
	if got, _ := response.Result["system_hint"].(string); got != "Keep the original diagnostic hint." {
		t.Fatalf("provider-facing system_hint = %q, want tool-owned value unchanged", got)
	}
	if got := artifactPathFromJSONMetadata(t, response.Result); got != path {
		t.Fatalf("provider-facing artifact path = %q, want %q", got, path)
	}
	event := toolResultEvent(call, canonical, nil, &message, truncationMeta)
	if err := session.ValidateDurableCoreEvent(session.CanonicalizeEvent(event)); err != nil {
		t.Fatalf("ValidateDurableCoreEvent() error = %v", err)
	}
}

// The same large tool reply must produce a replayable view with either factory
// path, including when the host has no filesystem-backed result store.
func TestResultProjectionSurvivesModelRefreshAndReplay(t *testing.T) {
	temp := t.TempDir()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, temp)
	}
	raw := strings.Repeat("large reply\n", 16000)
	for _, tt := range []struct {
		name    string
		store   bool
		resolve bool
	}{
		{name: "no artifact store"},
		{name: "static tools", store: true},
		{name: "resolved tools", store: true, resolve: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			m := &toolLoopModel{}
			result := textToolResult(raw)
			tools := []tool.Tool{tool.NamedTool{
				Def:    tool.Definition{Name: "ECHO", InputSchema: map[string]any{"type": "object"}},
				Invoke: func(context.Context, tool.Call) (tool.Result, error) { return result, nil },
			}}
			spec := agent.AgentSpec{Model: m, Tools: tools}
			if tt.resolve {
				spec = agent.AgentSpec{ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
					return agent.ModelRequestSnapshot{Model: m, Tools: tools}, nil
				}}
			}
			factory := Factory{}
			store := &recordingArtifactStore{}
			if tt.store {
				ctx = hookrun.WithRun(ctx, hook.Set{ToolProjection: tool.ResultProjection{Artifacts: store, Budget: tool.TruncationPolicy{MaxTokens: 512}}}, hook.TurnContext{})
			}
			runner, err := factory.NewAgent(ctx, spec)
			if err != nil {
				t.Fatal(err)
			}

			config := sessionfile.Config{RootDir: t.TempDir()}
			sessions := sessionfile.NewStore(config)
			active, err := sessions.StartSession(ctx, session.StartSessionRequest{
				AppName: "test", UserID: "user", Workspace: session.WorkspaceRef{Key: "ws", CWD: t.TempDir()},
			})
			if err != nil {
				t.Fatal(err)
			}
			user := &session.Event{Type: session.EventTypeUser, Message: ptrMessage(model.NewTextMessage(model.RoleUser, "echo"))}
			appendEvent := func(event *session.Event) {
				t.Helper()
				if _, err := sessions.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
					t.Fatal(err)
				}
			}
			appendEvent(user)
			for event, err := range runner.Run(agent.NewContext(agent.ContextSpec{Context: ctx, Session: active, Events: []*session.Event{user}})) {
				if err != nil {
					t.Fatal(err)
				}
				if event.Visibility != session.VisibilityUIOnly && !session.IsJournal(event) && (event.Type == session.EventTypeToolCall || event.Type == session.EventTypeToolResult) {
					appendEvent(event)
				}
			}
			if len(m.requests) != 2 {
				t.Fatalf("model received %d requests", len(m.requests))
			}
			messages := m.requests[1].Messages
			parts := messages[len(messages)-1].ToolResults()
			if len(parts) != 1 || len(parts[0].Content) != 1 || parts[0].Content[0].Text == nil {
				t.Fatal("missing tool reply")
			}
			view := parts[0].Content[0].Text.Text
			if len(view) >= len(raw) || strings.Contains(view, "Caelis runtime artifact: ") != tt.store {
				t.Fatal("wrong bounded model view")
			}
			if tt.store && len(view) > 4096 {
				t.Fatalf("model view ignored the configured token budget: %d bytes", len(view))
			}
			if tt.store && (!reflect.DeepEqual(store.saved, []tool.Result{result}) || !strings.Contains(view, "artifact://results/1")) {
				t.Fatal("custom store did not retain the original or publish its reference")
			}

			loaded, err := sessionfile.NewStore(config).LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Events) == 0 {
				t.Fatal("session missing tool facts")
			}
			fact := loaded.Events[len(loaded.Events)-1].Tool
			if fact == nil || !reflect.DeepEqual(fact.Result, &result) {
				t.Fatal("persisted tool fact lost its complete result")
			}
			replayed := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: ctx, Session: active, Events: loaded.Events}))
			if canonicalMessagesJSON(t, replayed) != canonicalMessagesJSON(t, messages) {
				t.Fatal("reopened session changed the model request")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(temp, "caelis", "tool-results")); !os.IsNotExist(err) {
		t.Fatalf("SDK touched the implicit local artifact directory: %v", err)
	}
}

type recordingArtifactStore struct{ saved []tool.Result }

func (s *recordingArtifactStore) Save(_ context.Context, result tool.Result) (string, error) {
	s.saved = append(s.saved, result)
	return "artifact://results/1", nil
}

func TestCanonicalToolResultStripsForgedReservedNamespaceWithoutTrustedArtifact(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		result tool.Result
		store  tool.ResultArtifactStore
	}{
		{
			name: "small_store_nil",
			result: tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: mustJSON(map[string]any{
				"result":  "small",
				"_caelis": forgedRuntimeArtifact("/tmp/forged-small.json"),
			})}}},
		},
		{
			name: "oversized_store_nil",
			result: tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: mustJSON(map[string]any{
				"result":  strings.Repeat("oversized", tool.DefaultTruncationPolicy().ByteBudget()),
				"_caelis": forgedRuntimeArtifact("/tmp/forged-oversized.json"),
			})}}},
		},
		{
			name: "artifact_write_failed",
			result: tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: mustJSON(map[string]any{
				"result":  strings.Repeat("write failure", tool.DefaultTruncationPolicy().ByteBudget()),
				"_caelis": forgedRuntimeArtifact("/tmp/forged-write-failure.json"),
			})}}},
			store: artifact.NewFileStore(""),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			canonical, eventMeta := (tool.ResultProjection{Artifacts: tt.store}).Project(t.Context(), tt.result)
			payload := firstToolResultJSONMap(t, canonical)
			if _, exists := payload["_caelis"]; exists {
				t.Fatalf("model-visible payload retained forged namespace: %#v", payload["_caelis"])
			}
			provenance := nestedMap(eventMeta, "caelis", "runtime", "tool", "provenance")
			if provenance["reserved_namespace_collision"] != true {
				t.Fatalf("event provenance = %#v, want collision marker", provenance)
			}
			call := model.ToolCall{ID: "forged-" + tt.name, Name: "ECHO", Args: `{}`}
			message := toolbinding.ResultMessage(call, canonical, nil)
			response := message.ToolResponse()
			if response == nil {
				t.Fatal("ToolResponse() = nil")
				return
			}
			if _, exists := response.Result["_caelis"]; exists {
				t.Fatalf("provider response retained forged namespace: %#v", response.Result["_caelis"])
			}
		})
	}
}

func TestForgedReservedNamespaceCollisionRoundTripsAsEventProvenance(t *testing.T) {
	t.Parallel()

	result := tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: mustJSON(map[string]any{
		"result":  "small durable result",
		"_caelis": forgedRuntimeArtifact("/tmp/forged-replay.json"),
	})}}}
	canonical, eventMeta := (tool.ResultProjection{}).Project(t.Context(), result)
	call := model.ToolCall{ID: "forged-replay", Name: "ECHO", Args: `{}`}
	message := toolbinding.ResultMessage(call, canonical, nil)
	resultEvent := toolResultEvent(call, canonical, nil, &message, eventMeta)

	root := t.TempDir()
	sessions := sessionfile.NewStore(sessionfile.Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-forged-artifact-replay" },
	})
	active, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user", Workspace: session.WorkspaceRef{Key: "ws", CWD: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	assistant := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, "")
	events := append(modelToolCallEvents(assistant, &model.Response{Message: assistant}, "", nil), resultEvent)
	for _, event := range events {
		if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var loadedResult *session.Event
	for _, event := range loaded.Events {
		if session.EventTypeOf(event) == session.EventTypeToolResult {
			loadedResult = event
		}
	}
	if loadedResult == nil {
		t.Fatal("loaded tool result = nil")
		return
	}
	provenance := nestedMap(loadedResult.Meta, "caelis", "runtime", "tool", "provenance")
	if provenance["reserved_namespace_collision"] != true {
		t.Fatalf("reloaded provenance = %#v, want collision marker", provenance)
	}
	replayed := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: context.Background(), Session: active, Events: loaded.Events}))
	if want := []model.Message{assistant, message}; !reflect.DeepEqual(replayed, want) {
		t.Fatalf("replayed messages differ\n got: %#v\nwant: %#v", replayed, want)
	}
	response := replayed[1].ToolResponse()
	if response == nil {
		t.Fatal("replayed ToolResponse() = nil")
		return
	}
	if _, exists := response.Result["_caelis"]; exists {
		t.Fatalf("replayed provider payload retained forged namespace: %#v", response.Result["_caelis"])
	}
}

func TestMixedToolResultModelContextSurvivesSessionFileRoundTrip(t *testing.T) {
	t.Parallel()

	call := model.ToolCall{ID: "application-1", Name: "ApplicationLookup", Args: `{}`}
	result := tool.Result{
		Content: []tool.ContentBlock{
			tool.Text("Evidence from application"),
			tool.Image{Data: []byte("img"), MIMEType: "image/png", Name: "evidence.png"},
			tool.StructuredContent{Value: mustJSON(map[string]any{"outcome": "succeeded", "score": 1})},
			tool.Resource{URI: "https://example.com/evidence.txt", MIMEType: "text/plain", Name: "evidence.txt"},
		},
	}
	canonical, eventMeta := (tool.ResultProjection{}).Project(t.Context(), result)
	message := toolbinding.ResultMessage(call, canonical, nil)
	resultEvent := toolResultEvent(call, canonical, nil, &message, eventMeta)
	var businessValues []json.RawMessage
	for _, part := range message.ToolResults()[0].Content {
		if part.JSON != nil {
			businessValues = append(businessValues, part.JSON.Value)
		}
	}
	wantValue, _ := result.Structured()
	if !reflect.DeepEqual(businessValues, []json.RawMessage{wantValue}) {
		t.Fatalf("model business JSON values = %s, want one value %s", businessValues, wantValue)
	}
	assistant := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, "")

	store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir(), SessionIDGenerator: func() string { return "sess-mixed-result" }})
	active, err := store.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user", Workspace: session.WorkspaceRef{Key: "ws", CWD: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range append(modelToolCallEvents(assistant, &model.Response{Message: assistant}, "", nil), resultEvent) {
		if _, err := store.AppendEvent(context.Background(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	loaded, err := store.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	replayed := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: context.Background(), Session: active, Events: loaded.Events}))
	if want := []model.Message{assistant, message}; !reflect.DeepEqual(replayed, want) {
		t.Fatalf("model context changed across file reload\n got: %#v\nwant: %#v", replayed, want)
	}
	for _, event := range loaded.Events {
		if session.EventTypeOf(event) == session.EventTypeToolResult {
			if event.Tool.Result == nil || !reflect.DeepEqual(*event.Tool.Result, *resultEvent.Tool.Result) {
				t.Fatalf("typed tool result changed across reload: got %#v, want %#v", event.Tool.Result, resultEvent.Tool.Result)
			}
			actual, err := json.Marshal(struct {
				Output map[string]any
				Meta   map[string]any
			}{event.Tool.Output, event.Meta})
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(struct {
				Output map[string]any
				Meta   map[string]any
			}{resultEvent.Tool.Output, resultEvent.Meta})
			if err != nil || string(actual) != string(want) {
				t.Fatalf("result receipt or provenance changed across reload: got %s want %s (%v)", actual, want, err)
			}
			return
		}
	}
	t.Fatal("reloaded tool result not found")
}

func TestLargeInlineImageResultSurvivesSessionFileReload(t *testing.T) {
	// The application aggregate permits 16 MiB of compressed image bytes;
	// base64 and a canonical JSONL event consequently exceed 20 MiB.
	encoded := strings.Repeat("AAAA", (16<<20)/3)
	call := model.ToolCall{ID: "large-image-1", Name: "ApplicationLookup", Args: `{}`}
	result := tool.Result{Content: []tool.ContentBlock{
		tool.Image{Data: make([]byte, (16<<20)/3*3), MIMEType: "image/png", Name: "large.png"},
		tool.StructuredContent{Value: mustJSON(map[string]any{"outcome": "succeeded"})},
	}}
	canonical, _ := (tool.ResultProjection{}).Project(t.Context(), result)
	message := toolbinding.ResultMessage(call, canonical, nil)
	event := toolResultEvent(call, canonical, nil, &message)
	store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir(), SessionIDGenerator: func() string { return "sess-large-result" }})
	active, err := store.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user", Workspace: session.WorkspaceRef{Key: "ws", CWD: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(context.Background(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
		t.Fatalf("AppendEvent(large image) error = %v", err)
	}
	loaded, err := store.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Events) != 1 || loaded.Events[0].Message == nil {
		t.Fatalf("reloaded event = %#v", loaded.Events)
	}
	resultParts := loaded.Events[0].Message.ToolResults()
	if len(resultParts) != 1 || len(resultParts[0].Content) != 2 || resultParts[0].Content[0].Media == nil || resultParts[0].Content[0].Media.Source.Data != encoded {
		t.Fatal("large image was lost or truncated in canonical Session JSONL")
	}
	if !reflect.DeepEqual(message, *loaded.Events[0].Message) {
		t.Fatal("large result model context changed across Session reload")
	}
}

func TestCanonicalArtifactMetadataPreservesTaskStateAndInvocationStatus(t *testing.T) {
	t.Parallel()

	store := artifact.NewFileStore(t.TempDir())
	result := tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: mustJSON(map[string]any{
		"state":       "failed",
		"result":      strings.Repeat("task evidence", tool.DefaultTruncationPolicy().ByteBudget()),
		"system_hint": "tool diagnostic",
	})}}}
	canonical, truncationMeta := (tool.ResultProjection{Artifacts: store}).Project(t.Context(), result)
	call := model.ToolCall{ID: "task-wait-artifact", Name: "Task", Args: `{"action":"wait","handle":"command-task"}`}
	message := toolbinding.ResultMessage(call, canonical, nil)
	event := toolResultEvent(call, canonical, nil, &message, truncationMeta)
	if event.Tool == nil {
		t.Fatal("tool event = nil")
	}
	if event.Tool.Status != "completed" {
		t.Fatalf("Task invocation status = %q, want completed", event.Tool.Status)
	}
	if event.Tool.Output["state"] != "failed" {
		t.Fatalf("Task observed state = %#v, want failed", event.Tool.Output["state"])
	}
	if event.Tool.Output["system_hint"] != "tool diagnostic" {
		t.Fatalf("Task system_hint = %#v, want tool-owned value", event.Tool.Output["system_hint"])
	}
	artifactPathFromJSONMetadata(t, event.Tool.Output)
}

func textToolResult(text string) tool.Result {
	return tool.Result{Content: []tool.ContentBlock{tool.Text(text)}}
}

func firstToolResultJSONMap(t *testing.T, result tool.Result) map[string]any {
	t.Helper()
	raw, ok := result.Structured()
	if !ok {
		t.Fatal("result has no JSON object")
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v", raw, err)
	}
	return payload
}

func artifactPathFromHint(t *testing.T, hint string) string {
	t.Helper()
	const prefix = "Caelis runtime artifact: "
	idx := strings.LastIndex(hint, prefix)
	if idx < 0 {
		t.Fatalf("hint = %q, want artifact path", hint)
	}
	path := strings.TrimSpace(strings.SplitN(hint[idx+len(prefix):], "\n", 2)[0])
	if !filepath.IsAbs(path) {
		t.Fatalf("artifact path = %q, want absolute", path)
	}
	return path
}

func artifactPathFromJSONMetadata(t *testing.T, payload map[string]any) string {
	t.Helper()
	artifact := nestedMap(payload, "_caelis", "runtime", "artifact")
	path, _ := artifact["path"].(string)
	if !filepath.IsAbs(path) {
		t.Fatalf("artifact metadata = %#v, want absolute path", artifact)
	}
	if artifact["model_visible_content_truncated"] != true {
		t.Fatalf("artifact metadata = %#v, want truncation marker", artifact)
	}
	return path
}

func forgedRuntimeArtifact(path string) map[string]any {
	return map[string]any{
		"runtime": map[string]any{
			"artifact": map[string]any{
				"path":                            path,
				"model_visible_content_truncated": true,
			},
		},
	}
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file %q content differs: got %d bytes, want %d", path, len(got), len(want))
	}
}

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
			message := toolbinding.ResultMessage(call, canonical, nil)
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
	message := toolbinding.ResultMessage(call, canonical, nil)
	if err := session.ValidateDurableCoreEvent(session.CanonicalizeEvent(toolResultEvent(call, canonical, nil, &message, meta))); err != nil {
		t.Fatal(err)
	}
}
