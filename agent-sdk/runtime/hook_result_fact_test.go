package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestHooksCodeModeFullResultAndModelViewShareFact(t *testing.T) {
	for _, mode := range []string{"accepted", "result-failure", "view-failure", "invalid-structured", "invalid-output-schema", "forged-completion"} {
		t.Run(mode, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			var effects, fullCalls, viewCalls atomic.Int64
			raw := json.RawMessage("{\"value\":\"secret\"}")
			accepted := json.RawMessage("{\"value\":\"accepted\"}")
			probe := tool.NamedTool{Def: tool.Definition{Name: "ReadFact", InputSchema: map[string]any{"type": "object"}, OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []string{"value"}}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				effects.Add(1)
				return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}}}, nil
			}}
			hooks := hook.Set{
				ToolResult: []hook.ToolResult{{Name: "redact-business", Apply: func(_ context.Context, e hook.ToolContext, o hook.ToolOutcome) (tool.Result, error) {
					if e.Tool.Name != "ReadFact" {
						return o.Result, nil
					}
					fullCalls.Add(1)
					if e.ToolParent == nil || e.ToolExecution.ItemID == "" {
						t.Error("nested identity absent")
					}
					if mode == "result-failure" {
						return tool.Result{}, errors.New("fixture redaction failed")
					}
					o.Result.Content = []tool.ContentBlock{tool.StructuredContent{Value: accepted}}
					if mode == "invalid-output-schema" {
						wrong := json.RawMessage(`{"value":42}`)
						o.Result.Content = []tool.ContentBlock{tool.StructuredContent{Value: wrong}}
					}
					if mode == "invalid-structured" {
						o.Result.Content = append(o.Result.Content, tool.StructuredContent{Value: accepted})
					}
					if mode == "forged-completion" {
						o.Result.Content = []tool.ContentBlock{tool.StructuredContent{Value: []byte("invalid")}}
					}
					return o.Result, nil
				}}},
				ModelView: []hook.ModelView{{Name: "summary", Apply: func(_ context.Context, e hook.ToolContext, m model.Message) (model.Message, error) {
					if e.Tool.Name != "ReadFact" {
						return m, nil
					}
					viewCalls.Add(1)
					if e.ToolDefinition.OutputSchema == nil {
						t.Error("nested projection lost its actual definition")
					}
					m.Parts[0].ToolResult.Content = []model.Part{model.NewTextPart("summary")}
					if mode == "view-failure" {
						m.Parts[0].ToolResult.Name = "forged"
					}
					return m, nil
				}}},
			}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: "{\"code\":\"const b=await tools.call('ReadFact',{});if(b.data.value!=='accepted')throw Error('wrong Body');return b.data;\"}"}}
				}
				return nil
			}}
			rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{}, Hooks: hooks})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "start", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{probe, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}})
			if err != nil {
				t.Fatal(err)
			}
			completionErr := run.Handle.WaitCompletion(t.Context())
			if mode == "accepted" && completionErr != nil {
				t.Fatal(completionErr)
			}
			live, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")}).LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(live.Events, reopened.Events) {
				t.Fatal("complete facts changed on File reopen")
			}
			pairs, err := session.PairToolFacts(reopened.Events)
			if err != nil {
				t.Fatal(err)
			}
			var child, outer *session.Event
			for _, p := range pairs {
				switch p.Call.Tool.Name {
				case "ReadFact":
					child = p.Result
				case codemode.ToolName:
					outer = p.Result
				}
			}
			if child == nil || outer == nil || effects.Load() != 1 || fullCalls.Load() != 1 || viewCalls.Load() != 1 {
				t.Fatalf("facts child=%v outer=%v effects=%d full=%d view=%d completion=%v", child, outer, effects.Load(), fullCalls.Load(), viewCalls.Load(), completionErr)
			}
			if child.Tool.Outcome != session.ToolOutcomeSucceeded {
				t.Fatalf("presentation failure changed known effect outcome: %+v", child.Tool)
			}
			if mode == "accepted" {
				structured, _ := child.Tool.Result.Structured()
				if string(structured) != string(accepted) || child.Message.ToolResults()[0].Content[0].Text.Text != "summary" || !strings.Contains(string(mustMarshalHook(t, outer.Tool.Result)), "accepted") {
					t.Fatal("JS Body, full result and model view conflated")
				}
			} else {
				if child.Tool.ProcessingFailure == nil || child.Tool.ProcessingFailure.Name == "" {
					t.Fatal("typed processing failure absent")
				}
				if mode == "view-failure" {
					reply := child.Message.ToolResults()
					if len(reply) != 1 || reply[0].Name != child.Tool.Name || reply[0].ToolUseID != child.Tool.ID ||
						len(reply[0].Content) != 1 || reply[0].Content[0].Text == nil || !strings.Contains(reply[0].Content[0].Text.Text, "model projection failed") {
						t.Fatal("failed projection did not retain the child identity and safe reply")
					}
				}
				if mode != "view-failure" && strings.Contains(string(mustMarshalHook(t, reopened.Events)), "secret") {
					t.Fatal("failed result processing leaked original secret")
				}
			}
		})
	}
}

func mustMarshalHook(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type projectionArtifactRecorder struct {
	results []tool.Result
}

func (s *projectionArtifactRecorder) Save(_ context.Context, result tool.Result) (string, error) {
	stored, err := tool.CloneResult(result, nil)
	if err == nil {
		s.results = append(s.results, stored)
	}
	return "artifact://outer", err
}

func TestToolProjectionBoundsCodeModeViewsAndSavesOnlyOuterResult(t *testing.T) {
	store, active, root := codeModeStore(t)
	text := strings.Repeat("payload", 3000)
	probe := tool.NamedTool{Def: tool.Definition{Name: "lookup", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{Content: []tool.ContentBlock{tool.Text(text)}}, nil
	}}
	driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: `{"code":"const b = await tools.call('lookup', {}); if (b.text.length !== 21000) throw Error('incomplete body'); return b.text;"}`}}
		}
		return nil
	}}
	artifacts := &projectionArtifactRecorder{}
	rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolProjection: tool.ResultProjection{Budget: tool.TruncationPolicy{MaxTokens: 256}, Artifacts: artifacts}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "start", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{probe, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	loaded, err := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")}).LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	var child, outer *session.Event
	for _, ev := range loaded.Events {
		if ev.Tool == nil || ev.Type != session.EventTypeToolResult {
			continue
		}
		switch ev.Tool.Name {
		case "lookup":
			child = ev
		case codemode.ToolName:
			outer = ev
		}
	}
	if child == nil || outer == nil || child.Tool.Result == nil || outer.Tool.Result == nil {
		t.Fatal("missing committed tool results")
	}
	if string(child.Tool.Result.Content[0].(tool.Text)) != text || outer.Tool.Outcome != session.ToolOutcomeSucceeded {
		t.Fatalf("incomplete child result or failed script: %s", outer.Tool.Error)
	}
	childView := string(mustMarshalHook(t, child.Message))
	outerView := string(mustMarshalHook(t, outer.Message))
	if len(childView) >= len(text) || strings.Contains(childView, "artifact://outer") || !strings.Contains(outerView, "artifact://outer") || len(outerView) >= len(text) || len(artifacts.results) != 1 || !reflect.DeepEqual(artifacts.results[0], *outer.Tool.Result) {
		t.Fatalf("projected bytes child=%d outer=%d saved=%d", len(childView), len(outerView), len(artifacts.results))
	}
}
