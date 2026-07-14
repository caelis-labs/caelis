package tuiapp

import (
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/surfaces/statusbar"
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
	model := NewModel(Config{
		RefreshStatus: func() (string, string) {
			calls.Add(1)
			return "refreshed-model", "1k / 100k"
		},
	})
	if calls.Load() != 0 {
		t.Fatalf("status calls after NewModel = %d, want 0", calls.Load())
	}

	cmd := model.Init()
	if cmd == nil || !model.statusRefreshInFlight {
		t.Fatal("Init did not schedule the initial status refresh")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("Init command result has type %T, want tea.BatchMsg", cmd())
	}
	results := make(chan tea.Msg, len(batch))
	for _, one := range batch {
		go func(run tea.Cmd) { results <- run() }(one)
	}
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case msg := <-results:
			if _, ok := msg.(StatusRefreshResultMsg); ok {
				if calls.Load() != 1 {
					t.Fatalf("status calls = %d, want 1", calls.Load())
				}
				return
			}
		case <-deadline:
			t.Fatalf("Init batch did not produce StatusRefreshResultMsg; status calls = %d", calls.Load())
		}
	}
}

func TestMainACPUsageUpdatesStatusContextForLiveAndReplay(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{})
	usage := eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: eventstream.UsageUpdateFromSnapshot(eventstream.UsageSnapshot{
			TotalTokens:         1600,
			ContextWindowTokens: 100000,
		}, nil),
	}
	want := statusbar.FormatContextUsage(1600, 100000)

	updated, _ := model.Update(usage)
	model = updated.(*Model)
	if model.statusContext != want {
		t.Fatalf("live usage status = %q, want %q", model.statusContext, want)
	}
	if model.statusView.Tokens != want {
		t.Fatalf("live usage view tokens = %q, want %q", model.statusView.Tokens, want)
	}
	model.applyTranscriptUsage(TranscriptEvent{
		Kind:  TranscriptEventUsage,
		Scope: ACPProjectionSubagent,
		Usage: &eventstream.UsageSnapshot{TotalTokens: 9999, ContextWindowTokens: 10000},
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
	if model.statusContext != want {
		t.Fatalf("replay usage status = %q, want %q", model.statusContext, want)
	}
	if model.statusView.Tokens != want {
		t.Fatalf("replay usage view tokens = %q, want %q", model.statusView.Tokens, want)
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
