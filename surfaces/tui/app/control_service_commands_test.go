package tuiapp

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func TestConfigFromControlServiceRunningEnterUsesLocalAdmissionSnapshot(t *testing.T) {
	statusCalled := make(chan struct{}, 1)
	statusRelease := make(chan struct{})
	service := &blockingAgentStatusService{
		interruptBridgeStub: &interruptBridgeStub{},
		statusCalled:        statusCalled,
		statusRelease:       statusRelease,
	}
	t.Cleanup(func() { close(statusRelease) })

	cfg := ConfigFromControlService(service, nil, Config{
		NoColor:   true,
		RenderFPS: maximumRendererFPS,
	})
	service.blockStatus.Store(true)
	model := NewModel(cfg)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(*Model)
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	model.setInputText("steer without status round trip")
	model.syncTextareaFromInput()

	type updateResult struct {
		model tea.Model
		cmd   tea.Cmd
	}
	returned := make(chan updateResult, 1)
	go func() {
		next, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		returned <- updateResult{model: next, cmd: cmd}
	}()

	select {
	case result := <-returned:
		model = result.model.(*Model)
		if result.cmd == nil {
			t.Fatal("running Enter returned no render-yield command")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("running Enter blocked the Bubble Tea update loop")
	}
	select {
	case <-statusCalled:
		t.Fatal("running Enter called the storage-backed AgentStatus path")
	default:
	}
	if got := model.pendingQueue.visibleCount(); got != 1 {
		t.Fatalf("pending count = %d, want 1 before dispatch", got)
	}
	frame := ansi.Strip(model.View().Content)
	if !strings.Contains(frame, "1 pending") {
		t.Fatalf("first feedback frame missing pending count:\n%s", frame)
	}
	if !model.spinnerTickScheduled {
		t.Fatal("running Enter did not leave the spinner tick scheduled")
	}

	spinnerMsg := make(chan tea.Msg, 1)
	go func() { spinnerMsg <- model.spinner.Tick() }()
	select {
	case msg := <-spinnerMsg:
		if next, _ := model.Update(msg); next == nil {
			t.Fatal("spinner tick returned no model")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("spinner tick did not continue after running Enter")
	}
}

type blockingAgentStatusService struct {
	*interruptBridgeStub
	statusCalled  chan<- struct{}
	statusRelease <-chan struct{}
	blockStatus   atomic.Bool
}

func (s *blockingAgentStatusService) AgentStatus(ctx context.Context) (controlprompt.AgentStatusSnapshot, error) {
	if !s.blockStatus.Load() {
		return controlprompt.AgentStatusSnapshot{}, nil
	}
	select {
	case s.statusCalled <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return controlprompt.AgentStatusSnapshot{}, ctx.Err()
	case <-s.statusRelease:
		return controlprompt.AgentStatusSnapshot{}, nil
	}
}
