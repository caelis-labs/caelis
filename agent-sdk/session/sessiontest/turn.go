package sessiontest

import (
	"context"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// TurnAdmissionConformance runs two independent runtimes over the same Session.
// The second can read committed tool calls during execution and start a prompt
// after completion. A conflicting prompt must fail before recording any input.
func TurnAdmissionConformance(t *testing.T, ref session.SessionRef, writer, reader session.Service) {
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
