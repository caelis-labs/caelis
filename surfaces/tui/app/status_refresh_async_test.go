package tuiapp

import (
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/surfaces/internal/promptview"
)

func TestNewModelDoesNotRunStatusCallbacksBeforeFirstFrame(t *testing.T) {
	t.Parallel()

	called := make(chan struct{}, 1)
	release := make(chan struct{})
	returned := make(chan *Model, 1)
	go func() {
		returned <- NewModel(Config{
			ModelAlias: "initial-model",
			RefreshStatus: func() (string, string) {
				called <- struct{}{}
				<-release
				return "refreshed-model", ""
			},
		})
	}()

	var model *Model
	select {
	case model = <-returned:
	case <-time.After(500 * time.Millisecond):
		close(release)
		<-returned
		t.Fatal("NewModel blocked on a status callback before the first frame")
	}
	select {
	case <-called:
		close(release)
		t.Fatal("NewModel invoked a status callback before Init scheduled background work")
	default:
		close(release)
	}
	if model.statusModel != "" {
		t.Fatalf("status model before background refresh = %q, want empty", model.statusModel)
	}
}

func TestModelInitSchedulesStatusRefreshCommand(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	resultReceived := make(chan struct{}, 1)
	model := NewModel(Config{
		RefreshStatus: func() (string, string) {
			calls.Add(1)
			return "refreshed-model", promptview.FormatContextUsage(0, 100000)
		},
		RefreshStatusUsage: func() controlstatus.StatusUsage {
			return controlstatus.StatusUsage{ContextWindowTokens: 100000, ContextUsageAvailable: true}
		},
	})
	if calls.Load() != 0 {
		t.Fatalf("status calls after NewModel = %d, want 0", calls.Load())
	}

	program := tea.NewProgram(
		model,
		tea.WithInput(nil),
		tea.WithoutRenderer(),
		tea.WithoutSignalHandler(),
		tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if _, ok := msg.(StatusRefreshResultMsg); ok {
				select {
				case resultReceived <- struct{}{}:
				default:
				}
			}
			return msg
		}),
	)
	var (
		finalModel tea.Model
		runErr     error
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		finalModel, runErr = program.Run()
	}()
	t.Cleanup(func() {
		program.Quit()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			program.Kill()
		}
	})

	select {
	case <-resultReceived:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Init did not deliver the status refresh result; status calls = %d", calls.Load())
	}
	// The filter runs before Model.Update. Quit is sent through the same
	// unbuffered event loop, so it cannot be received until that update ends.
	program.Quit()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		program.Kill()
		t.Fatal("status-refresh test program did not stop")
	}
	if runErr != nil {
		t.Fatalf("run status-refresh test program: %v", runErr)
	}
	if calls.Load() != 1 {
		t.Fatalf("status calls = %d, want 1", calls.Load())
	}
	final, ok := finalModel.(*Model)
	if !ok {
		t.Fatalf("final model = %T, want *Model", finalModel)
	}
	if final.statusModel != "refreshed-model" {
		t.Fatalf("final status model = %q, want refreshed-model", final.statusModel)
	}
	if final.statusContext != "" {
		t.Fatalf("final status context = %q, want empty before observed usage", final.statusContext)
	}
	if final.statusUsageWindow != 100000 {
		t.Fatalf("status window = %d, want lightweight capacity retained", final.statusUsageWindow)
	}
	if final.statusRefreshInFlight {
		t.Fatal("status refresh remained in flight after its result was applied")
	}
	updated, _ := final.Update(eventstream.Envelope{
		Kind:           eventstream.KindSessionUpdate,
		UsageSemantics: eventstream.UsageSemanticsProviderUsage,
		Update: eventstream.UsageUpdateFromSnapshot(session.UsageSnapshot{
			TotalTokens: 1_600,
		}, nil),
	})
	final = updated.(*Model)
	wantAfterUsage := promptview.FormatContextUsage(1_600, 100000)
	if final.statusContext != wantAfterUsage || final.statusView.Tokens != wantAfterUsage {
		t.Fatalf("total-only usage after lightweight capacity = %q / %q, want %q", final.statusContext, final.statusView.Tokens, wantAfterUsage)
	}
}

func TestMainACPUsageUpdatesStatusContextForLiveAndReplay(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{})
	usage := eventstream.Envelope{
		Kind:           eventstream.KindSessionUpdate,
		SessionID:      "session-1",
		UsageSemantics: eventstream.UsageSemanticsContextGauge,
		Update: eventstream.UsageUpdate{
			SessionUpdate: eventstream.UpdateUsage,
			Used:          1600,
			Size:          100000,
		},
	}
	want := promptview.FormatContextUsage(1600, 100000)

	updated, _ := model.Update(usage)
	model = updated.(*Model)
	if model.statusContext != want {
		t.Fatalf("live usage status = %q, want %q", model.statusContext, want)
	}
	if model.statusView.Tokens != want {
		t.Fatalf("live usage view tokens = %q, want %q", model.statusView.Tokens, want)
	}
	cleared := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", UsageSemantics: eventstream.UsageSemanticsContextGauge,
		Update: eventstream.UsageUpdate{SessionUpdate: eventstream.UpdateUsage, Size: 100000, Used: 0},
	}
	updated, _ = model.Update(cleared)
	model = updated.(*Model)
	if model.statusContext != "" || model.statusView.Tokens != "" || model.statusUsageTotal != 0 || model.statusUsageWindow != 100000 {
		t.Fatalf("zero standard gauge did not replace footer: context=%q view=%q usage=%d/%d", model.statusContext, model.statusView.Tokens, model.statusUsageTotal, model.statusUsageWindow)
	}
	updated, _ = model.Update(usage)
	model = updated.(*Model)
	totalOnly := eventstream.Envelope{
		Kind:           eventstream.KindSessionUpdate,
		SessionID:      "session-1",
		UsageSemantics: eventstream.UsageSemanticsProviderUsage,
		Update: eventstream.UsageUpdateFromSnapshot(session.UsageSnapshot{
			TotalTokens: 2_400,
		}, nil),
	}
	updated, _ = model.Update(totalOnly)
	model = updated.(*Model)
	want = promptview.FormatContextUsage(2_400, 100000)
	if model.statusContext != want || model.statusView.Tokens != want {
		t.Fatalf("total-only usage erased capacity: %q / %q, want %q", model.statusContext, model.statusView.Tokens, want)
	}
	model.applyTranscriptUsage(TranscriptEvent{
		Kind:  TranscriptEventUsage,
		Scope: ACPProjectionSubagent,
		Usage: &session.UsageSnapshot{TotalTokens: 9999, ContextWindowTokens: 10000},
	})
	if model.statusContext != want {
		t.Fatalf("child usage overwrote main status = %q, want %q", model.statusContext, want)
	}
	if model.statusView.Tokens != want {
		t.Fatalf("child usage overwrote main view tokens = %q, want %q", model.statusView.Tokens, want)
	}

	model.resetConversationView()
	if model.statusContext != "" {
		t.Fatalf("status after session reset = %q, want empty", model.statusContext)
	}
	if model.statusView.Tokens != "" {
		t.Fatalf("view tokens after session reset = %q, want empty", model.statusView.Tokens)
	}
	replay := projectResumeReplayEvents([]eventstream.Envelope{usage})
	updated, _ = model.Update(TranscriptEventsMsg{Events: replay})
	model = updated.(*Model)
	replayWant := promptview.FormatContextUsage(1_600, 100000)
	if model.statusContext != replayWant {
		t.Fatalf("replay usage status = %q, want %q", model.statusContext, replayWant)
	}
	if model.statusView.Tokens != replayWant {
		t.Fatalf("replay usage view tokens = %q, want %q", model.statusView.Tokens, replayWant)
	}
}

func TestEmptyLightweightStatusDoesNotEraseStreamUsage(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{})
	model.statusContext = "1.6k / 100k · 2%"
	model.statusView.Tokens = model.statusContext
	model.handleStatusRefreshResultMsg(StatusRefreshResultMsg{
		Model:     "model-a",
		HasStatus: true,
		HasView:   true,
		Status:    StatusViewModel{Model: "model-a"},
	})
	if model.statusContext != "1.6k / 100k · 2%" {
		t.Fatalf("empty lightweight status erased stream usage: %q", model.statusContext)
	}
	if model.statusView.Tokens != "1.6k / 100k · 2%" {
		t.Fatalf("empty lightweight status erased view tokens: %q", model.statusView.Tokens)
	}
}

func TestSetStatusReplacesUsageWhenModelChanges(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{})
	model.handleSetStatusMsg(SetStatusMsg{
		Model: "model-a", TotalTokens: 1_600, ContextWindowTokens: 100000, HasUsage: true,
	})
	model.handleSetStatusMsg(SetStatusMsg{
		Model: "model-b", ContextWindowTokens: 200000, HasUsage: true,
	})
	if model.statusUsageTotal != 0 || model.statusUsageWindow != 200000 {
		t.Fatalf("model-b usage = %d/%d, want 0/200000", model.statusUsageTotal, model.statusUsageWindow)
	}
	if model.statusContext != "" || model.statusView.Tokens != "" {
		t.Fatalf("model-b context = %q / %q, want empty until observed usage", model.statusContext, model.statusView.Tokens)
	}
}

func TestStatusRefreshReplacesUsageWhenModelChanges(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{})
	model.statusModel = "model-a"
	model.replaceStatusUsage(1_600, 100000)
	model.handleStatusRefreshResultMsg(StatusRefreshResultMsg{
		Model: "model-b", HasStatus: true,
		TotalTokens: 0, ContextWindowTokens: 200000, HasUsage: true,
	})
	if model.statusUsageTotal != 0 || model.statusUsageWindow != 200000 {
		t.Fatalf("refreshed model-b usage = %d/%d, want 0/200000", model.statusUsageTotal, model.statusUsageWindow)
	}
	if model.statusContext != "" || model.statusView.Tokens != "" {
		t.Fatalf("refreshed model-b context = %q / %q, want empty until observed usage", model.statusContext, model.statusView.Tokens)
	}
}

func TestACPStatusRefreshReplacesZeroUsageWithoutModelChange(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{})
	model.statusModel = "codex"
	model.replaceStatusUsage(42000, 200000)
	model.handleStatusRefreshResultMsg(StatusRefreshResultMsg{
		Model: "codex", HasStatus: true,
		TotalTokens: 0, ContextWindowTokens: 200000, HasUsage: true, UsageReplace: true,
	})
	if model.statusUsageTotal != 0 || model.statusUsageWindow != 200000 || model.statusContext != "" || model.statusView.Tokens != "" {
		t.Fatalf("ACP status refresh retained stale gauge: context=%q view=%q usage=%d/%d", model.statusContext, model.statusView.Tokens, model.statusUsageTotal, model.statusUsageWindow)
	}
}

func TestACPStatusRefreshWithoutAvailableUsagePreservesLiveGauge(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{})
	model.statusModel = "codex"
	model.statusUsageControllerEpoch = "epoch-1"
	model.statusUsageIdentityKnown = true
	model.replaceStatusUsage(42000, 200000)
	model.handleStatusRefreshResultMsg(StatusRefreshResultMsg{
		Model:                "codex",
		HasStatus:            true,
		HasView:              true,
		Status:               StatusViewModel{Model: "codex", Provider: "acp"},
		UsageReplace:         true,
		UsageControllerEpoch: "epoch-1",
		HasUsageIdentity:     true,
	})
	want := promptview.FormatContextUsage(42000, 200000)
	if model.statusUsageTotal != 42000 || model.statusUsageWindow != 200000 || model.statusContext != want || model.statusView.Tokens != want {
		t.Fatalf("unavailable ACP status erased live gauge: context=%q view=%q usage=%d/%d", model.statusContext, model.statusView.Tokens, model.statusUsageTotal, model.statusUsageWindow)
	}
}

func TestACPStatusRefreshClearsUsageWhenControllerEpochChangesWithoutModelChange(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{})
	model.statusModel = "codex"
	model.statusUsageControllerEpoch = "epoch-1"
	model.statusUsageIdentityKnown = true
	model.replaceStatusUsage(42000, 200000)
	model.handleStatusRefreshResultMsg(StatusRefreshResultMsg{
		Model:                "codex",
		HasStatus:            true,
		HasView:              true,
		Status:               StatusViewModel{Model: "codex", Provider: "acp"},
		UsageReplace:         true,
		UsageControllerEpoch: "epoch-2",
		HasUsageIdentity:     true,
	})
	if model.statusUsageTotal != 0 || model.statusUsageWindow != 0 || model.statusContext != "" || model.statusView.Tokens != "" {
		t.Fatalf("controller handoff retained stale gauge: context=%q view=%q usage=%d/%d", model.statusContext, model.statusView.Tokens, model.statusUsageTotal, model.statusUsageWindow)
	}
	if model.statusUsageControllerEpoch != "epoch-2" || !model.statusUsageIdentityKnown {
		t.Fatalf("usage identity = %q/%v, want epoch-2/known", model.statusUsageControllerEpoch, model.statusUsageIdentityKnown)
	}
}

func TestStatusMessagesUseTypedUsageSemanticsInsteadOfProviderName(t *testing.T) {
	t.Parallel()

	var sent SetStatusMsg
	sendStatusUpdate(func(msg tea.Msg) {
		var ok bool
		sent, ok = msg.(SetStatusMsg)
		if !ok {
			t.Fatalf("status message = %T, want SetStatusMsg", msg)
		}
	}, controlstatus.StatusSnapshot{
		ModelStatus: controlstatus.StatusModel{Provider: "remote-agent"},
		Usage: controlstatus.StatusUsage{
			TotalTokens:                 42000,
			ContextWindowTokens:         200000,
			ContextUsageAvailable:       true,
			ContextUsageReplace:         true,
			ContextUsageControllerEpoch: "epoch-1",
		},
	})
	if !sent.HasUsage || !sent.UsageReplace || sent.UsageControllerEpoch != "epoch-1" {
		t.Fatalf("typed status usage = %#v, want available replace gauge from epoch-1", sent)
	}

	model := NewModel(Config{
		RefreshStatusUsage: func() controlstatus.StatusUsage {
			return controlstatus.StatusUsage{
				TotalTokens:                 1600,
				ContextWindowTokens:         100000,
				ContextUsageAvailable:       true,
				ContextUsageControllerEpoch: "epoch-merge",
			}
		},
		RefreshStatusView: func() StatusViewModel {
			return StatusViewModel{Provider: "acp"}
		},
	})
	raw := model.statusRefreshCmd()()
	msg, ok := raw.(StatusRefreshResultMsg)
	if !ok {
		t.Fatalf("refresh result = %T, want StatusRefreshResultMsg", raw)
	}
	if !msg.HasUsage || msg.UsageReplace || !msg.HasUsageIdentity || msg.UsageControllerEpoch != "epoch-merge" {
		t.Fatalf("refresh usage = %#v, want typed merge from epoch-merge despite acp display provider", msg)
	}
}
