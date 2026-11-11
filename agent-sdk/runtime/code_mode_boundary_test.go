package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestCodeModeSourceFileChangesOnNextRuntimeExecution(t *testing.T) {
	store, active, root := codeModeStore(t)
	sandbox, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close()
	var sources []codemode.Source
	declaration := &codemode.Tool{Name: "js_repl", Sandbox: sandbox, Executor: codeExecutorFunc(func(_ context.Context, source codemode.Source, _ codemode.Host) (json.RawMessage, error) {
		sources = append(sources, source)
		return json.Marshal(map[string]string{"source": source.Code})
	})}
	llm := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i > 1 {
			return nil
		}
		value := "first"
		if i == 1 {
			value = "changed"
		}
		if err := os.WriteFile(filepath.Join(root, "task.js"), []byte(value), 0600); err != nil {
			t.Error(err)
			return nil
		}
		return []model.ToolCall{{ID: "script", Name: "js_repl", Args: `{"file":"task.js"}`}}
	}}
	runCodeMode(t, store, active, agent.AgentSpec{Model: llm, Tools: []tool.Tool{declaration}}, nil, nil)
	if len(sources) != 2 || sources[0].Code != "first" || sources[1].Code != "changed" || sources[0].SHA256 == sources[1].SHA256 {
		t.Fatalf("sources=%+v", sources)
	}
}

func TestCodeModeRepeatedProviderIDsKeepDistinctChildReceipts(t *testing.T) {
	store, active, _ := codeModeStore(t)
	var effects atomic.Int32
	target := tool.NamedTool{Def: tool.Definition{Name: "Read", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		effects.Add(1)
		return tool.Result{Content: []model.Part{model.NewTextPart("ok")}}, nil
	}}
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		_, err := host.Call(ctx, "Read", []byte(`{}`))
		return []byte(`{}`), err
	})}
	for range 2 {
		runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{declaration, target}}, nil, nil)
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	children, terminals := map[string]bool{}, map[string]bool{}
	for _, event := range events {
		if session.IsMirror(event) && event.Tool != nil && event.Tool.Parent != nil {
			children[event.Tool.ID] = true
		}
		if event.Journal != nil && event.Journal.ToolExecution != nil {
			r := event.Journal.ToolExecution
			if r.Parent != nil && r.Status == session.ToolExecutionSucceeded {
				terminals[r.Identity] = true
			}
		}
	}
	if effects.Load() != 2 || len(children) != 2 || len(terminals) != 2 {
		t.Fatalf("effects=%d child IDs=%d terminal identities=%d", effects.Load(), len(children), len(terminals))
	}
}

func TestCodeModeTurnCompleteCannotBeSuppressedByExecutor(t *testing.T) {
	store, active, _ := codeModeStore(t)
	var effects atomic.Int32
	stop := tool.NamedTool{Def: tool.Definition{Name: "Complete", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{TurnComplete: true, Content: []model.Part{model.NewTextPart("complete")}}, nil
	}}
	write := tool.NamedTool{Def: tool.Definition{Name: "Write", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { effects.Add(1); return tool.Result{}, nil }}
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		reply, err := host.Call(ctx, "Complete", []byte(`{}`))
		if err != nil || !reply.TurnComplete {
			return nil, errors.New("trusted completion lost")
		}
		if _, err := host.Call(ctx, "Write", []byte(`{}`)); err == nil {
			return nil, errors.New("effect ran after completion")
		}
		return nil, errors.New("executor ignored completion")
	})}
	llm := codeModeModel(false)
	runCodeMode(t, store, active, agent.AgentSpec{Model: llm, Tools: []tool.Tool{declaration, stop, write}}, nil, nil)
	if effects.Load() != 0 || len(llm.requests) != 1 {
		t.Fatalf("effects=%d model calls=%d", effects.Load(), len(llm.requests))
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Tool != nil && event.Tool.Name == "js_repl" && event.Tool.TurnComplete {
			found = true
		}
	}
	if !found {
		t.Fatal("trusted completion lost during durable event cloning")
	}
}

func TestCodeModeBoundaryBudgetsAndParallelSafety(t *testing.T) {
	for _, safe := range []bool{false, true} {
		name := "serial"
		if safe {
			name = "parallel-safe"
		}
		t.Run(name, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			started, release := make(chan struct{}, 2), make(chan struct{})
			var effects atomic.Int32
			target := tool.NamedTool{Def: tool.Definition{Name: "Read", InputSchema: map[string]any{"type": "object"}, Capabilities: tool.Capabilities{ParallelSafe: safe}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				effects.Add(1)
				started <- struct{}{}
				<-release
				return tool.Result{Content: []model.Part{model.NewTextPart("ok")}}, nil
			}}
			declaration := &codemode.Tool{Name: "js_repl", Limits: codemode.Limits{Calls: 3, ArgumentBytes: 32, ReplyBytes: 2048}, Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
				if _, err := host.Call(ctx, "Read", []byte(`{"large":"`+strings.Repeat("x", 32)+`"}`)); err == nil {
					return nil, errors.New("argument budget ignored")
				}
				first := make(chan error, 1)
				go func() { _, err := host.Call(ctx, "Read", []byte(`{}`)); first <- err }()
				<-started
				if safe {
					second := make(chan error, 1)
					go func() { _, err := host.Call(ctx, "Read", []byte(`{}`)); second <- err }()
					<-started
					close(release)
					if err := <-second; err != nil {
						return nil, err
					}
				} else {
					if _, err := host.Call(ctx, "Read", []byte(`{}`)); err == nil {
						close(release)
						return nil, errors.New("unsafe calls ran concurrently")
					}
					close(release)
				}
				if err := <-first; err != nil {
					return nil, err
				}
				if safe {
					if _, err := host.Call(ctx, "Read", []byte(`{}`)); err == nil {
						return nil, errors.New("call budget ignored")
					}
				}
				return []byte(`{"ok":true}`), nil
			})}
			runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{declaration, target}}, nil, nil)
			want := int32(1)
			if safe {
				want = 2
			}
			if effects.Load() != want {
				t.Fatalf("effects=%d want=%d", effects.Load(), want)
			}
		})
	}
	store, active, _ := codeModeStore(t)
	large := tool.NamedTool{Def: tool.Definition{Name: "Large", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{Content: []model.Part{model.NewTextPart(strings.Repeat("x", 1024))}}, nil
	}}
	mixed := tool.NamedTool{Def: tool.Definition{Name: "Mixed", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{Content: []model.Part{{Kind: model.PartKindJSON, JSON: &model.JSONPart{Value: []byte(`{}`)}, Text: &model.TextPart{Text: strings.Repeat("x", 1024)}}}}, nil
	}}
	var checked atomic.Bool
	declaration := &codemode.Tool{Name: "js_repl", Limits: codemode.Limits{ReplyBytes: 2048}, Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		reply, err := host.Call(ctx, "Large", []byte(`{}`))
		if err == nil || len(reply.Parts) != 0 {
			return nil, errors.New("oversized result crossed script transfer boundary")
		}
		reply, err = host.Call(ctx, "Mixed", []byte(`{}`))
		if err != nil || len(reply.Parts) != 1 || len(reply.Parts[0].Text) != 0 || string(reply.Parts[0].JSON) != `{}` {
			return nil, errors.New("part projection and budget used different discriminants")
		}
		checked.Store(true)
		return []byte(`{}`), nil
	})}
	runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{declaration, large, mixed}}, nil, nil)
	if !checked.Load() {
		t.Fatal("projection limit was not checked")
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Journal != nil && event.Journal.ToolExecution != nil && event.Journal.ToolExecution.Parent != nil && strings.Contains(string(event.Journal.ToolExecution.Result), strings.Repeat("x", 1024)) {
			found = true
		}
	}
	if !found {
		t.Fatal("projection budgeting discarded full durable result")
	}
}
