package tuiapp

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/charmbracelet/x/ansi"
)

type heldStatusService struct {
	*interruptBridgeStub
	entered chan struct{}
	release chan struct{}
}

func (s *heldStatusService) Status(ctx context.Context) (controlstatus.StatusSnapshot, error) {
	close(s.entered)
	select {
	case <-ctx.Done():
		return controlstatus.StatusSnapshot{}, ctx.Err()
	case <-s.release:
	}
	return controlstatus.StatusSnapshot{Session: controlstatus.StatusSession{ID: "test-session"}, Usage: controlstatus.StatusUsage{SessionUsageTotal: controlstatus.UsageSnapshot{TotalTokens: 42}}}, nil
}

func TestSlowStatusKeepsTypingAndLateOutputPreservesDraft(t *testing.T) {
	submitted := make(chan struct{}, 1)
	service := &heldStatusService{interruptBridgeStub: &interruptBridgeStub{submitted: submitted}, entered: make(chan struct{}), release: make(chan struct{})}
	messages := make(chan tea.Msg, 32)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	defer sender.Close()
	cfg := ConfigFromControlService(service, sender, Config{Context: t.Context(), NoColor: true, NoAnimation: true, PromptRouterFactory: controlprompt.New})
	m := NewModel(cfg)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.setCommands(DefaultCommands())
	m.setInputText("/status")
	m.syncTextareaFromInput()
	_, cmd := m.submitInteractiveLine("/status", "/status", nil)
	if cmd == nil || m.textarea.Value() != "" {
		t.Fatal("submission did not consume its own command")
	}
	var id uint64
	for localID := range m.submissionDispatches {
		id = localID
	}
	_, execute := m.handleSubmissionDispatch(submissionDispatchMsg{submission: Submission{Text: "/status", localID: id, viewGeneration: m.viewGeneration}, turnGeneration: m.liveTurn.generation})
	done := make(chan tea.Msg, 1)
	go func() { done <- execute() }()
	select {
	case <-service.entered:
	case <-time.After(time.Second):
		t.Fatal("status did not start")
	}
	var frames []string
	for _, r := range "新的草稿 abc" {
		m.Update(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}
	m.insertAttachmentsAtCursor([]string{"draft.png"})
	want := m.textarea.Value()
	attachments := cloneInputAttachments(m.inputAttachments)
	frames = append(frames, m.View().Content)
	if !strings.Contains(ansi.Strip(frames[0]), "新的草稿 abc") {
		t.Fatal("typing while status waits is not visible")
	}
	close(service.release)
	var completion tea.Msg
	select {
	case completion = <-done:
	case <-time.After(time.Second):
		t.Fatal("status did not finish")
	}
	for len(messages) > 0 {
		m.Update(<-messages)
	}
	m.Update(completion)
	m.flushPendingDeferredBatches()
	m.drainPendingRenderEvents(time.Now())
	m.flushPendingViewportSync()
	if m.textarea.Value() != want || !reflect.DeepEqual(m.inputAttachments, attachments) {
		t.Fatal("late status cleared a newer draft")
	}
	frames = append(frames, m.View().Content)
	if !strings.Contains(ansi.Strip(frames[1]), "Status") {
		t.Fatal("status result not rendered")
	}
	select {
	case <-submitted:
		t.Fatal("local status was sent to the model")
	default:
	}
	updates := renderFullscreenFramesForTest(t, 80, 24, frames...)
	assertPhysicalFullscreenFrame(t, 80, 24, frames[1], updates)
}
