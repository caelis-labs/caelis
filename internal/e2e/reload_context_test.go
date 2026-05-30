package e2e

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/core/tool"
	storejsonl "github.com/OnslaughtSnail/caelis/internal/adapters/store/jsonl"
	storesqlite "github.com/OnslaughtSnail/caelis/internal/adapters/store/sqlite"
	enginecontext "github.com/OnslaughtSnail/caelis/internal/engine/context"
	"github.com/OnslaughtSnail/caelis/internal/engine/gateway"
	"github.com/OnslaughtSnail/caelis/internal/engine/loop"
)

func TestReloadedGatewayRebuildsProviderContextFromCanonicalStore(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	cases := []struct {
		name string
		open func(t *testing.T, root string) session.Store
	}{
		{
			name: "jsonl",
			open: func(t *testing.T, root string) session.Store {
				t.Helper()
				store, err := storejsonl.New(filepath.Join(root, "sessions"))
				if err != nil {
					t.Fatal(err)
				}
				return store
			},
		},
		{
			name: "sqlite",
			open: func(t *testing.T, root string) session.Store {
				t.Helper()
				store, err := storesqlite.New(filepath.Join(root, "sessions.db"))
				if err != nil {
					t.Fatal(err)
				}
				return store
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store := tc.open(t, root)
			initialProvider := &reloadScriptedProvider{responses: []model.Response{
				{Message: model.Message{
					Role:  model.RoleAssistant,
					Parts: []model.Part{model.NewTextPart("pre-compact answer")},
					Usage: &model.Usage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10},
				}},
				{Message: assistantToolUseMessage(now)},
				{Message: model.Message{
					Role: model.RoleAssistant,
					Parts: []model.Part{
						{
							Kind: model.PartReasoning,
							Reasoning: &model.ReasoningPart{
								VisibleText: "checked tool output",
								Visibility:  model.ReasoningHidden,
								Replay:      &model.ReplayMeta{Provider: "scripted", Kind: "reasoning_signature", Token: "final-replay-token"},
							},
						},
						model.NewTextPart("post-compact answer"),
					},
					Origin: &model.Origin{
						Provider:        "scripted",
						Model:           "reload-model",
						RawFinishReason: "stop",
						CreatedAt:       now.Add(time.Second),
						Metadata: map[string]json.RawMessage{
							"response_id": json.RawMessage(`"resp-final"`),
						},
					},
					Usage: &model.Usage{InputTokens: 20, CachedInputTokens: 5, OutputTokens: 6, ReasoningTokens: 2, TotalTokens: 28, ContextWindowTokens: 4096},
					Meta: map[string]any{
						"provider_checkpoint": "final",
					},
				}},
			}}
			gw := newReloadGateway(t, store, initialProvider)
			active, err := gw.StartSession(ctx, session.StartRequest{
				AppName:            "caelis",
				UserID:             "tester",
				PreferredSessionID: "sess-reload-context",
				Workspace:          session.Workspace{Key: "repo", CWD: "/tmp/repo"},
			})
			if err != nil {
				t.Fatal(err)
			}

			runReloadTurn(t, gw, active.Ref, "pre compact prompt")
			compact := compactCheckpointEvent(now.Add(2 * time.Second))
			if _, err := gw.RecordEvents(ctx, active.Ref, []session.Event{compact}); err != nil {
				t.Fatal(err)
			}
			runReloadTurn(t, gw, active.Ref, "use tool after compact")

			beforeReload, err := store.Load(ctx, active.Ref)
			if err != nil {
				t.Fatal(err)
			}
			expectedContext := enginecontext.SnapshotMessages(beforeReload)
			assertReloadSemanticContext(t, expectedContext)
			closeStore(t, store)

			reloadedStore := tc.open(t, root)
			t.Cleanup(func() { closeStore(t, reloadedStore) })
			afterReload, err := reloadedStore.Load(ctx, active.Ref)
			if err != nil {
				t.Fatal(err)
			}
			assertMessagesEqual(t, enginecontext.SnapshotMessages(afterReload), expectedContext, "reloaded store context")

			reloadProvider := &reloadScriptedProvider{responses: []model.Response{{
				Message: model.Message{
					Role:  model.RoleAssistant,
					Parts: []model.Part{model.NewTextPart("answer after reload")},
				},
			}}}
			reloadedGateway := newReloadGateway(t, reloadedStore, reloadProvider)
			runReloadTurn(t, reloadedGateway, active.Ref, "continue after reload")

			requests := reloadProvider.Requests()
			if len(requests) != 1 {
				t.Fatalf("reloaded provider requests = %d, want 1", len(requests))
			}
			wantRequestMessages := cloneReloadMessages(expectedContext)
			wantRequestMessages = append(wantRequestMessages, model.Message{
				Role:  model.RoleUser,
				Parts: []model.Part{model.NewTextPart("continue after reload")},
			})
			assertMessagesEqual(t, requests[0].Messages, wantRequestMessages, "reloaded provider request")
		})
	}
}

func newReloadGateway(t *testing.T, store session.Store, provider model.Provider) *gateway.Gateway {
	t.Helper()
	runner, err := loop.New(loop.Config{
		Provider: provider,
		Tools: reloadTools{tool.NamedTool{
			Def: tool.Definition{Name: "ECHO"},
			Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
				return tool.Result{
					ID:   call.ID,
					Name: call.Name,
					Content: []model.Part{
						model.NewTextPart("tool output after compact"),
						{
							Kind: model.PartJSON,
							JSON: &model.JSONPart{Value: json.RawMessage(`{"ok":true,"source":"canonical"}`)},
						},
					},
					Meta: map[string]any{
						"result_id": "tool-result-1",
					},
				}, nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	gw, err := gateway.New(gateway.Config{Store: store, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	return gw
}

func assistantToolUseMessage(now time.Time) model.Message {
	return model.Message{
		Role: model.RoleAssistant,
		Parts: []model.Part{
			{
				Kind: model.PartReasoning,
				Reasoning: &model.ReasoningPart{
					VisibleText: "need echo",
					Visibility:  model.ReasoningHidden,
					Replay:      &model.ReplayMeta{Provider: "scripted", Kind: "reasoning_signature", Token: "tool-replay-token"},
					ProviderDetails: map[string]json.RawMessage{
						"encrypted": json.RawMessage(`"reasoning-payload"`),
					},
				},
			},
			{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "call-echo",
					Name:  "ECHO",
					Input: json.RawMessage(`{"text":"hello"}`),
					Replay: &model.ReplayMeta{
						Provider: "scripted",
						Kind:     "tool_call_signature",
						Token:    "tool-call-replay-token",
					},
					ProviderDetails: map[string]json.RawMessage{
						"provider_call_id": json.RawMessage(`"provider-call-1"`),
					},
				},
			},
		},
		Origin: &model.Origin{
			Provider:        "scripted",
			Model:           "reload-model",
			RawFinishReason: "tool_calls",
			CreatedAt:       now,
			Metadata: map[string]json.RawMessage{
				"response_id": json.RawMessage(`"resp-tool"`),
			},
		},
		Usage: &model.Usage{InputTokens: 15, OutputTokens: 4, ReasoningTokens: 1, TotalTokens: 20, ContextWindowTokens: 4096},
		Meta: map[string]any{
			"provider_checkpoint": "tool-use",
		},
	}
}

func compactCheckpointEvent(at time.Time) session.Event {
	message := model.Message{
		Role:  model.RoleUser,
		Parts: []model.Part{model.NewTextPart("CONTEXT CHECKPOINT\nOnly retain post-compact runtime semantics.")},
		Usage: &model.Usage{InputTokens: 30, OutputTokens: 8, TotalTokens: 38},
		Meta: map[string]any{
			"caelis_compact_checkpoint": true,
		},
	}
	return session.Event{
		Type:       session.EventCompact,
		Visibility: session.VisibilityCanonical,
		Time:       at,
		Actor:      session.ActorRef{Kind: session.ActorSystem, ID: "compact", Name: "compact"},
		Message:    &message,
		Meta: map[string]any{
			"compact": map[string]any{
				"contract_version": 1,
				"source_event_ids": []any{
					"evt-1",
					"evt-2",
				},
			},
			"usage_category": "compact",
		},
	}
}

func assertReloadSemanticContext(t *testing.T, messages []model.Message) {
	t.Helper()
	if len(messages) != 5 {
		t.Fatalf("rebuilt context messages = %d, want compact checkpoint, user, assistant tool use, tool result, final assistant", len(messages))
	}
	rendered := dumpReloadMessages(messages)
	if strings.Contains(rendered, "pre compact prompt") || strings.Contains(rendered, "pre-compact answer") {
		t.Fatalf("rebuilt context leaked pre-compact messages: %s", rendered)
	}
	if got := messages[0].TextContent(); !strings.Contains(got, "CONTEXT CHECKPOINT") {
		t.Fatalf("first rebuilt message = %q, want compact checkpoint", got)
	}
	if got := messages[1].TextContent(); got != "use tool after compact" {
		t.Fatalf("post-compact user message = %q, want use tool after compact", got)
	}
	if reasoning := messages[2].Parts[0].Reasoning; reasoning == nil || reasoning.Replay == nil || reasoning.Replay.Token != "tool-replay-token" {
		t.Fatalf("assistant reasoning replay = %#v, want preserved tool replay token", reasoning)
	}
	calls := messages[2].ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call-echo" || calls[0].Replay == nil || calls[0].Replay.Token != "tool-call-replay-token" {
		t.Fatalf("assistant tool calls = %#v, want replayable call-echo", calls)
	}
	result := messages[3].Parts[0].ToolResult
	if result == nil || result.ToolCallID != "call-echo" || len(result.Content) < 2 {
		t.Fatalf("tool result = %#v, want canonical call-echo result with content", result)
	}
	final := messages[4]
	if final.Origin == nil || final.Origin.RawFinishReason != "stop" || final.Usage == nil || final.Usage.TotalTokens != 28 {
		t.Fatalf("final assistant metadata = origin %#v usage %#v, want provider origin and usage", final.Origin, final.Usage)
	}
}

func runReloadTurn(t *testing.T, gw *gateway.Gateway, ref session.Ref, input string) []session.Event {
	t.Helper()
	turn, err := gw.BeginTurn(context.Background(), coreruntime.TurnRequest{
		SessionRef: ref,
		Input:      input,
		Surface:    "reload-test",
		Model:      "reload-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []session.Event
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatalf("turn error: %s", env.Err)
		}
		events = append(events, session.CloneEvent(env.Event))
	}
	return events
}

func assertMessagesEqual(t *testing.T, got []model.Message, want []model.Message, label string) {
	t.Helper()
	if reflect.DeepEqual(got, want) {
		return
	}
	t.Fatalf("%s messages mismatch\ngot:  %s\nwant: %s", label, dumpReloadMessages(got), dumpReloadMessages(want))
}

func dumpReloadMessages(messages []model.Message) string {
	raw, err := json.Marshal(messages)
	if err != nil {
		return err.Error()
	}
	return string(raw)
}

func closeStore(t *testing.T, store session.Store) {
	t.Helper()
	if closer, ok := store.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func cloneReloadMessages(in []model.Message) []model.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]model.Message, 0, len(in))
	for _, message := range in {
		out = append(out, model.CloneMessage(message))
	}
	return out
}

type reloadScriptedProvider struct {
	mu        sync.Mutex
	requests  []model.Request
	responses []model.Response
}

func (p *reloadScriptedProvider) ID() string {
	return "reload-scripted"
}

func (p *reloadScriptedProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return []model.ModelInfo{{ID: "reload-model", Provider: "reload-scripted", SupportsToolCalls: true}}, nil
}

func (p *reloadScriptedProvider) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, cloneReloadRequest(req))
	if len(p.responses) == 0 {
		return &model.StaticStream{Events: []model.StreamEvent{{
			Type: model.StreamTurnDone,
			Response: &model.Response{
				Status:  model.ResponseCompleted,
				Message: model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.NewTextPart("default")}},
			},
		}}}, nil
	}
	response := p.responses[0]
	p.responses = p.responses[1:]
	response.Message = model.CloneMessage(response.Message)
	if response.Status == "" {
		response.Status = model.ResponseCompleted
	}
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type:     model.StreamTurnDone,
		Response: &response,
	}}}, nil
}

func (p *reloadScriptedProvider) Requests() []model.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]model.Request, 0, len(p.requests))
	for _, req := range p.requests {
		out = append(out, cloneReloadRequest(req))
	}
	return out
}

func cloneReloadRequest(in model.Request) model.Request {
	out := in
	out.Messages = cloneReloadMessages(in.Messages)
	out.Tools = append([]model.ToolSpec(nil), in.Tools...)
	out.Instructions = append([]string(nil), in.Instructions...)
	return out
}

type reloadTools []tool.Tool

func (s reloadTools) List(context.Context) ([]tool.Tool, error) {
	return append([]tool.Tool(nil), s...), nil
}

func (s reloadTools) Lookup(_ context.Context, name string) (tool.Tool, bool, error) {
	for _, item := range s {
		if item != nil && strings.EqualFold(item.Definition().Name, name) {
			return item, true, nil
		}
	}
	return nil, false, nil
}

var _ model.Provider = (*reloadScriptedProvider)(nil)
var _ tool.Registry = reloadTools{}
