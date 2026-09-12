package tuiapp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	"github.com/caelis-labs/caelis/internal/gatewayapptest/localclient"
)

func TestProductSessionObservationTracksRealHostTurns(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	release := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"observation\",\"object\":\"chat.completion.chunk\",\"model\":\"observation\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"visible output\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer provider.Close()
	workspace := t.TempDir()
	clients, closeHost, err := localclient.New(ctx, t.TempDir(), workspace, provider.URL, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeHost(); err != nil {
			t.Error(err)
		}
	}()
	defer close(release)
	created, err := clients.Sessions.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "observation-create"}, WorkspaceKey: "workspace", CWD: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
		Surface: "cli-tui", WorkspaceKey: "workspace", WorkspaceDir: workspace,
		Sessions: clients.Sessions, Participants: clients.Participants, Status: clients.Status,
		Configuration: clients.Configuration, Agents: clients.Agents, Completion: clients.Completion, Plugins: clients.Plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	snapshot, err := adapter.ResumeSession(ctx, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	messages := make(chan tea.Msg, 128)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	defer sender.Close()
	observeSelectedSession(ctx, sender, snapshot.Reconnect, false)
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	nextMessage := func() tea.Msg {
		t.Helper()
		select {
		case msg := <-messages:
			m.Update(msg)
			m.drainPendingRenderEvents(time.Now())
			m.flushPendingViewportSync()
			return unwrapSessionViewMessage(msg)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return nil
		}
	}
	for {
		if _, ready := nextMessage().(sessionHistoryReadyMsg); ready {
			break
		}
	}
	starter, err := appserver.NewSessionTurnClient(clients.Sessions)
	if err != nil {
		t.Fatal(err)
	}
	var frames []string
	var previousTurnID string
	for _, source := range []string{"another client", "local admission"} {
		if source == "another client" {
			turn, err := starter.Start(ctx, appserver.SessionTurnStartRequest{SessionID: created.SessionID, Input: "hello"})
			if err != nil {
				t.Fatal(err)
			}
			defer turn.Close()
		} else {
			turn, err := adapter.Submit(ctx, controlprompt.Submission{Text: "next turn"})
			if err != nil {
				t.Fatal(err)
			}
			if _, attached := attachAdmittedSession(ctx, adapter, sender, turn); !attached {
				t.Fatal("local admission did not retain the selected Session observation")
			}
		}
		for {
			env, ok := nextMessage().(eventstream.Envelope)
			if ok && eventstream.UpdateType(env.Update) == eventstream.UpdateAgentMessage {
				break
			}
		}
		state, err := clients.Sessions.InspectSession(ctx, appserver.StateRequest{SessionID: created.SessionID})
		if err != nil {
			t.Fatal(err)
		}
		observed := snapshot.Reconnect.State().Run
		if !state.Run.Active || !m.turnRunning() || !adapter.CanSubmitRunningPrompt() ||
			observed.HandleID != state.Run.HandleID || observed.RunID != state.Run.RunID || observed.TurnID != state.Run.TurnID ||
			observed.TurnID == previousTurnID {
			t.Fatalf("%s: Host=%+v observed=%+v TUI running=%v", source, state.Run, observed, m.turnRunning())
		}
		previousTurnID = observed.TurnID
		frame := m.View().Content
		if !strings.Contains(ansi.Strip(frame), "visible output") {
			t.Fatalf("%s output missing from frame:\n%s", source, ansi.Strip(frame))
		}
		frames = append(frames, frame)
		if _, err := adapter.Submit(ctx, controlprompt.Submission{Text: "steered input", Mode: controlprompt.SubmissionModeActiveTurn}); err != nil {
			t.Fatalf("%s: steer observed Turn: %v", source, err)
		}
		if err := adapter.InterruptSession(ctx, created.SessionID); err != nil {
			t.Fatalf("%s: interrupt observed Turn: %v", source, err)
		}
		for {
			env, ok := nextMessage().(eventstream.Envelope)
			if ok && eventstream.IsTurnTerminalLifecycle(env) {
				if env.TurnID != previousTurnID {
					t.Fatalf("terminal Turn = %q, want %q", env.TurnID, previousTurnID)
				}
				break
			}
		}
		if m.turnRunning() || adapter.CanSubmitRunningPrompt() {
			t.Fatalf("%s: terminal did not return the observer to idle", source)
		}
		frames = append(frames, m.View().Content)
	}
	updates := renderFullscreenFramesForTest(t, m.width, m.height, frames...)
	assertPhysicalFullscreenFrame(t, m.width, m.height, frames[len(frames)-1], updates)
}
