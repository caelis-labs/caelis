package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	sessionfile "github.com/OnslaughtSnail/caelis/impl/session/file"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"google.golang.org/genai"
)

func payloadTools(payload map[string]any) []any {
	tools, _ := payload["tools"].([]any)
	return tools
}

func payloadHasToolKey(tools []any, keys ...string) bool {
	for _, item := range tools {
		obj, _ := item.(map[string]any)
		for _, key := range keys {
			if _, ok := obj[key]; ok {
				return true
			}
		}
	}
	return false
}

func payloadToolConfigBool(payload map[string]any, key string) bool {
	cfg, _ := payload["toolConfig"].(map[string]any)
	value, _ := cfg[key].(bool)
	return value
}

func TestGeminiRequest_IncludesGoogleSearchByDefaultForCurrentModels(t *testing.T) {
	var payload map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "gemini-2.5-flash",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "Who won the euro 2024?")},
	}) {
		if err != nil {
			t.Fatalf("expected no generate error, got %v", err)
		}
	}

	if !payloadHasToolKey(payloadTools(payload), "googleSearch", "google_search") {
		t.Fatalf("payload tools = %#v, want Gemini Google Search tool", payload["tools"])
	}
	if !payloadToolConfigBool(payload, "includeServerSideToolInvocations") {
		t.Fatalf("payload toolConfig = %#v, want includeServerSideToolInvocations=true", payload["toolConfig"])
	}
}

func TestGeminiRequest_DisabledProviderSpecOptOutSkipsDefaultGoogleSearch(t *testing.T) {
	var payload map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "gemini-2.5-flash",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Tools: []model.ToolSpec{
			model.NewProviderExecutedToolSpec("gemini", geminiGoogleSearchToolName, map[string]json.RawMessage{
				"disabled": json.RawMessage(`true`),
			}),
		},
	}) {
		if err != nil {
			t.Fatalf("expected no generate error, got %v", err)
		}
	}

	if payloadHasToolKey(payloadTools(payload), "googleSearch", "google_search") {
		t.Fatalf("payload tools = %#v, want no Gemini Google Search tool", payload["tools"])
	}
	if payloadToolConfigBool(payload, "includeServerSideToolInvocations") {
		t.Fatalf("payload toolConfig = %#v, want no server-side invocation opt-in", payload["toolConfig"])
	}
}

func TestGeminiRequest_DoesNotDefaultGoogleSearchForLegacyModels(t *testing.T) {
	var payload map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "gemini-1.5-pro",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
	}) {
		if err != nil {
			t.Fatalf("expected no generate error, got %v", err)
		}
	}

	if payloadHasToolKey(payloadTools(payload), "googleSearch", "google_search") {
		t.Fatalf("payload tools = %#v, want no current Google Search tool for legacy model", payload["tools"])
	}
}

func TestGeminiRequest_ProviderExecutedGoogleSearchCombinesWithFunctionTools(t *testing.T) {
	var payload map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Tools: []model.ToolSpec{
			model.NewFunctionToolSpec("lookup", "Look up local data.", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
				},
			}),
			model.NewProviderExecutedToolSpec("gemini", geminiGoogleSearchToolName, nil),
		},
	}) {
		if err != nil {
			t.Fatalf("expected no generate error, got %v", err)
		}
	}

	tools := payloadTools(payload)
	if !payloadHasToolKey(tools, "functionDeclarations", "function_declarations") {
		t.Fatalf("payload tools = %#v, want function declarations", payload["tools"])
	}
	if !payloadHasToolKey(tools, "googleSearch", "google_search") {
		t.Fatalf("payload tools = %#v, want Gemini Google Search tool", payload["tools"])
	}
	if !payloadToolConfigBool(payload, "includeServerSideToolInvocations") {
		t.Fatalf("payload toolConfig = %#v, want includeServerSideToolInvocations=true", payload["toolConfig"])
	}
}

func TestGeminiUsesProviderExecutedToolsReflectsDefaultAndOptOut(t *testing.T) {
	llm, ok := newGemini(Config{Provider: "gemini", Model: "gemini-2.5-flash"}, "token").(*geminiLLM)
	if !ok {
		t.Fatal("newGemini() did not return *geminiLLM")
	}
	if !llm.UsesProviderExecutedTools(&model.Request{}) {
		t.Fatal("UsesProviderExecutedTools() = false, want Gemini 2+ default Google Search visible to retry policy")
	}
	if llm.UsesProviderExecutedTools(&model.Request{
		Tools: []model.ToolSpec{
			model.NewProviderExecutedToolSpec("gemini", geminiGoogleSearchToolName, map[string]json.RawMessage{
				"disabled": json.RawMessage(`true`),
			}),
		},
	}) {
		t.Fatal("UsesProviderExecutedTools() = true, want disabled Google Search hidden from retry policy")
	}

	legacy, ok := newGemini(Config{Provider: "gemini", Model: "gemini-1.5-pro"}, "token").(*geminiLLM)
	if !ok {
		t.Fatal("newGemini() did not return *geminiLLM")
	}
	if legacy.UsesProviderExecutedTools(&model.Request{}) {
		t.Fatal("legacy UsesProviderExecutedTools() = true, want false")
	}
}

func TestGeminiStream_PreservesInterleavedPartOrderForReplay(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1beta/models/test-model:streamGenerateContent") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunks := []string{
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"pre-"}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"toolCall":{"id":"search-1","toolType":"GOOGLE_SEARCH_WEB","args":{"query":"latest release"}}}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"mid-"}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"lookup","args":{"query":"release"}},"thoughtSignature":"c2lnLWNhbGwtMQ=="}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"toolResponse":{"id":"search-1","toolType":"GOOGLE_SEARCH_WEB","response":{"status":"ok"}}}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]}}]}`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	var final model.Message
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   true,
	}) {
		if err != nil {
			t.Fatalf("expected no stream error, got %v", err)
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			final = resp.Response.Message
		}
	}

	_, contents, err := toGeminiContents(nil, []model.Message{final})
	if err != nil {
		t.Fatalf("toGeminiContents: %v", err)
	}
	if len(contents) != 1 || len(contents[0].Parts) != 6 {
		t.Fatalf("contents = %#v, want one assistant content with 6 ordered parts", contents)
	}
	parts := contents[0].Parts
	if parts[0].Text != "pre-" {
		t.Fatalf("part[0] = %#v, want leading text", parts[0])
	}
	if parts[1].ToolCall == nil || parts[1].ToolCall.ID != "search-1" {
		t.Fatalf("part[1] = %#v, want server-side tool call", parts[1])
	}
	if parts[2].Text != "mid-" {
		t.Fatalf("part[2] = %#v, want middle text", parts[2])
	}
	if parts[3].FunctionCall == nil || parts[3].FunctionCall.ID != "call-1" {
		t.Fatalf("part[3] = %#v, want client function call", parts[3])
	}
	if parts[4].ToolResponse == nil || parts[4].ToolResponse.ID != "search-1" {
		t.Fatalf("part[4] = %#v, want server-side tool response", parts[4])
	}
	if parts[5].Text != "done" {
		t.Fatalf("part[5] = %#v, want final text", parts[5])
	}
}

func TestGeminiResponseToMessage_PreservesServerSideToolPartsForReplay(t *testing.T) {
	msg, _, err := geminiResponseToMessage(&genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{
							ToolCall: &genai.ToolCall{
								ID:       "search-1",
								ToolType: genai.ToolTypeGoogleSearchWeb,
								Args:     map[string]any{"query": "latest release"},
							},
						},
						{
							ToolResponse: &genai.ToolResponse{
								ID:       "search-1",
								ToolType: genai.ToolTypeGoogleSearchWeb,
								Response: map[string]any{"status": "ok"},
							},
						},
						{
							ThoughtSignature: []byte("sig-call-1"),
							FunctionCall: &genai.FunctionCall{
								ID:   "call-1",
								Name: "lookup",
								Args: map[string]any{"query": "release"},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(msg.ToolCalls()); got != 1 {
		t.Fatalf("len(ToolCalls) = %d, want only client-executed function call", got)
	}
	_, contents, err := toGeminiContents(nil, []model.Message{msg})
	if err != nil {
		t.Fatalf("toGeminiContents: %v", err)
	}
	if len(contents) != 1 || len(contents[0].Parts) != 3 {
		t.Fatalf("contents = %#v, want one assistant content with 3 parts", contents)
	}
	if contents[0].Parts[0].ToolCall == nil || contents[0].Parts[0].ToolCall.ID != "search-1" {
		t.Fatalf("first part = %#v, want replayed server-side tool call", contents[0].Parts[0])
	}
	if contents[0].Parts[1].ToolResponse == nil || contents[0].Parts[1].ToolResponse.ID != "search-1" {
		t.Fatalf("second part = %#v, want replayed server-side tool response", contents[0].Parts[1])
	}
	if contents[0].Parts[2].FunctionCall == nil || contents[0].Parts[2].FunctionCall.ID != "call-1" {
		t.Fatalf("third part = %#v, want function call", contents[0].Parts[2])
	}
}

func TestGeminiServerToolReplayPartsRoundTripThroughFileStore(t *testing.T) {
	msg, _, err := geminiResponseToMessage(&genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{
							ToolCall: &genai.ToolCall{
								ID:       "search-1",
								ToolType: genai.ToolTypeGoogleSearchWeb,
								Args:     map[string]any{"query": "latest release"},
							},
						},
						{
							ToolResponse: &genai.ToolResponse{
								ID:       "search-1",
								ToolType: genai.ToolTypeGoogleSearchWeb,
								Response: map[string]any{"status": "ok"},
							},
						},
						{Text: "done"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-gemini-server-tools" },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-gemini-server-tools",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event: &session.Event{
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Message:    &msg,
			Text:       msg.TextContent(),
		},
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if len(loaded.Events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(loaded.Events))
	}
	loadedMsg, ok := session.ModelMessageOf(loaded.Events[0])
	if !ok {
		t.Fatalf("loaded event = %#v, want model message", loaded.Events[0])
	}
	_, contents, err := toGeminiContents(nil, []model.Message{loadedMsg})
	if err != nil {
		t.Fatalf("toGeminiContents: %v", err)
	}
	if len(contents) != 1 || len(contents[0].Parts) != 3 {
		t.Fatalf("contents = %#v, want one assistant content with 3 parts", contents)
	}
	if contents[0].Parts[0].ToolCall == nil || contents[0].Parts[0].ToolCall.ID != "search-1" {
		t.Fatalf("first part = %#v, want replayed server-side tool call", contents[0].Parts[0])
	}
	if contents[0].Parts[1].ToolResponse == nil || contents[0].Parts[1].ToolResponse.ID != "search-1" {
		t.Fatalf("second part = %#v, want replayed server-side tool response", contents[0].Parts[1])
	}
	if contents[0].Parts[2].Text != "done" {
		t.Fatalf("third part = %#v, want final text", contents[0].Parts[2])
	}
}

func TestGeminiResponseToMessage_DoesNotAttachEmptyReplayMeta(t *testing.T) {
	msg, _, err := geminiResponseToMessage(&genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{
							FunctionCall: &genai.FunctionCall{
								ID:   "call-1",
								Name: "lookup",
								Args: map[string]any{"query": "release"},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(msg.Parts); got != 1 {
		t.Fatalf("len(parts) = %d, want 1", got)
	}
	if msg.Parts[0].ToolUse == nil {
		t.Fatalf("part = %#v, want tool use", msg.Parts[0])
	}
	if msg.Parts[0].ToolUse.Replay != nil {
		t.Fatalf("replay = %#v, want nil for unsigned function call", msg.Parts[0].ToolUse.Replay)
	}
	_, contents, err := toGeminiContents(nil, []model.Message{msg})
	if err != nil {
		t.Fatalf("toGeminiContents: %v", err)
	}
	if len(contents) != 0 {
		t.Fatalf("contents = %#v, want unsigned function call omitted from replay", contents)
	}
}

func TestGeminiAssistantContentPartsUnwrapsNormalizedToolUseInput(t *testing.T) {
	part := model.NewToolUsePart("call-1", "lookup", json.RawMessage("```json\n{\"query\":\"release\"}\n```"))
	if part.ToolUse == nil {
		t.Fatal("NewToolUsePart() produced nil ToolUse")
	}
	part.ToolUse.Replay = &model.ReplayMeta{
		Provider: geminiReplayProvider,
		Token:    encodeGeminiThoughtSignature([]byte("sig-call-1")),
	}

	_, contents, err := toGeminiContents(nil, []model.Message{model.NewMessage(model.RoleAssistant, part)})
	if err != nil {
		t.Fatalf("toGeminiContents: %v", err)
	}
	if len(contents) != 1 || len(contents[0].Parts) != 1 || contents[0].Parts[0].FunctionCall == nil {
		t.Fatalf("contents = %#v, want one function call", contents)
	}
	args := contents[0].Parts[0].FunctionCall.Args
	if got, _ := args["query"].(string); got != "release" {
		t.Fatalf("function args = %#v, want unwrapped query=release", args)
	}
	if _, ok := args["__caelis_raw_tool_input"]; ok {
		t.Fatalf("function args = %#v, leaked normalized raw-input wrapper", args)
	}
}
