package tuiapp

import (
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
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
		RefreshStatusUsage: func() (int, int) { return 0, 100000 },
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
		Kind: eventstream.KindSessionUpdate,
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
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: eventstream.UsageUpdateFromSnapshot(session.UsageSnapshot{
			TotalTokens:         1600,
			ContextWindowTokens: 100000,
		}, nil),
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
	totalOnly := eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
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
		Model: "model-a", TotalTokens: 1_600, ContextWindowTokens: 100000,
	})
	model.handleSetStatusMsg(SetStatusMsg{
		Model: "model-b", ContextWindowTokens: 200000,
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
