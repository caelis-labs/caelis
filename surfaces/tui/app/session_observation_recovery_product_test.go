package tuiapp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	"github.com/caelis-labs/caelis/internal/gatewayapptest/localclient"
)

// The CLI transport owns endpoint discovery. This test switches the same typed
// Session client between real, sequential Host instances sharing one Store.
type recoveryHostSessionClient struct {
	appserver.SessionClient
	mu      sync.Mutex
	current appserver.SessionClient
	gap     chan struct{}
}

func (c *recoveryHostSessionClient) set(client appserver.SessionClient) {
	c.mu.Lock()
	c.current = client
	c.mu.Unlock()
}

func (c *recoveryHostSessionClient) client() appserver.SessionClient {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *recoveryHostSessionClient) Reconnect(ctx context.Context, req appserver.ReconnectRequest) (appserver.ReconnectResult, error) {
	if client := c.client(); client != nil {
		return client.Reconnect(ctx, req)
	}
	select {
	case c.gap <- struct{}{}:
	default:
	}
	return appserver.ReconnectResult{}, errorcode.New(errorcode.Unavailable, "new Host is not ready")
}

func (c *recoveryHostSessionClient) InspectSession(ctx context.Context, req appserver.StateRequest) (appserver.SessionState, error) {
	return c.client().InspectSession(ctx, req)
}

func (c *recoveryHostSessionClient) Prompt(ctx context.Context, req appserver.PromptRequest) (appserver.CommandResult, error) {
	return c.client().Prompt(ctx, req)
}

func TestProductSessionObservationAutomaticallyRecoversAfterHostReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	requests := make(chan string, 4)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"recovery\",\"object\":\"chat.completion.chunk\",\"model\":\"observation\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"visible output\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer provider.Close()
	store, workspace := t.TempDir(), t.TempDir()
	clients, closeHost, err := localclient.New(ctx, store, workspace, provider.URL, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeHost != nil {
			if err := closeHost(); err != nil {
				t.Error(err)
			}
		}
	}()
	created, err := clients.Sessions.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "recovery-create"}, WorkspaceKey: "workspace", CWD: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions := &recoveryHostSessionClient{SessionClient: clients.Sessions, current: clients.Sessions, gap: make(chan struct{}, 1)}
	adapter, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
		Surface: "cli-tui", WorkspaceKey: "workspace", WorkspaceDir: workspace,
		Sessions: sessions, Participants: clients.Participants, Status: clients.Status,
		Configuration: clients.Configuration, Agents: clients.Agents, Completion: clients.Completion, Plugins: clients.Plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	messages := make(chan tea.Msg, 256)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	defer sender.Close()
	cfg := ConfigFromControlService(adapter, sender, Config{Context: ctx, NoColor: true, NoAnimation: true})
	m := NewModel(cfg)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	snapshot, err := adapter.ResumeSession(ctx, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	observeSelectedSession(ctx, sender, snapshot.Reconnect, false)
	recoveryTestReady(t, messages, m)
	turn, err := adapter.Submit(ctx, controlprompt.Submission{Text: "original user request"})
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Close()
	for {
		if env, ok := recoveryTestRead(t, messages, m).(eventstream.Envelope); ok && eventstream.UpdateType(env.Update) == eventstream.UpdateAgentMessage {
			break
		}
	}
	<-requests
	if !m.turnRunning() {
		t.Fatal("initial Host Turn was not observed as active")
	}
	m.textarea.SetValue("draft across Host replacement")
	frames := []string{m.View().Content}
	sessions.set(nil)
	if err := closeHost(); err != nil {
		t.Fatal(err)
	}
	closeHost = nil
	sawInterruption := false
	for {
		msg := recoveryTestRead(t, messages, m)
		if env, ok := msg.(eventstream.Envelope); ok && eventstream.IsTurnTerminalLifecycle(env) {
			sawInterruption = liveTurnLifecycleInterrupted(env)
		}
		if _, ok := msg.(sessionObservationRecoveringMsg); ok {
			break
		}
	}
	if !sawInterruption || m.turnRunning() {
		t.Fatal("Host shutdown did not publish interrupted Turn before disconnect")
	}
	frames = append(frames, m.View().Content)
	select {
	case <-sessions.gap:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	clients, closeHost, err = localclient.New(ctx, store, workspace, provider.URL, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	sessions.set(clients.Sessions)
	recoveryTestReady(t, messages, m)
	if m.currentSessionID != created.SessionID || m.viewGeneration != 2 || m.turnRunning() || m.sessionObservationRecovering || m.textarea.Value() != "draft across Host replacement" || adapter.CanSubmitRunningPrompt() {
		t.Fatalf("recovered Session=%q generation=%d running=%v recovering=%v draft=%q", m.currentSessionID, m.viewGeneration, m.turnRunning(), m.sessionObservationRecovering, m.textarea.Value())
	}
	frame := ansi.Strip(m.View().Content)
	if !strings.Contains(frame, "original user request") {
		t.Fatalf("canonical input missing from recovered frame:\n%s", frame)
	}
	frames = append(frames, m.View().Content)
	select {
	case body := <-requests:
		t.Fatalf("observation recovery submitted work: %s", body)
	default:
	}
	next, err := adapter.Submit(ctx, controlprompt.Submission{Text: "fresh user request"})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	select {
	case body := <-requests:
		if strings.Count(body, "original user request") != 1 || strings.Count(body, "fresh user request") != 1 {
			t.Fatalf("new Host model context did not round-trip both user inputs exactly once: %s", body)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for {
		if env, ok := recoveryTestRead(t, messages, m).(eventstream.Envelope); ok && eventstream.UpdateType(env.Update) == eventstream.UpdateAgentMessage {
			break
		}
	}
	if !m.turnRunning() || !adapter.CanSubmitRunningPrompt() || m.textarea.Value() != "draft across Host replacement" {
		t.Fatal("fresh user prompt did not reuse the recovered observation")
	}
	frames = append(frames, m.View().Content)
	updates := renderFullscreenFramesForTest(t, m.width, m.height, frames...)
	assertPhysicalFullscreenFrame(t, m.width, m.height, frames[len(frames)-1], updates)
}
