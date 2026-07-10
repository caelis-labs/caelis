package runtime

import (
	"context"
	"errors"
	"iter"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestRunLimitsStopBeforeExcessModelCall(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	llm := limitTestModel{generate: func(context.Context, *model.Request) *model.Response {
		n := calls.Add(1)
		if n == 1 {
			return limitToolResponse("call-1", "probe")
		}
		return limitTextResponse("unexpected", model.Usage{})
	}}
	probe := tool.NamedTool{Def: tool.Definition{Name: "probe"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{Name: "probe", Content: []model.Part{model.NewTextPart("ok")}}, nil
	}}
	err := runLimitedChat(t, llm, []tool.Tool{probe}, agent.RunLimits{MaxModelCalls: 1})
	assertRunLimit(t, err, agent.RunLimitModelCalls, 1, 1)
	if got := calls.Load(); got != 1 {
		t.Fatalf("model calls = %d, want 1", got)
	}
}

func TestRunLimitsStopBeforeExcessCompletedTurn(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	llm := limitTestModel{generate: func(context.Context, *model.Request) *model.Response {
		n := calls.Add(1)
		if n == 1 {
			return limitToolResponse("call-1", "probe")
		}
		return limitTextResponse("unexpected", model.Usage{})
	}}
	probe := tool.NamedTool{Def: tool.Definition{Name: "probe"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{Name: "probe", Content: []model.Part{model.NewTextPart("ok")}}, nil
	}}
	err := runLimitedChat(t, llm, []tool.Tool{probe}, agent.RunLimits{MaxTurns: 1})
	assertRunLimit(t, err, agent.RunLimitTurns, 1, 1)
	if got := calls.Load(); got != 1 {
		t.Fatalf("model calls = %d, want 1", got)
	}
}

func TestRunLimitsRejectUninstrumentedAgentBudgets(t *testing.T) {
	t.Parallel()

	sessions, active := newTestSessionService(t, "limit-uninstrumented")
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef,
		Agent:      &limitBlockingAgent{started: make(chan struct{})},
		Limits:     agent.RunLimits{MaxModelCalls: 1},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want undeclared capability rejection")
	}
}

func TestRunLimitsReserveParallelToolCallsAtomically(t *testing.T) {
	t.Parallel()

	var toolCalls atomic.Int64
	llm := limitTestModel{generate: func(context.Context, *model.Request) *model.Response {
		return &model.Response{
			Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{
				{ID: "call-1", Name: "RUN_COMMAND", Args: `{}`},
				{ID: "call-2", Name: "RUN_COMMAND", Args: `{}`},
			}, ""),
			TurnComplete: true,
		}
	}}
	runCommand := tool.NamedTool{Def: tool.Definition{Name: "RUN_COMMAND", Capabilities: tool.Capabilities{ParallelSafe: true}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		toolCalls.Add(1)
		return tool.Result{Name: "RUN_COMMAND"}, nil
	}}
	err := runLimitedChat(t, llm, []tool.Tool{runCommand}, agent.RunLimits{MaxToolCalls: 1})
	assertRunLimit(t, err, agent.RunLimitToolCalls, 1, 1)
	if got := toolCalls.Load(); got > 1 {
		t.Fatalf("tool calls = %d, want at most one admitted call", got)
	}
}

func TestRunLimitsStopAfterReportedTokenAndCostUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		limits agent.RunLimits
		kind   agent.RunLimitKind
		limit  int64
		used   int64
	}{
		{name: "tokens", limits: agent.RunLimits{MaxTokens: 9}, kind: agent.RunLimitTokens, limit: 9, used: 10},
		{name: "cost", limits: agent.RunLimits{MaxCostMicros: 49}, kind: agent.RunLimitCost, limit: 49, used: 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			llm := limitTestModel{generate: func(context.Context, *model.Request) *model.Response {
				return limitTextResponse("over", model.Usage{TotalTokens: 10, CostMicros: 50})
			}}
			err := runLimitedChat(t, llm, nil, tt.limits)
			assertRunLimit(t, err, tt.kind, tt.limit, tt.used)
		})
	}
}

func TestRunLimitsCancelAtWallTimeWithTypedError(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	agentImpl := &limitBlockingAgent{started: started}
	sessions, active := newTestSessionService(t, "limit-wall-time")
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	run, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef,
		Agent:      agentImpl,
		Limits:     agent.RunLimits{MaxWallTime: 30 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("agent did not start")
	}
	err = runnerError(run.Handle)
	var limitErr *agent.RunLimitError
	if !errors.As(err, &limitErr) || limitErr.Kind != agent.RunLimitWallTime {
		t.Fatalf("runner error = %v, want wall-time RunLimitError", err)
	}
}

func runLimitedChat(t *testing.T, llm model.LLM, tools []tool.Tool, limits agent.RunLimits) error {
	t.Helper()
	sessions, active := newTestSessionService(t, "run-limits-"+t.Name())
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	run, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef,
		AgentSpec:  agent.AgentSpec{Name: "limited", Model: llm, Tools: tools},
		Limits:     limits,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	return runnerError(run.Handle)
}

func runnerError(handle agent.Runner) error {
	var out error
	for _, err := range handle.Events() {
		if err != nil {
			out = err
		}
	}
	return out
}

func assertRunLimit(t *testing.T, err error, kind agent.RunLimitKind, limit, used int64) {
	t.Helper()
	var limitErr *agent.RunLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("error = %v, want *agentsdk.RunLimitError", err)
	}
	if got := *limitErr; got != (agent.RunLimitError{Kind: kind, Limit: limit, Used: used}) {
		t.Fatalf("RunLimitError = %+v, want kind=%q limit=%d used=%d", got, kind, limit, used)
	}
}

type limitTestModel struct {
	generate func(context.Context, *model.Request) *model.Response
}

func (limitTestModel) Name() string { return "limit-test" }

func (limitTestModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true, ParallelToolCalls: true}
}

func (m limitTestModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(nil, err)
			return
		}
		yield(model.StreamEventFromResponse(m.generate(ctx, req)), nil)
	}
}

func limitToolResponse(id, name string) *model.Response {
	return &model.Response{
		Message:      model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: id, Name: name, Args: `{}`}}, ""),
		TurnComplete: true,
	}
}

func limitTextResponse(text string, usage model.Usage) *model.Response {
	return &model.Response{
		Message:      model.NewTextMessage(model.RoleAssistant, text),
		TurnComplete: true,
		Usage:        usage,
	}
}

type limitBlockingAgent struct {
	started chan struct{}
}

func (a *limitBlockingAgent) Name() string { return "limit-blocking" }

func (a *limitBlockingAgent) Run(ctx agent.Context) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		close(a.started)
		<-ctx.Done()
		yield(nil, ctx.Err())
	}
}
