package session_test

import (
	"context"
	"iter"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/sdkctx"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestFileToolRunConformance(t *testing.T) {
	t.Parallel()
	cfg := sessionfile.Config{RootDir: t.TempDir()}
	active, err := sdkctx.UseLogicalFileStore(t, sessionfile.NewStore(cfg)).StartSession(t.Context(), session.StartSessionRequest{AppName: "conformance", UserID: "host"})
	if err != nil {
		t.Fatal(err)
	}
	toolRunConformance(t, active.SessionRef, func(t *testing.T) session.Service {
		store := sdkctx.UseLogicalFileStore(t, sessionfile.NewStore(cfg))
		return session.NewService(store)
	})
}

func TestFileTurnAdmissionConformance(t *testing.T) {
	t.Parallel()
	cfg := sessionfile.Config{RootDir: t.TempDir()}
	writer := sdkctx.UseLogicalFileStore(t, sessionfile.NewStore(cfg))
	active, err := writer.StartSession(t.Context(), session.StartSessionRequest{AppName: "conformance", UserID: "host"})
	if err != nil {
		t.Fatal(err)
	}
	turnAdmissionConformance(t, active.SessionRef, session.NewService(writer), session.NewService(sdkctx.UseLogicalFileStore(t, sessionfile.NewStore(cfg))))
}

func toolRunConformance(t *testing.T, ref session.SessionRef, open func(*testing.T) session.Service) {
	t.Helper()
	effects := 0
	lookup := tool.NamedTool{
		Def: tool.Definition{Name: "lookup", InputSchema: map[string]any{"type": "object"}},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			effects++
			if call.Name != "lookup" || call.ID != "lookup-1" || string(call.Input) != `{"key":"one"}` {
				t.Errorf("unexpected tool call: %#v", call)
			}
			return tool.Result{Content: []tool.ContentBlock{tool.Text("stored value")}}, nil
		},
	}
	run := func(input string, probe *toolRunModel) session.Service {
		t.Helper()
		store := open(t)
		core, err := runtime.New(runtime.Config{Sessions: store, AgentFactory: chat.Factory{}})
		if err != nil {
			t.Fatal(err)
		}
		turn, err := core.Run(t.Context(), agent.RunRequest{
			SessionRef: ref, Input: input,
			AgentSpec: agent.AgentSpec{Model: probe, Tools: []tool.Tool{lookup}},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer turn.Handle.Close()
		if err := turn.Handle.WaitCompletion(t.Context()); err != nil {
			t.Fatal(err)
		}
		return store
	}

	first := &toolRunModel{callTool: true}
	run("look up a value", first)
	if effects != 1 || len(first.requests) != 2 {
		t.Fatalf("first turn executed %d tools and made %d model requests", effects, len(first.requests))
	}
	live := first.requests[1]
	if len(live) != 3 || live[0].Role != model.RoleUser || live[0].TextContent() != "look up a value" || len(live[1].ToolCalls()) != 1 {
		t.Fatalf("expected the original input, tool call and reply: %#v", live)
	}
	last := live[2].ToolResults()
	if len(last) != 1 || last[0].Name != "lookup" || last[0].ToolUseID != "lookup-1" || last[0].IsError ||
		len(last[0].Content) != 1 || last[0].Content[0].Text == nil || last[0].Content[0].Text.Text != "stored value" {
		t.Fatalf("unexpected model-visible result: %#v", last)
	}
	second := &toolRunModel{}
	store := run("continue", second)
	want := append(model.CloneMessages(live), model.NewTextMessage(model.RoleAssistant, "done"), model.NewTextMessage(model.RoleUser, "continue"))
	if effects != 1 || len(second.requests) != 1 || !reflect.DeepEqual(second.requests[0], want) {
		t.Fatalf("reopened turn executed %d total effects and saw %#v; want %#v", effects, second.requests, want)
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	var seq uint64
	for _, e := range events {
		if e == nil || e.Seq <= seq {
			t.Fatalf("event sequence did not advance after %d", seq)
		}
		seq = e.Seq
	}
	pairs, err := session.PairToolFacts(events)
	if err != nil || len(pairs) != 1 || pairs[0].Result == nil || pairs[0].Result.Tool.Result == nil ||
		!reflect.DeepEqual(pairs[0].Result.Tool.Result.Content, []tool.ContentBlock{tool.Text("stored value")}) {
		t.Fatalf("unexpected persisted tool facts: %#v, %v", pairs, err)
	}
}

func turnAdmissionConformance(t *testing.T, ref session.SessionRef, writer, reader session.Service) {
	t.Helper()
	newRuntime := func(s session.Service) *runtime.Runtime {
		t.Helper()
		rt, err := runtime.New(runtime.Config{Sessions: s, AgentFactory: chat.Factory{}})
		if err != nil {
			t.Fatal(err)
		}
		return rt
	}
	first, second := newRuntime(writer), newRuntime(reader)
	entered, release := make(chan struct{}), make(chan struct{})
	lookup := tool.NamedTool{
		Def: tool.Definition{Name: "lookup", InputSchema: map[string]any{"type": "object"}},
		Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
			close(entered)
			select {
			case <-release:
				return tool.Result{Content: []tool.ContentBlock{tool.Text("value")}}, nil
			case <-ctx.Done():
				return tool.Result{}, ctx.Err()
			}
		},
	}
	active, err := first.Run(t.Context(), agent.RunRequest{SessionRef: ref, Input: "first", AgentSpec: agent.AgentSpec{
		Model: &toolRunModel{callTool: true}, Tools: []tool.Tool{lookup},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer active.Handle.Close()
	completed := make(chan error, 1)
	go func() { completed <- active.Handle.WaitCompletion(t.Context()) }()
	select {
	case <-entered:
	case err := <-completed:
		t.Fatalf("first tool was never entered: %v", err)
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	read := func() session.LoadedSession {
		t.Helper()
		s, err := reader.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: ref, IncludeTransient: true})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := read()
	pairs, err := session.PairToolFacts(before.Events)
	if err != nil || len(pairs) != 1 || pairs[0].Result != nil {
		t.Fatalf("in-flight tool facts: %v, %v", pairs, err)
	}
	runNext := func(input string) (agent.RunResult, error) {
		return second.Run(t.Context(), agent.RunRequest{SessionRef: ref, Input: input, AgentSpec: agent.AgentSpec{Model: &toolRunModel{}}})
	}
	if competing, err := runNext("competing prompt"); err == nil {
		competing.Handle.Cancel()
		t.Fatal("second Runtime accepted a concurrent prompt")
	} else if !errorcode.Is(err, errorcode.Conflict) {
		t.Fatalf("expected an admission conflict, got %v", err)
	}
	after := read()
	if before.Session.Revision != after.Session.Revision || len(before.Events) != len(after.Events) {
		t.Fatal("rejected prompt changed durable history")
	}
	close(release)
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	next, err := runNext("next")
	if err != nil {
		t.Fatal(err)
	}
	defer next.Handle.Close()
	if err := next.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type toolRunModel struct {
	callTool bool
	requests [][]model.Message
}

func (*toolRunModel) Name() string                     { return "session-conformance" }
func (*toolRunModel) Capabilities() model.Capabilities { return model.Capabilities{ToolCalls: true} }
func (m *toolRunModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.requests = append(m.requests, model.CloneMessages(req.Messages))
		message := model.NewTextMessage(model.RoleAssistant, "done")
		if m.callTool && len(m.requests) == 1 {
			message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "lookup-1", Name: "lookup", Args: `{"key":"one"}`}}, "")
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: message, StepComplete: true, TurnComplete: true}), nil)
	}
}
