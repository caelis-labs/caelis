package tuiapp

import (
	"context"
	"errors"
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
	frames := []string{m.View().Content}
	if first := ansi.Strip(frames[0]); strings.Count(first, "/status") != 1 ||
		!strings.Contains(first, "Running command") || strings.Contains(first, "test-session") {
		t.Fatalf("first frame should show the submitted command and loading state:\n%s", first)
	}
	_, execute := m.Update(cmd())
	if execute == nil {
		t.Fatal("status did not dispatch after the first frame")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- execute() }()
	select {
	case <-service.entered:
	case <-time.After(time.Second):
		t.Fatal("status did not start")
	}
	for _, r := range "新的草稿 abc" {
		m.Update(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}
	m.insertAttachmentsAtCursor([]string{"draft.png"})
	want := m.textarea.Value()
	attachments := cloneInputAttachments(m.inputAttachments)
	frames = append(frames, m.View().Content)
	if waiting := ansi.Strip(frames[1]); !strings.Contains(waiting, "新的草稿 abc") || !strings.Contains(waiting, "Running command") {
		t.Fatalf("draft and command loading should both remain visible:\n%s", waiting)
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
	final := ansi.Strip(frames[2])
	if result := strings.Index(final, "test-session"); strings.Count(final, "/status") != 1 ||
		result <= strings.Index(final, "/status") || strings.Contains(final, "Running command") {
		t.Fatalf("final frame should show one command followed by the result:\n%s", final)
	}
	select {
	case <-submitted:
		t.Fatal("local status was sent to the model")
	default:
	}
	updates := renderFullscreenFramesForTest(t, 80, 24, frames...)
	assertPhysicalFullscreenFrame(t, 80, 24, frames[0], updates[:1])
	assertPhysicalFullscreenFrame(t, 80, 24, frames[2], updates)
}

func TestSlashCommandErrorClearsLoading(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true, ExecuteLine: func(Submission) TaskResultMsg {
		return TaskResultMsg{Err: errors.New("status unavailable")}
	}})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_, dispatch := m.submitInteractiveLine("/status", "/status", nil)
	_, execute := m.Update(dispatch())
	m.Update(execute())
	frame := ansi.Strip(m.View().Content)
	if result := strings.Index(frame, "status unavailable"); strings.Count(frame, "/status") != 1 ||
		result <= strings.Index(frame, "/status") || m.turnRunning() || strings.Contains(frame, "Running command") {
		t.Fatalf("the failure should follow the command and clear the loading state:\n%s", frame)
	}
}
