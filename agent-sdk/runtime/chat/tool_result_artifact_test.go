package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestCanonicalTerminalToolResultWritesJSONArtifactWithoutChangingToolHint(t *testing.T) {
	t.Parallel()

	store := artifact.NewFileStore(t.TempDir())
	large := strings.Repeat("evidence line\n", tool.DefaultTruncationPolicy().ByteBudget()/8)
	raw := mustJSON(map[string]any{
		"result":      large,
		"system_hint": "Keep the original diagnostic hint.",
	})
	result := tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}}}
	canonical, truncationMeta := canonicalToolResult(t.Context(), result, store)
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
	message := toolResultMessageFromCanonical(call, canonical, nil)
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

func TestCanonicalTerminalToolResultAddsTextAndScalarJSONHints(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		result    tool.Result
		extension string
		assert    func(*testing.T, tool.Result) string
	}{
		{
			name:      "text",
			result:    textToolResult(strings.Repeat("text evidence\n", tool.DefaultTruncationPolicy().ByteBudget()/8)),
			extension: ".txt",
			assert: func(t *testing.T, result tool.Result) string {
				t.Helper()
				for _, part := range tool.ModelParts(result.Content) {
					if part.Text != nil && strings.Contains(part.Text.Text, "Caelis runtime artifact: ") {
						if strings.Contains(part.Text.Text, "System hint:") {
							t.Fatalf("text artifact metadata claims system-hint authority: %q", part.Text.Text)
						}
						return artifactPathFromHint(t, part.Text.Text)
					}
				}
				t.Fatal("canonical text result has no artifact hint")
				return ""
			},
		},
		{
			name: "scalar_json",
			result: tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: mustJSONValue(t,
				strings.Repeat("scalar evidence", tool.DefaultTruncationPolicy().ByteBudget()/4),
			)}}},
			extension: ".json",
			assert: func(t *testing.T, result tool.Result) string {
				t.Helper()
				payload := firstToolResultJSONMap(t, result)
				if _, ok := payload["result"].(string); !ok {
					t.Fatalf("scalar JSON payload = %#v, want wrapped result", payload)
				}
				return artifactPathFromJSONMetadata(t, payload)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := artifact.NewFileStore(t.TempDir())
			canonical, _ := canonicalToolResult(t.Context(), tt.result, store)
			path := tt.assert(t, canonical)
			if filepath.Ext(path) != tt.extension {
				t.Fatalf("artifact path = %q, want %s", path, tt.extension)
			}
			var want []byte
			switch block := tt.result.Content[0].(type) {
			case tool.Text:
				want = []byte(block)
			case tool.StructuredContent:
				want = block.Value
			}
			assertFileContent(t, path, want)
		})
	}
}

func TestCanonicalTerminalToolResultDoesNotWriteWithoutTruncationOrAfterWriteFailure(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "artifacts")
	store := artifact.NewFileStore(dir)
	canonical, _ := canonicalToolResult(t.Context(), textToolResult("small"), store)
	if strings.Contains(string(canonical.Content[0].(tool.Text)), "Caelis runtime artifact: ") {
		t.Fatalf("untruncated result = %#v, should not contain artifact hint", canonical)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("artifact dir stat error = %v, want not exist", err)
	}

	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	large := textToolResult(strings.Repeat("large", tool.DefaultTruncationPolicy().ByteBudget()))
	canonical, _ = canonicalToolResult(t.Context(), large, store)
	for _, part := range tool.ModelParts(canonical.Content) {
		if part.Text != nil && strings.Contains(part.Text.Text, "Caelis runtime artifact: ") {
			t.Fatalf("write-failed result contains artifact hint: %#v", canonical)
		}
	}
}

// The same large tool reply must produce a replayable view with either factory
// path, including when the host has no filesystem-backed result store.
func TestFactoryResultArtifactsSurviveModelRefreshAndReplay(t *testing.T) {
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
				factory.ResultArtifacts = store
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
			if len(view) >= len(raw) || strings.Contains(view, toolResultArtifactHintPrefix) != tt.store {
				t.Fatal("wrong bounded model view")
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

func TestCanonicalTerminalToolResultControlsReservedNamespaceCollision(t *testing.T) {
	t.Parallel()

	store := artifact.NewFileStore(t.TempDir())
	raw := mustJSON(map[string]any{
		"result":      strings.Repeat("evidence", tool.DefaultTruncationPolicy().ByteBudget()),
		"system_hint": "tool-owned collision diagnostic",
		"_caelis":     map[string]any{"tool_owned": "must not become Runtime authority"},
	})
	canonical, _ := canonicalToolResult(t.Context(), tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}}}, store)
	payload := firstToolResultJSONMap(t, canonical)
	artifact := nestedMap(payload, "_caelis", "runtime", "artifact")
	if artifact["tool_namespace_collision"] != true {
		t.Fatalf("artifact metadata = %#v, want controlled collision marker", artifact)
	}
	if payload["system_hint"] != "tool-owned collision diagnostic" {
		t.Fatalf("system_hint = %#v, want tool-owned value unchanged", payload["system_hint"])
	}
	path, _ := artifact["path"].(string)
	assertFileContent(t, path, raw)
}

func TestCanonicalToolResultStripsForgedReservedNamespaceWithoutTrustedArtifact(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		result tool.Result
		store  ResultArtifactStore
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
			canonical, eventMeta := canonicalToolResult(t.Context(), tt.result, tt.store)
			payload := firstToolResultJSONMap(t, canonical)
			if _, exists := payload["_caelis"]; exists {
				t.Fatalf("model-visible payload retained forged namespace: %#v", payload["_caelis"])
			}
			provenance := nestedMap(eventMeta, "caelis", "runtime", "tool", "provenance")
			if provenance["reserved_namespace_collision"] != true {
				t.Fatalf("event provenance = %#v, want collision marker", provenance)
			}
			call := model.ToolCall{ID: "forged-" + tt.name, Name: "ECHO", Args: `{}`}
			message := toolResultMessageFromCanonical(call, canonical, nil)
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
	canonical, eventMeta := canonicalToolResult(t.Context(), result, nil)
	call := model.ToolCall{ID: "forged-replay", Name: "ECHO", Args: `{}`}
	message := toolResultMessageFromCanonical(call, canonical, nil)
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
	canonical, eventMeta := canonicalToolResult(t.Context(), result, nil)
	message := toolResultMessageFromCanonical(call, canonical, nil)
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
	canonical, _ := canonicalToolResult(t.Context(), result, nil)
	message := toolResultMessageFromCanonical(call, canonical, nil)
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
	canonical, truncationMeta := canonicalToolResult(t.Context(), result, store)
	call := model.ToolCall{ID: "task-wait-artifact", Name: "Task", Args: `{"action":"wait","handle":"command-task"}`}
	message := toolResultMessageFromCanonical(call, canonical, nil)
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

func mustJSONValue(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return raw
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
