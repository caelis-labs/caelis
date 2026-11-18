package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
)

func TestGenerationWatchdogInterruptsRepeatedRawModelToolStep(t *testing.T) {
	t.Parallel()

	watchdog := newGenerationWatchdog(50, 3, 8)
	for step := 1; step <= 3; step++ {
		watchdog.beginModelStep()
		err := watchdog.finishModelStep(toolStepResponse("call-"+fmt.Sprint(step), "Grep", `{"query":"same"}`))
		if step < 3 && err != nil {
			t.Fatalf("step %d error = %v", step, err)
		}
		if step == 3 {
			var loopErr *GenerationLoopError
			if !errors.As(err, &loopErr) || loopErr.Reason != GenerationToolLoop || loopErr.Streak != 3 {
				t.Fatalf("step %d error = %#v, want tool-loop streak 3", step, err)
			}
		}
	}
}

func TestGenerationWatchdogTreatsDifferentRawModelArgsAsProgress(t *testing.T) {
	t.Parallel()

	watchdog := newGenerationWatchdog(50, 3, 8)
	for step := 1; step <= 12; step++ {
		watchdog.beginModelStep()
		err := watchdog.finishModelStep(toolStepResponse(
			"call-"+fmt.Sprint(step),
			"Grep",
			fmt.Sprintf(`{"query":"query-%d"}`, step),
		))
		if err != nil {
			t.Fatalf("step %d error = %v, want distinct model args treated as progress", step, err)
		}
	}
}

func TestGenerationWatchdogTreatsExactDistinctToolNamesAsProgress(t *testing.T) {
	t.Parallel()

	watchdog := newGenerationWatchdog(50, 2, 8)
	for step, name := range []string{"Search", "SEARCH", "Search", "SEARCH"} {
		watchdog.beginModelStep()
		if err := watchdog.finishModelStep(toolStepResponse(
			fmt.Sprintf("call-%d", step), name, `{"query":"same"}`,
		)); err != nil {
			t.Fatalf("step %d (%s) error = %v, want exact-distinct name to reset loop evidence", step, name, err)
		}
	}
}

func TestGenerationWatchdogRepeatedTaskWaitIsObservation(t *testing.T) {
	t.Parallel()

	watchdog := newGenerationWatchdog(50, 3, 8)
	for step := 1; step <= 12; step++ {
		watchdog.beginModelStep()
		err := watchdog.finishModelStep(toolStepResponse(
			"wait-"+fmt.Sprint(step),
			tasktool.ToolName,
			`{"action":"wait","task_id":"command-1"}`,
		))
		if err != nil {
			t.Fatalf("step %d error = %v, want Task wait excluded", step, err)
		}
	}
}

func TestGenerationWatchdogMixedTaskWaitStepRemainsLoopEvidence(t *testing.T) {
	t.Parallel()

	watchdog := newGenerationWatchdog(50, 2, 8)
	for step := 1; step <= 2; step++ {
		watchdog.beginModelStep()
		err := watchdog.finishModelStep(&model.Response{
			Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{
				{ID: fmt.Sprintf("wait-%d", step), Name: tasktool.ToolName, Args: `{"action":"wait","task_id":"command-1"}`},
				{ID: fmt.Sprintf("write-%d", step), Name: "Write", Args: `{"path":"result.txt","content":"same"}`},
			}, ""),
			TurnComplete: true,
			StepComplete: true,
			Status:       model.ResponseStatusCompleted,
			FinishReason: model.FinishReasonToolCalls,
		})
		if step == 1 && err != nil {
			t.Fatalf("first mixed step error = %v", err)
		}
		if step == 2 {
			var loopErr *GenerationLoopError
			if !errors.As(err, &loopErr) || loopErr.Reason != GenerationToolLoop {
				t.Fatalf("second mixed step error = %#v, want non-wait call to remain loop evidence", err)
			}
		}
	}
}

func TestGenerationWatchdogRepeatedTaskReadRemainsLoopEvidence(t *testing.T) {
	t.Parallel()

	watchdog := newGenerationWatchdog(50, 3, 8)
	var err error
	for step := 1; step <= 3; step++ {
		watchdog.beginModelStep()
		err = watchdog.finishModelStep(toolStepResponse(
			"read-"+fmt.Sprint(step),
			tasktool.ToolName,
			`{"action":"read","task_id":"command-1"}`,
		))
	}
	var loopErr *GenerationLoopError
	if !errors.As(err, &loopErr) || loopErr.Reason != GenerationToolLoop {
		t.Fatalf("final error = %#v, want repeated immediate Task read counted", err)
	}
}

func TestGenerationWatchdogToolEvidenceDoesNotCrossNoToolStep(t *testing.T) {
	t.Parallel()

	watchdog := newGenerationWatchdog(50, 2, 8)
	watchdog.beginModelStep()
	if err := watchdog.finishModelStep(toolStepResponse("before", "Grep", `{"query":"same"}`)); err != nil {
		t.Fatal(err)
	}
	watchdog.beginModelStep()
	if err := watchdog.finishModelStep(&model.Response{
		Message:      model.NewTextMessage(model.RoleAssistant, "completed without a tool"),
		TurnComplete: true,
		StepComplete: true,
		Status:       model.ResponseStatusCompleted,
		FinishReason: model.FinishReasonStop,
	}); err != nil {
		t.Fatal(err)
	}
	watchdog.beginModelStep()
	if err := watchdog.finishModelStep(toolStepResponse("after", "Grep", `{"query":"same"}`)); err != nil {
		t.Fatalf("tool step after a no-tool boundary error = %v, want a new evidence window", err)
	}
}

func TestChatAgentGenerationWatchdogTaskReadDuringApproval(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		state     task.State
		untrusted bool
		failed    bool
		mixed     bool
		wantLoop  bool
	}{
		{name: "approval pending", state: task.StateWaitingApproval},
		{name: "running", state: task.StateRunning, wantLoop: true},
		{name: "completed", state: task.StateCompleted, wantLoop: true},
		{name: "untrusted approval", state: task.StateWaitingApproval, untrusted: true, wantLoop: true},
		{name: "failed read", state: task.StateWaitingApproval, failed: true, wantLoop: true},
		{name: "mixed with cancel", state: task.StateWaitingApproval, mixed: true, wantLoop: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := []model.ToolCall{{ID: "read", Name: tasktool.ToolName, Args: `{"action":"read","handle":"command-665"}`}}
			if test.mixed {
				calls = append(calls, model.ToolCall{ID: "cancel", Name: tasktool.ToolName, Args: `{"action":"cancel","handle":"command-123"}`})
			}
			testModel := &longToolLoopModel{toolCalls: calls}
			readCount := 0
			taskTool := tool.NamedTool{
				Def: tool.Definition{Name: tasktool.ToolName, InputSchema: map[string]any{"type": "object"}},
				Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
					if call.ID == "read" {
						readCount++
					}
					return tool.Result{
						Content: []model.Part{model.NewJSONPart([]byte(fmt.Sprintf(`{"state":%q}`, test.state)))},
						IsError: test.failed,
						Metadata: mergeEventMeta(runtimeTaskResultSourceMeta(true), map[string]any{
							"caelis": map[string]any{"runtime": map[string]any{
								"task": map[string]any{"state": string(test.state)},
							}},
						}),
					}, nil
				},
			}
			var selected tool.Tool = trustedTaskResultTool{NamedTool: taskTool}
			if test.untrusted {
				selected = taskTool
			}
			chatAgent, err := NewWithTools("chat", testModel, []tool.Tool{selected}, "")
			if err != nil {
				t.Fatal(err)
			}
			ctx := agent.NewContext(agent.ContextSpec{
				Context: context.Background(),
				Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-task-read-approval"}},
				Events: []*session.Event{{
					Type: session.EventTypeUser, Message: ptrMessage(model.NewTextMessage(model.RoleUser, "observe the command")),
				}},
			})
			var runErr error
			var final *session.Event
			for event, eventErr := range chatAgent.Run(ctx) {
				final, runErr = event, eventErr
			}
			if test.wantLoop {
				var loopErr *GenerationLoopError
				if !errors.As(runErr, &loopErr) || loopErr.Reason != GenerationToolLoop || loopErr.Streak != defaultToolLoopStreak {
					t.Fatalf("Run() error = %v, want tool loop at threshold", runErr)
				}
				if readCount != defaultToolLoopStreak-1 {
					t.Fatalf("dispatched %d reads, want %d", readCount, defaultToolLoopStreak-1)
				}
				return
			}
			if runErr != nil || final == nil || strings.TrimSpace(final.Text) != "done" || readCount <= defaultToolLoopStreak {
				t.Fatalf("Run() dispatched %d reads; final = %#v, error = %v; want a completed turn beyond the threshold", readCount, final, runErr)
			}
		})
	}
}

func TestGenerationWatchdogUsesRawStreamTextAndAttemptReset(t *testing.T) {
	t.Parallel()

	const cycle = "repeat this exact reasoning"
	watchdog := newGenerationWatchdog(3, 50, 8)
	watchdog.beginModelStep()
	for repeat := 0; repeat < 2; repeat++ {
		if err := watchdog.observeStreamEvent(textDelta(cycle)); err != nil {
			t.Fatalf("pre-reset repeat %d error = %v", repeat, err)
		}
	}
	if err := watchdog.observeStreamEvent(&model.StreamEvent{Type: model.StreamEventAttemptReset}); err != nil {
		t.Fatal(err)
	}
	for repeat := 0; repeat < 2; repeat++ {
		if err := watchdog.observeStreamEvent(textDelta(cycle)); err != nil {
			t.Fatalf("post-reset repeat %d error = %v", repeat, err)
		}
	}
	err := watchdog.observeStreamEvent(textDelta(cycle))
	var loopErr *GenerationLoopError
	if !errors.As(err, &loopErr) || loopErr.Reason != GenerationTextLoop {
		t.Fatalf("final error = %#v, want raw stream text loop", err)
	}
}

func TestGenerationWatchdogDoesNotRecountStreamedCumulativeFinal(t *testing.T) {
	t.Parallel()

	const cycle = "repeat this exact streamed text"
	watchdog := newGenerationWatchdog(2, 50, 8)
	watchdog.beginModelStep()
	if err := watchdog.observeStreamEvent(textDelta(cycle)); err != nil {
		t.Fatal(err)
	}
	if err := watchdog.finishModelStep(&model.Response{
		Message:      model.NewTextMessage(model.RoleAssistant, cycle),
		TurnComplete: true,
	}); err != nil {
		t.Fatalf("finishModelStep() error = %v, cumulative final must not duplicate its streamed delta", err)
	}
}

func TestGenerationWatchdogUsesUnstreamedFinalContentForToolProgress(t *testing.T) {
	t.Parallel()

	const reasoning = "same streamed reasoning prefix"
	watchdog := newGenerationWatchdog(50, 2, 8)
	for step, finalText := range []string{"first final answer", "different final answer"} {
		watchdog.beginModelStep()
		if err := watchdog.observeStreamEvent(textDelta(reasoning)); err != nil {
			t.Fatalf("step %d stream error = %v", step+1, err)
		}
		err := watchdog.finishModelStep(&model.Response{
			Message: model.MessageFromAssistantParts(finalText, reasoning, []model.ToolCall{{
				ID: fmt.Sprintf("call-%d", step+1), Name: "Grep", Args: `{"query":"same"}`,
			}}),
			TurnComplete: true,
			StepComplete: true,
			Status:       model.ResponseStatusCompleted,
			FinishReason: model.FinishReasonToolCalls,
		})
		if err != nil {
			t.Fatalf("step %d error = %v, unstreamed final content is progress", step+1, err)
		}
	}
}

func TestGenerationWatchdogIncompleteToolArgsFailOpenAndResetEvidence(t *testing.T) {
	t.Parallel()

	watchdog := newGenerationWatchdog(50, 3, 8)
	for _, args := range []string{`{"query":"same"}`, `{"query":"same"} trailing`, `{"query":"same"}`, `{"query":"same"}`} {
		watchdog.beginModelStep()
		if err := watchdog.finishModelStep(toolStepResponse("call", "Grep", args)); err != nil {
			t.Fatalf("args %q error = %v, want incomplete args as evidence barrier", args, err)
		}
	}
}

func TestGenerationWatchdogUserSubmissionResetsPriorEvidence(t *testing.T) {
	t.Parallel()

	watchdog := newGenerationWatchdog(50, 3, 8)
	for step := 0; step < 2; step++ {
		watchdog.beginModelStep()
		if err := watchdog.finishModelStep(toolStepResponse("before", "Grep", `{"query":"same"}`)); err != nil {
			t.Fatal(err)
		}
	}
	watchdog.resetAll() // chat.Agent calls this when it accepts new user steering.
	for step := 0; step < 2; step++ {
		watchdog.beginModelStep()
		if err := watchdog.finishModelStep(toolStepResponse("after", "Grep", `{"query":"same"}`)); err != nil {
			t.Fatalf("post-submission step %d error = %v, want a new evidence window", step, err)
		}
	}
}

func TestGenerationLoopEventIsAgentOwned(t *testing.T) {
	t.Parallel()

	event := generationLoopEvent(&GenerationLoopError{
		Reason: GenerationToolLoop,
		Streak: 6,
		Detail: "same step",
	})
	if event == nil || event.Actor.Name != "agent-watchdog" {
		t.Fatalf("event = %#v, want Agent-owned watchdog actor", event)
	}
	if event.Visibility != session.VisibilityJournal {
		t.Fatalf("event visibility = %q, want durable execution journal", event.Visibility)
	}
	if event.Lifecycle == nil || event.Lifecycle.Status != agentWatchdogCheckpointStatus {
		t.Fatalf("lifecycle = %#v, want %q", event.Lifecycle, agentWatchdogCheckpointStatus)
	}
	if strings.Contains(event.Actor.Name, "control") {
		t.Fatalf("actor = %q, must not claim Control ownership", event.Actor.Name)
	}
}

func toolStepResponse(id, name, args string) *model.Response {
	return &model.Response{
		Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
			ID: id, Name: name, Args: args,
		}}, ""),
		TurnComplete: true,
		StepComplete: true,
		Status:       model.ResponseStatusCompleted,
		FinishReason: model.FinishReasonToolCalls,
	}
}

func textDelta(text string) *model.StreamEvent {
	return &model.StreamEvent{
		Type: model.StreamEventPartDelta,
		PartDelta: &model.PartDelta{
			Kind:      model.PartKindReasoning,
			TextDelta: text,
		},
	}
}
