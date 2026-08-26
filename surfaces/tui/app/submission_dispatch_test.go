package tuiapp

import (
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestExecutableSubmissionsShareRenderYieldBeforeDispatch(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		displayLine string
		attachments []Attachment
		commands    []string
		startActive bool
		wantMode    SubmissionMode
	}{
		{name: "idle text", line: "hello", displayLine: "hello", wantMode: SubmissionModeDefault},
		{
			name: "idle image", line: "inspect image", displayLine: "inspect [image #1] image",
			attachments: []Attachment{{Name: "idle.png", Offset: 8}}, wantMode: SubmissionModeDefault,
		},
		{name: "active text", line: "continue", displayLine: "continue", startActive: true, wantMode: SubmissionModeActiveTurn},
		{
			name: "active image", line: "inspect image", displayLine: "inspect [image #1] image",
			attachments: []Attachment{{Name: "active.png", Offset: 8}}, startActive: true, wantMode: SubmissionModeActiveTurn,
		},
		{name: "slash execution", line: "/status", displayLine: "/status", commands: []string{"status"}, wantMode: SubmissionModeDefault},
		{
			name: "overlay execution", line: "/btw quick check", displayLine: "/btw quick check",
			commands: []string{"btw"}, startActive: true, wantMode: SubmissionModeOverlay,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []Submission
			model := NewModel(Config{
				NoColor:     true,
				NoAnimation: true,
				RenderFPS:   maximumRendererFPS,
				Commands:    test.commands,
				ExecuteLine: func(submission Submission) TaskResultMsg {
					calls = append(calls, cloneSubmission(submission))
					return TaskResultMsg{ContinueRunning: submission.Mode == SubmissionModeActiveTurn}
				},
				CanSubmitRunningPrompt: func() bool { return true },
			})
			if test.startActive {
				model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(200, 0))
			}

			next, cmd := model.submitInteractiveLine(test.line, test.displayLine, test.attachments)
			model = next.(*Model)
			if cmd == nil {
				t.Fatal("submission returned no render-yield command")
			}
			if len(calls) != 0 {
				t.Fatalf("ExecuteLine calls before render yield = %d, want 0", len(calls))
			}
			if test.wantMode == SubmissionModeActiveTurn {
				if len(model.pendingQueue) != 1 || !model.pendingQueue[0].dispatchScheduled() {
					t.Fatalf("active pending state before render yield = %#v", model.pendingQueue)
				}
			}

			dispatch := submissionDispatchMessageForTest(t, cmd)
			if len(calls) != 0 {
				t.Fatalf("ExecuteLine calls before dispatch message update = %d, want 0", len(calls))
			}
			_, executeCmd := model.Update(dispatch)
			if executeCmd == nil {
				t.Fatal("dispatch message returned no execution command")
			}
			if len(calls) != 0 {
				t.Fatalf("ExecuteLine calls before execution command = %d, want 0", len(calls))
			}
			if _, ok := executeCmd().(TaskResultMsg); !ok {
				t.Fatal("execution command did not return TaskResultMsg")
			}
			if len(calls) != 1 {
				t.Fatalf("ExecuteLine calls = %d, want 1", len(calls))
			}
			if calls[0].Mode != test.wantMode || calls[0].Text != test.line ||
				calls[0].DisplayText != test.displayLine || !reflect.DeepEqual(calls[0].Attachments, test.attachments) {
				t.Fatalf("dispatched submission = %#v", calls[0])
			}
		})
	}
}

func TestActiveSubmissionTerminalBeforeDispatchFallsBackToIdleOnce(t *testing.T) {
	var calls []Submission
	model := NewModel(Config{
		NoAnimation: true,
		RenderFPS:   maximumRendererFPS,
		ExecuteLine: func(submission Submission) TaskResultMsg {
			calls = append(calls, cloneSubmission(submission))
			return TaskResultMsg{}
		},
		CanSubmitRunningPrompt: func() bool { return true },
	})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(210, 0))
	next, cmd := model.submitInteractiveLine(
		"inspect image",
		"inspect [image #1] image",
		[]Attachment{{Name: "fallback.png", Offset: 8}},
	)
	model = next.(*Model)
	dispatch := submissionDispatchMessageForTest(t, cmd)
	model.restoreHistoryEntry("new draft", []inputAttachment{{
		Kind: attachmentKindImage, Name: "new-draft.png", Offset: len([]rune("new ")),
	}})
	beforeExec, beforeDisplay, beforeAttachments := model.prepareComposerSubmission()

	_ = model.finishLiveTurn(time.Unix(211, 0), false, nil)
	if !model.pendingQueue[0].dispatchScheduled() {
		t.Fatalf("successful terminal dropped scheduled prompt: %#v", model.pendingQueue)
	}
	updated, fallbackCmd := model.Update(dispatch)
	model = updated.(*Model)
	if fallbackCmd == nil {
		t.Fatal("successful terminal did not schedule idle fallback")
	}
	if len(calls) != 0 {
		t.Fatalf("fallback executed before its render yield: %d calls", len(calls))
	}
	assertComposerSubmissionEqual(t, model, beforeExec, beforeDisplay, beforeAttachments)

	fallbackDispatch := submissionDispatchMessageForTest(t, fallbackCmd)
	_, executeCmd := model.Update(fallbackDispatch)
	if executeCmd == nil {
		t.Fatal("idle fallback dispatch returned no execution command")
	}
	_ = executeCmd()
	if len(calls) != 1 || calls[0].Mode != SubmissionModeDefault || calls[0].Attachments[0].Name != "fallback.png" {
		t.Fatalf("idle fallback calls = %#v, want one exact default submission", calls)
	}
	assertComposerSubmissionEqual(t, model, beforeExec, beforeDisplay, beforeAttachments)
}

func TestInterruptDuringRenderYieldRevokesIdleDispatch(t *testing.T) {
	calls := 0
	model := NewModel(Config{
		NoAnimation: true,
		RenderFPS:   maximumRendererFPS,
		ExecuteLine: func(Submission) TaskResultMsg {
			calls++
			return TaskResultMsg{}
		},
		CancelRunning: func() bool {
			t.Fatal("provisional submission reached Control interrupt")
			return false
		},
	})
	next, cmd := model.submitLine("cancel before dispatch")
	model = next.(*Model)
	dispatch := submissionDispatchMessageForTest(t, cmd)

	updated, _ := model.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(*Model)
	if model.turnRunning() {
		t.Fatal("interrupt left the provisional live Turn running")
	}
	if len(model.submissionDispatches) != 0 {
		t.Fatalf("interrupt retained dispatches: %#v", model.submissionDispatches)
	}
	_, executeCmd := model.Update(dispatch)
	if executeCmd != nil || calls != 0 {
		t.Fatalf("late dispatch after interrupt = cmd:%v calls:%d, want no-op", executeCmd != nil, calls)
	}
}

func TestInterruptDuringRenderYieldRevokesActiveTurnDispatch(t *testing.T) {
	calls := 0
	cancelCalls := 0
	model := NewModel(Config{
		NoAnimation: true,
		RenderFPS:   maximumRendererFPS,
		ExecuteLine: func(Submission) TaskResultMsg {
			calls++
			return TaskResultMsg{ContinueRunning: true}
		},
		CanSubmitRunningPrompt: func() bool { return true },
		CancelRunning: func() bool {
			cancelCalls++
			return true
		},
	})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(212, 0))
	next, cmd := model.submitLine("cancel steer before dispatch")
	model = next.(*Model)
	dispatch := submissionDispatchMessageForTest(t, cmd)
	if dispatch.submission.Mode != SubmissionModeActiveTurn {
		t.Fatalf("scheduled mode = %v, want active-turn", dispatch.submission.Mode)
	}

	updated, interruptCmd := model.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(*Model)
	if !model.turnRunning() {
		t.Fatal("interrupt ended the existing Runtime Turn before its terminal lifecycle")
	}
	if len(model.pendingQueue) != 0 || len(model.submissionDispatches) != 0 {
		t.Fatalf("interrupt retained scheduled steer: pending=%#v dispatches=%#v", model.pendingQueue, model.submissionDispatches)
	}
	if interruptCmd == nil {
		t.Fatal("interrupt returned no Control cancel command")
	}
	batch, ok := interruptCmd().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("interrupt command = %T, want non-empty batch", interruptCmd())
	}
	result, ok := batch[len(batch)-1]().(RunningInterruptResultMsg)
	if !ok || !result.Accepted || cancelCalls != 1 {
		t.Fatalf("Control cancel result = %#v calls=%d, want one accepted call", result, cancelCalls)
	}

	_, executeCmd := model.Update(dispatch)
	if executeCmd != nil || calls != 0 {
		t.Fatalf("late active-turn dispatch after interrupt = cmd:%v calls:%d, want no-op", executeCmd != nil, calls)
	}
}

func TestResetAndOverlayDismissRevokeRenderYieldDispatch(t *testing.T) {
	t.Run("conversation reset", func(t *testing.T) {
		calls := 0
		model := NewModel(Config{
			NoAnimation: true,
			RenderFPS:   maximumRendererFPS,
			ExecuteLine: func(Submission) TaskResultMsg {
				calls++
				return TaskResultMsg{}
			},
		})
		next, cmd := model.submitLine("reset before dispatch")
		model = next.(*Model)
		dispatch := submissionDispatchMessageForTest(t, cmd)
		model.resetConversationView()
		_, executeCmd := model.Update(dispatch)
		if executeCmd != nil || calls != 0 {
			t.Fatalf("late dispatch after reset = cmd:%v calls:%d, want no-op", executeCmd != nil, calls)
		}
	})

	t.Run("overlay dismiss", func(t *testing.T) {
		calls := 0
		model := NewModel(Config{
			NoAnimation: true,
			RenderFPS:   maximumRendererFPS,
			Commands:    []string{"btw"},
			ExecuteLine: func(Submission) TaskResultMsg {
				calls++
				return TaskResultMsg{}
			},
		})
		model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(215, 0))
		next, cmd := model.submitLine("/btw cancel this")
		model = next.(*Model)
		dispatch := submissionDispatchMessageForTest(t, cmd)
		updated, _ := model.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
		model = updated.(*Model)
		if model.btwOverlay != nil {
			t.Fatal("Escape did not dismiss BTW overlay")
		}
		_, executeCmd := model.Update(dispatch)
		if executeCmd != nil || calls != 0 {
			t.Fatalf("late overlay dispatch after dismiss = cmd:%v calls:%d, want no-op", executeCmd != nil, calls)
		}
	})
}

func TestActiveSubmissionAbortBeforeDispatchIsNoop(t *testing.T) {
	calls := 0
	model := NewModel(Config{
		NoAnimation: true,
		RenderFPS:   maximumRendererFPS,
		ExecuteLine: func(Submission) TaskResultMsg {
			calls++
			return TaskResultMsg{}
		},
		CanSubmitRunningPrompt: func() bool { return true },
	})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(220, 0))
	next, cmd := model.submitLine("do not replay")
	model = next.(*Model)
	dispatch := submissionDispatchMessageForTest(t, cmd)

	_ = model.finishLiveTurn(time.Unix(221, 0), true, nil)
	if len(model.pendingQueue) != 0 {
		t.Fatalf("abort retained pending queue: %#v", model.pendingQueue)
	}
	_, executeCmd := model.Update(dispatch)
	if executeCmd != nil || calls != 0 {
		t.Fatalf("late dispatch after abort = cmd:%v calls:%d, want no-op", executeCmd != nil, calls)
	}
}

func TestPendingScheduledExtractionUsesLocalIDForDuplicateImages(t *testing.T) {
	queue := pendingPromptQueue{
		{localID: 41, execLine: "inspect", attachments: []Attachment{{Name: "a.png"}}, state: pendingPromptDispatchScheduled},
		{localID: 42, execLine: "inspect", attachments: []Attachment{{Name: "b.png"}}, state: pendingPromptDispatchScheduled},
	}
	got, ok := queue.takeScheduled(42)
	if !ok || got.attachments[0].Name != "b.png" {
		t.Fatalf("takeScheduled(42) = %#v, %v", got, ok)
	}
	if len(queue) != 1 || queue[0].localID != 41 || queue[0].attachments[0].Name != "a.png" {
		t.Fatalf("remaining duplicate queue = %#v", queue)
	}
}

func TestSubmissionRenderDelayFollowsRendererCap(t *testing.T) {
	if got, want := submissionRenderDelay(0), 2*time.Second/60; got != want {
		t.Fatalf("default render delay = %s, want %s", got, want)
	}
	if got, want := submissionRenderDelay(240), 2*time.Second/120; got != want {
		t.Fatalf("capped render delay = %s, want %s", got, want)
	}
}

func BenchmarkRunningSubmissionFirstFeedback(b *testing.B) {
	model := NewModel(Config{
		NoColor:                true,
		NoAnimation:            true,
		RenderFPS:              maximumRendererFPS,
		ExecuteLine:            func(Submission) TaskResultMsg { return TaskResultMsg{ContinueRunning: true} },
		CanSubmitRunningPrompt: func() bool { return true },
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(*Model)
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		model.setInputText("benchmark running submission")
		model.syncTextareaFromInput()
		_, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		if cmd == nil || model.pendingQueue.visibleCount() != 1 {
			b.Fatal("running Enter did not produce pending feedback")
		}
		_ = model.View()
		model.pendingQueue = nil
		model.submissionDispatches = nil
		model.history = nil
		model.historyAttachments = nil
	}
}

func assertComposerSubmissionEqual(t *testing.T, model *Model, wantExec, wantDisplay string, wantAttachments []Attachment) {
	t.Helper()
	gotExec, gotDisplay, gotAttachments := model.prepareComposerSubmission()
	if gotExec != wantExec || gotDisplay != wantDisplay || !reflect.DeepEqual(gotAttachments, wantAttachments) {
		t.Fatalf("composer changed: got=%q/%q/%#v want=%q/%q/%#v",
			gotExec, gotDisplay, gotAttachments, wantExec, wantDisplay, wantAttachments)
	}
}

func submissionDispatchMessageForTest(t *testing.T, cmd tea.Cmd) submissionDispatchMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("nil command while waiting for submissionDispatchMsg")
	}
	msg := cmd()
	if dispatch, ok := msg.(submissionDispatchMsg); ok {
		return dispatch
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			if child == nil {
				continue
			}
			childMsg := child()
			if dispatch, ok := childMsg.(submissionDispatchMsg); ok {
				return dispatch
			}
		}
	}
	t.Fatalf("command returned %T, want submissionDispatchMsg", msg)
	return submissionDispatchMsg{}
}
