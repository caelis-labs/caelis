package acpagentbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

// acpParticipantTasks owns presentation subscriptions for explicit background
// Tasks. One Session observer survives prompt completion and handle follow-ups;
// closing it detaches delivery without cancelling Host work.
type acpParticipantTasks struct {
	agent         *RuntimeAgent
	sessionID     string
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	wake          chan struct{}
	callbacks     PromptCallbacks
	mux           *acpTaskStreamMux
	mu            sync.Mutex
	tasks         map[string]struct{}
	inputReceipts map[string]participantInputReceipt
}

func (a *RuntimeAgent) observeParticipantTask(ctx context.Context, child controlprompt.AgentRunResult, cb PromptCallbacks) error {
	if a.sessionClient == nil || a.taskStreamClient == nil {
		return errors.New("internal/acpagentbridge: background participant observation is unavailable")
	}
	sessionID, taskID := strings.TrimSpace(child.SessionID), strings.TrimSpace(child.TaskID)
	if sessionID == "" || taskID == "" {
		return errors.New("internal/acpagentbridge: background participant identity is incomplete")
	}
	a.mu.Lock()
	if a.participantTasks == nil {
		a.participantTasks = make(map[string]*acpParticipantTasks)
	}
	observer := a.participantTasks[sessionID]
	if observer == nil {
		deliveryCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		observer = &acpParticipantTasks{agent: a, sessionID: sessionID, ctx: deliveryCtx, cancel: cancel,
			done: make(chan struct{}), wake: make(chan struct{}, 1), callbacks: cb,
			mux:           newACPTaskStreamClientMux(deliveryCtx, a.taskStreamClient, sessionID),
			tasks:         make(map[string]struct{}),
			inputReceipts: make(map[string]participantInputReceipt),
		}
		a.participantTasks[sessionID] = observer
		go observer.run()
	}
	observer.mu.Lock()
	observer.tasks[taskID] = struct{}{}
	observer.trackInputReceiptLocked(child)
	observer.mu.Unlock()
	observer.mux.ObserveTask(taskID)
	a.mu.Unlock()
	observer.wakeApprovals()
	return nil
}

func (a *RuntimeAgent) participantTaskObserver(sessionID, taskID string) *acpParticipantTasks {
	a.mu.Lock()
	observer := a.participantTasks[sessionID]
	a.mu.Unlock()
	if observer != nil && observer.contains(taskID) {
		return observer
	}
	return nil
}

func (o *acpParticipantTasks) contains(taskID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.tasks[taskID]
	return ok
}

func (a *RuntimeAgent) closeParticipantTasks(sessionID string) {
	a.mu.Lock()
	observer := a.participantTasks[sessionID]
	delete(a.participantTasks, sessionID)
	a.mu.Unlock()
	if observer != nil {
		observer.cancel()
		<-observer.done
	}
}

func (o *acpParticipantTasks) run() {
	defer close(o.done)
	defer o.cancel()
	defer o.mux.Close()
	defer func() {
		o.agent.mu.Lock()
		if o.agent.participantTasks[o.sessionID] == o {
			delete(o.agent.participantTasks, o.sessionID)
		}
		o.agent.mu.Unlock()
	}()
	if err := o.forward(); err != nil && o.ctx.Err() == nil {
		_ = emitACPNotice(o.ctx, o.callbacks, o.sessionID, eventstream.Envelope{
			Kind: eventstream.KindNotice, Notice: fmt.Sprintf("Background participant updates stopped: %v", err),
		}, "", nil)
	}
}

func (o *acpParticipantTasks) forward() error {
	reconnected, err := o.agent.sessionClient.Reconnect(o.ctx, appserver.ReconnectRequest{SessionID: o.sessionID, HistoryTurns: 1})
	if err != nil {
		return err
	}
	if reconnected.Subscription == nil {
		return errors.New("background participant approval feed is unavailable")
	}
	defer reconnected.Subscription.Close()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	approvalCtx, cancelApprovals := context.WithCancel(o.ctx)
	approvalDone := make(chan struct{})
	go func() {
		defer close(approvalDone)
		o.forwardApprovals(approvalCtx)
	}()
	defer func() { cancelApprovals(); <-approvalDone }()
	assembler := &appserver.FeedDeliveryAssembler{}
	filter := newACPNarrativeFilter(true)
	for {
		select {
		case <-o.ctx.Done():
			return o.ctx.Err()
		case <-ticker.C:
			if err := o.pollInputReceipts(o.ctx); err != nil {
				return err
			}
		case envelope, ok := <-o.mux.Events():
			if !ok {
				return nil
			}
			if err := o.agent.emitControlEnvelope(o.ctx, o.callbacks, o.sessionID, nil, envelope, filter); err != nil {
				return err
			}
		case delivery, ok := <-reconnected.Subscription.Deliveries():
			if !ok {
				if err := reconnected.Subscription.Err(); err != nil {
					return err
				}
				return errors.New("background participant approval feed ended")
			}
			events, _, err := assembler.Accept(delivery)
			if err != nil {
				return err
			}
			// Task streams own content. The Session feed only wakes an inspection of
			// the current approval head, never replays historical permission requests.
			inspect := delivery.Kind == appserver.FeedDeliverySync
			for _, env := range events {
				inspect = inspect || env.Kind == eventstream.KindRequestPermission || env.ApprovalRequestID != ""
			}
			if inspect {
				o.wakeApprovals()
			}
		}
	}
}
