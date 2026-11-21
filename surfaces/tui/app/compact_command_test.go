package tuiapp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	"github.com/caelis-labs/caelis/internal/gatewayapptest/localclient"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
)

func TestManualCompactCommandAndFeedRenderOneCheckpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"compact\",\"object\":\"chat.completion.chunk\",\"model\":\"observation\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"CONTEXT CHECKPOINT\\n\\nObjective: keep the original requirements.\\n\\nNext actions: continue.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
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
	created, err := clients.Sessions.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "manual-compact-create"}, WorkspaceKey: "workspace", CWD: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	starter, err := appserver.NewSessionTurnClient(clients.Sessions)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := starter.Start(ctx, appserver.SessionTurnStartRequest{SessionID: created.SessionID, Input: "Retain this objective"})
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Close()
	for range turn.Events() {
	}
	if err := turn.Err(); err != nil {
		t.Fatal(err)
	}
	feed, err := clients.Sessions.Reconnect(ctx, appserver.ReconnectRequest{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Subscription.Close()
	nextDelivery := func() appserver.FeedDelivery {
		t.Helper()
		select {
		case delivery, open := <-feed.Subscription.Deliveries():
			if open {
				return delivery
			}
			t.Fatalf("Session feed closed: %v", feed.Subscription.Err())
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		return appserver.FeedDelivery{}
	}
	for nextDelivery().Kind != appserver.FeedDeliverySync {
	}

	adapter, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
		SessionID: created.SessionID, Surface: "cli-tui", WorkspaceKey: "workspace", WorkspaceDir: workspace,
		Sessions: compactWireSessionClient{clients.Sessions}, Participants: clients.Participants, Status: clients.Status,
		Configuration: clients.Configuration, Agents: clients.Agents, Completion: clients.Completion, Plugins: clients.Plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	router := controlprompt.New(controlprompt.RouterConfig{Service: adapter})
	runCompact := func() SlashNoticeMsg {
		t.Helper()
		result, err := router.Route(ctx, controlprompt.Request{Submission: controlprompt.Submission{Text: "/compact"}})
		if err != nil {
			t.Fatal(err)
		}
		var feedback SlashNoticeMsg
		sender := &ProgramSender{Send: func(msg tea.Msg) {
			if notice, ok := unwrapSessionViewMessage(msg).(SlashNoticeMsg); ok {
				feedback = notice
			}
		}}
		executeControlPromptResult(ctx, adapter, sender, result)
		return feedback
	}
	feedback := runCompact()
	var checkpoint eventstream.Envelope
	for checkpoint.EventID == "" {
		for _, env := range nextDelivery().Events {
			if eventstream.UpdateType(env.Update) == eventstream.UpdateCompact {
				raw, err := wirev1.MarshalEnvelope(env)
				if err != nil {
					t.Fatal(err)
				}
				checkpoint, err = wirev1.UnmarshalEnvelope(raw)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, test := range []struct {
		name   string
		events []tea.Msg
	}{
		{"feed first", []tea.Msg{checkpoint, feedback, checkpoint}},
		{"feedback first", []tea.Msg{feedback, checkpoint, feedback}},
		{"feedback alone", []tea.Msg{feedback}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := newACPEventstreamRegressionModel(t, 120, 32)
			m.currentSessionID = created.SessionID
			for _, msg := range test.events {
				m.Update(msg)
			}
			m.flushPendingViewportSync()
			frame := m.View().Content
			if rows := strings.Count(frame, transcript.CompactNoticeLabel); rows != 1 {
				t.Fatalf("got %d success rows for checkpoint %s:\n%s", rows, checkpoint.EventID, frame)
			}
		})
	}
	if feedback.CheckpointID != checkpoint.EventID || feedback.SessionID != created.SessionID {
		t.Fatalf("slash feedback %+v did not identify feed checkpoint %s", feedback, checkpoint.EventID)
	}
	if noop := runCompact(); noop.Text != "Nothing to compact" || noop.CheckpointID != "" {
		t.Fatalf("no-op feedback = %+v", noop)
	}
}

type compactWireSessionClient struct{ appserver.SessionClient }

func (c compactWireSessionClient) CompactSession(ctx context.Context, req appserver.CompactSessionRequest) (appserver.CommandResult, error) {
	result, err := c.SessionClient.CompactSession(ctx, req)
	if err != nil {
		return result, err
	}
	raw, err := wirev1.Marshal(result)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	var decoded appserver.CommandResult
	err = wirev1.Unmarshal(raw, &decoded)
	return decoded, err
}
