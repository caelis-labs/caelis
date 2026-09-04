package acpagentbridge

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
)

func acpMuxCommandAnchor(handle string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "command-1",
			RawOutput: map[string]any{"handle": handle, "state": "running", "target_kind": "command"},
			Meta:      acpMuxCommandMeta(),
		},
	}
}

func acpMuxCommandMeta() map[string]any {
	meta := acpmeta.WithTerminalInfo(nil, "command-1")
	return acpmeta.WithToolName(meta, "RunCommand")
}

func acpMuxTerminalCommandAnchor(handle string) eventstream.Envelope {
	envelope := acpMuxCommandAnchor(handle)
	completed := eventstream.ToolStatusCompleted
	update := envelope.Update.(eventstream.ToolCallUpdate)
	update.Status = &completed
	update.RawOutput = map[string]any{"handle": handle, "state": "completed", "target_kind": "command"}
	envelope.Update = update
	return envelope
}

func acpMuxCommandOutputEnvelope(cursor string, output string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Cursor: cursor,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "command-1",
			Meta:          acpmeta.WithTerminalOutput(nil, "command-1", output),
		},
	}
}

func acpMuxSubagentAnchor(handle string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "spawn-1",
			RawOutput: map[string]any{
				"handle": handle, "state": "running", "target_kind": "subagent",
				"parent_call": "spawn-1", "parent_tool": "Spawn",
			},
			Meta: acpmeta.WithToolName(nil, "Spawn"),
		},
	}
}

func acpMuxSubagentLifecycleEnvelope(cursor string, turnID string, state string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind: eventstream.KindLifecycle, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
		ScopeID: "task-1", TurnID: turnID, Cursor: cursor,
		ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"},
		Lifecycle:  &eventstream.Lifecycle{State: state},
		Final:      eventstream.IsTerminalLifecycleState(state),
	}
}

func acpMuxSubagentMessageEnvelope(cursor string, turnID string, messageID string, text string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
		ScopeID: "task-1", TurnID: turnID, Cursor: cursor,
		ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"},
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage, MessageID: messageID,
			Content: eventstream.TextContent{Type: "text", Text: text},
		},
	}
}

func receiveACPTaskStreamRequest(t *testing.T, requests <-chan taskstream.SubscribeRequest) taskstream.SubscribeRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(time.Second):
		t.Fatal("Task stream Subscribe request was not received")
		return taskstream.SubscribeRequest{}
	}
}

type acpMuxTestService struct {
	mu             sync.Mutex
	requests       chan taskstream.SubscribeRequest
	sub            *acpMuxTestSubscription
	list           taskstream.ListResult
	listErr        error
	err            error
	subscribeCalls int
	subscribeStart chan struct{}
	subscribeGate  <-chan struct{}
}

type acpMuxRetryService struct {
	mu         sync.Mutex
	listCalls  int
	requests   chan taskstream.SubscribeRequest
	sub        *acpMuxTestSubscription
	descriptor taskstream.TaskDescriptor
}

type acpMuxSubscribeStep struct {
	sub           *acpMuxTestSubscription
	err           error
	started       chan<- struct{}
	release       <-chan struct{}
	cancelled     chan<- struct{}
	closeOnCancel bool
}

type acpMuxReconnectService struct {
	mu        sync.Mutex
	steps     []acpMuxSubscribeStep
	next      int
	handle    string
	listCalls int
	requests  chan taskstream.SubscribeRequest
}

type acpMuxPromptCallbacks struct {
	updates chan eventstream.SessionNotification
}

func (c *acpMuxPromptCallbacks) SessionUpdate(_ context.Context, notification eventstream.SessionNotification) error {
	c.updates <- notification
	return nil
}

func (*acpMuxPromptCallbacks) RequestPermission(context.Context, acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	return acpsdk.RequestPermissionResponse{}, nil
}

func (s *acpMuxRetryService) List(context.Context, taskstream.Principal, taskstream.ListRequest) (taskstream.ListResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls++
	if s.listCalls == 1 {
		return taskstream.ListResult{}, nil
	}
	return taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{s.descriptor}}, nil
}

func (*acpMuxRetryService) Events(context.Context, taskstream.Principal, taskstream.ReadRequest) (taskstream.ReadResult, error) {
	return taskstream.ReadResult{}, nil
}

func (s *acpMuxRetryService) Subscribe(_ context.Context, _ taskstream.Principal, request taskstream.SubscribeRequest) (taskstream.SubscribeResult, error) {
	s.requests <- request
	return taskstream.SubscribeResult{Subscription: s.sub}, nil
}

func newACPMuxReconnectService(steps []acpMuxSubscribeStep) *acpMuxReconnectService {
	return &acpMuxReconnectService{
		steps:    append([]acpMuxSubscribeStep(nil), steps...),
		requests: make(chan taskstream.SubscribeRequest, len(steps)+1),
	}
}

func (s *acpMuxReconnectService) List(context.Context, taskstream.Principal, taskstream.ListRequest) (taskstream.ListResult, error) {
	s.mu.Lock()
	s.listCalls++
	handle := s.handle
	s.mu.Unlock()
	if handle == "" {
		handle = "command"
	}
	return taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
		SessionID: "session-1", TaskID: "task-1", Handle: handle, Kind: task.KindCommand,
		State: task.StateRunning, Running: true,
		ParentTool: taskstream.ParentTool{ToolCallID: "command-1", ToolName: "RunCommand"},
	}}}, nil
}

func (*acpMuxReconnectService) Events(context.Context, taskstream.Principal, taskstream.ReadRequest) (taskstream.ReadResult, error) {
	return taskstream.ReadResult{}, nil
}

func (s *acpMuxReconnectService) Subscribe(ctx context.Context, _ taskstream.Principal, request taskstream.SubscribeRequest) (taskstream.SubscribeResult, error) {
	s.requests <- request
	s.mu.Lock()
	if s.next >= len(s.steps) {
		s.mu.Unlock()
		return taskstream.SubscribeResult{}, errorcode.New(errorcode.Unavailable, "unexpected extra Task stream subscription")
	}
	step := s.steps[s.next]
	s.next++
	s.mu.Unlock()
	if step.started != nil {
		step.started <- struct{}{}
	}
	if step.release != nil {
		select {
		case <-ctx.Done():
			if step.cancelled != nil {
				step.cancelled <- struct{}{}
			}
			return taskstream.SubscribeResult{}, ctx.Err()
		case <-step.release:
		}
	}
	if step.err != nil {
		return taskstream.SubscribeResult{}, step.err
	}
	if step.closeOnCancel {
		go func() {
			<-ctx.Done()
			step.sub.finish(ctx.Err())
			if step.cancelled != nil {
				step.cancelled <- struct{}{}
			}
		}()
	}
	return taskstream.SubscribeResult{Subscription: step.sub}, nil
}

func (s *acpMuxReconnectService) listCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listCalls
}

func (s *acpMuxReconnectService) subscribeCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next
}

func (s *acpMuxTestService) List(context.Context, taskstream.Principal, taskstream.ListRequest) (taskstream.ListResult, error) {
	return s.list, s.listErr
}

func (s *acpMuxTestService) Events(context.Context, taskstream.Principal, taskstream.ReadRequest) (taskstream.ReadResult, error) {
	return taskstream.ReadResult{}, nil
}

func (s *acpMuxTestService) Subscribe(_ context.Context, _ taskstream.Principal, request taskstream.SubscribeRequest) (taskstream.SubscribeResult, error) {
	s.mu.Lock()
	s.subscribeCalls++
	err := s.err
	sub := s.sub
	started := s.subscribeStart
	gate := s.subscribeGate
	s.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if gate != nil {
		<-gate
	}
	if s.requests != nil {
		s.requests <- request
	}
	if err != nil {
		return taskstream.SubscribeResult{}, err
	}
	return taskstream.SubscribeResult{Subscription: sub}, nil
}

func (s *acpMuxTestService) setSubscriptionResult(err error, sub *acpMuxTestSubscription) {
	s.mu.Lock()
	s.err = err
	s.sub = sub
	s.mu.Unlock()
}

func (s *acpMuxTestService) subscribeCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subscribeCalls
}

type acpMuxTestSubscription struct {
	events     chan eventstream.Envelope
	deliveries chan taskstream.Delivery
	startOnce  sync.Once
	once       sync.Once
	mu         sync.Mutex
	done       bool
	err        error
}

type acpMuxControlService struct {
	requests chan controltaskstream.SubscribeRequest
	sub      *acpMuxControlSubscription
	list     controltaskstream.ListResult
}

func (s *acpMuxControlService) List(context.Context, controltaskstream.Principal, controltaskstream.ListRequest) (controltaskstream.ListResult, error) {
	return s.list, nil
}

func (*acpMuxControlService) Events(context.Context, controltaskstream.Principal, controltaskstream.ReadRequest) (controltaskstream.ReadResult, error) {
	return controltaskstream.ReadResult{}, nil
}

func (s *acpMuxControlService) Subscribe(_ context.Context, _ controltaskstream.Principal, request controltaskstream.SubscribeRequest) (controltaskstream.SubscribeResult, error) {
	s.requests <- request
	return controltaskstream.SubscribeResult{Subscription: s.sub}, nil
}

type acpMuxControlSubscription struct {
	records    chan controltaskstream.Record
	deliveries chan controltaskstream.Delivery
	startOnce  sync.Once
	once       sync.Once
}

func (s *acpMuxControlSubscription) Deliveries() <-chan controltaskstream.Delivery {
	s.startOnce.Do(func() {
		s.deliveries = make(chan controltaskstream.Delivery)
		go func() {
			defer close(s.deliveries)
			for record := range s.records {
				s.deliveries <- controltaskstream.Delivery{
					Kind: controltaskstream.DeliveryAppendPage, Source: controltaskstream.SourceExact,
					Records: []controltaskstream.Record{record}, NextCursor: record.Cursor,
				}
			}
		}()
	})
	return s.deliveries
}
func (*acpMuxControlSubscription) Err() error { return nil }
func (s *acpMuxControlSubscription) Close() error {
	s.once.Do(func() { close(s.records) })
	return nil
}

func (s *acpMuxTestSubscription) Deliveries() <-chan taskstream.Delivery {
	s.mu.Lock()
	if s.deliveries != nil {
		deliveries := s.deliveries
		s.mu.Unlock()
		return deliveries
	}
	s.mu.Unlock()
	s.startOnce.Do(func() {
		deliveries := make(chan taskstream.Delivery)
		s.mu.Lock()
		s.deliveries = deliveries
		events := s.events
		s.mu.Unlock()
		go func() {
			defer close(deliveries)
			sequence := uint64(0)
			for envelope := range events {
				sequence++
				envelope = acpMuxExactEnvelope(envelope, sequence)
				cursor := envelope.Cursor
				deliveries <- taskstream.Delivery{
					Kind: taskstream.DeliveryAppendPage, Source: taskstream.SourceExact,
					Events: []eventstream.Envelope{envelope}, NextCursor: cursor,
				}
			}
		}()
	})
	s.mu.Lock()
	deliveries := s.deliveries
	s.mu.Unlock()
	return deliveries
}

func acpMuxExactEnvelope(envelope eventstream.Envelope, sequence uint64) eventstream.Envelope {
	if envelope.Cursor == "" {
		envelope.Cursor = fmt.Sprintf("acp-mux-cursor-%d", sequence)
	}
	if envelope.Position == nil {
		envelope.Position = &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
			Generation: "acp-mux-test", Sequence: sequence,
		}}
	}
	if envelope.Delivery == nil {
		envelope.Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
	}
	return envelope
}
func (s *acpMuxTestSubscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *acpMuxTestSubscription) Close() error {
	s.finish(nil)
	return nil
}

func (s *acpMuxTestSubscription) finish(err error) {
	s.once.Do(func() {
		s.mu.Lock()
		s.done = true
		s.err = err
		events := s.events
		deliveries := s.deliveries
		rawDeliveries := events == nil && deliveries != nil
		s.mu.Unlock()
		if events != nil {
			close(events)
		}
		if rawDeliveries {
			close(deliveries)
		}
	})
}

func (s *acpMuxTestSubscription) closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}
