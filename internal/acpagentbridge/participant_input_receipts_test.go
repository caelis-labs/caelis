package acpagentbridge

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
)

type participantReceiptBackend struct {
	release chan struct{}
	failure error
	calls   atomic.Int32
}

func (*participantReceiptBackend) List(context.Context, string) ([]collaboration.Thread, error) {
	return []collaboration.Thread{{ID: "task-1", ParticipantID: "participant", SessionID: "child", Generation: "g", CanDeliver: true}}, nil
}
func (*participantReceiptBackend) Deliver(context.Context, string, []collaboration.Message) error {
	return errors.New("unexpected Agent mailbox input")
}
func (b *participantReceiptBackend) DeliverUserInput(ctx context.Context, _ collaboration.UserInput) error {
	b.calls.Add(1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.release:
		return b.failure
	}
}

type participantReceiptClient struct{ mailbox *collaboration.Service }

func (c participantReceiptClient) SubmitSubagentInput(ctx context.Context, req appserver.SubagentInputRequest) (collaboration.UserInputStatus, error) {
	return c.mailbox.EnqueueUserInput(ctx, req.OperationID, "user", req.SessionID, req.ParticipantID, req.TaskID, req.Input, req.ContentParts)
}
func (c participantReceiptClient) SubagentInputStatuses(ctx context.Context, req appserver.SubagentInputStatusRequest) ([]collaboration.UserInputStatus, error) {
	return c.mailbox.UserInputStatuses(ctx, "user", req.SessionID, req.IDs)
}

func TestACPParticipantInputReportsAsynchronousDeliveryOutcome(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		label   string
	}{
		{"unsupported", errorcode.New(errorcode.Unsupported, "endpoint does not support user input"), "Not sent · endpoint does not support user input"},
		{"unknown", errors.New("connection lost after dispatch"), "Delivery unconfirmed · not retried"},
		{"sent", nil, ": Sent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			backend := &participantReceiptBackend{release: make(chan struct{}), failure: tc.failure}
			mailbox, err := collaboration.Open(filepath.Join(t.TempDir(), "control.sqlite"), backend)
			if err != nil {
				t.Fatal(err)
			}
			defer mailbox.Close()
			runCtx, stop := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { defer close(done); mailbox.Run(runCtx, nil) }()
			defer func() { stop(); <-done }()
			client := participantReceiptClient{mailbox: mailbox}
			sessions := &participantSessionClient{state: appserver.SessionState{SessionID: "session-1", CWD: t.TempDir(), Participants: []session.ParticipantBinding{{
				ID: "participant", Label: "@worker", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleSidecar, DelegationID: "task-1",
			}}}, feed: &participantApprovalFeed{deliveries: make(chan appserver.FeedDelivery), closed: make(chan struct{})}}
			adapter, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
				SessionID: "session-1", Sessions: sessions, SubagentInputs: client,
				Participants: &struct{ appserver.ParticipantClient }{}, Status: &struct{ appserver.StatusClient }{},
				Configuration: &struct{ appserver.ConfigurationClient }{}, Agents: &struct{ appserver.AgentClient }{},
				Completion: &struct{ appserver.CompletionClient }{}, Plugins: &struct{ appserver.PluginClient }{},
			})
			if err != nil {
				t.Fatal(err)
			}
			child, err := adapter.ContinueAgentRun(ctx, "worker", "recheck the fix", nil)
			if err != nil || child.InputReceipt == nil || child.InputReceipt.ID == "" || child.InputReceipt.State != "queued" {
				t.Fatalf("/handle enqueue = %#v, %v", child, err)
			}
			sub := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 4)}
			streams := &acpMuxTestService{requests: make(chan taskstream.SubscribeRequest, 4), sub: sub}
			tasks, err := taskstream.BindClient(streams, taskstream.Principal{ID: "user"})
			if err != nil {
				t.Fatal(err)
			}
			bridge := &RuntimeAgent{sessionClient: sessions, taskStreamClient: tasks, subagentInputClient: client}
			callbacks := &participantCallbacks{acpMuxPromptCallbacks: acpMuxPromptCallbacks{updates: make(chan eventstream.SessionNotification, 16)}}
			defer bridge.clearSessionDelivery("session-1")
			promptCtx, endPrompt := context.WithCancel(ctx)
			if err := bridge.emitPromptRouterResult(promptCtx, session.Session{SessionRef: session.SessionRef{SessionID: child.SessionID}}, controlprompt.Result{Handled: true, ParticipantTask: &child}, callbacks, true); err != nil {
				t.Fatal(err)
			}
			endPrompt()
			awaitReceiptNotice(t, ctx, callbacks.updates, child.InputReceipt.ID+": Queued")
			close(backend.release)
			awaitReceiptNotice(t, ctx, callbacks.updates, tc.label)
			observer := bridge.participantTaskObserver("session-1", "task-1")
			if observer == nil {
				t.Fatal("receipt outcome closed Task observation")
			}
			// Join delivery before inspecting its final bookkeeping.
			bridge.clearSessionDelivery("session-1")
			observer.mu.Lock()
			pending := len(observer.inputReceipts)
			observer.mu.Unlock()
			if pending != 0 {
				t.Fatal("terminal receipt retained")
			}
			if backend.calls.Load() != 1 {
				t.Fatalf("input was resubmitted: %d", backend.calls.Load())
			}
		})
	}
}

func awaitReceiptNotice(t *testing.T, ctx context.Context, updates <-chan eventstream.SessionNotification, want string) {
	t.Helper()
	for {
		select {
		case update := <-updates:
			chunk, ok := update.Update.(eventstream.ContentChunk)
			if !ok {
				continue
			}
			content, _ := chunk.Content.(eventstream.TextContent)
			if strings.Contains(content.Text, want) {
				return
			}
		case <-ctx.Done():
			t.Fatalf("missing input receipt notice %q", want)
		}
	}
}
