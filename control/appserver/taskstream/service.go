// Package taskstream adapts Control-owned Task observation into the Envelope
// contract consumed by presentation clients. It owns no Task lifecycle,
// storage, authorization, cursor, or transport wire semantics.
package taskstream

import (
	"context"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	sdkstream "github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

// Product Task semantics and directory DTOs have one owner in
// control/taskstream. This adapter aliases those values instead of maintaining
// a second ACP-shaped mirror.
type Principal = controltaskstream.Principal
type ResumeMode = controltaskstream.ResumeMode
type ParentTool = controltaskstream.ParentTool
type TaskDescriptor = controltaskstream.TaskDescriptor
type ListRequest = controltaskstream.ListRequest
type ListResult = controltaskstream.ListResult
type ReadRequest = controltaskstream.ReadRequest
type SubscribeRequest = controltaskstream.SubscribeRequest

const (
	ResumeModeExact        = controltaskstream.ResumeModeExact
	ResumeModeCurrentState = controltaskstream.ResumeModeCurrentState
)

var ErrSlowConsumer = controltaskstream.ErrSlowConsumer

// IsTransientGapEnvelope reports the recoverable Task-stream boundary emitted
// when an earlier process generation or retained output prefix is unavailable.
// Programmatic clients retain the typed fact and cursor; first-party human
// surfaces may silently continue from the advertised current state.
func IsTransientGapEnvelope(envelope eventstream.Envelope) bool {
	return envelope.Kind == eventstream.KindNotice &&
		metautil.Bool(envelope.Meta, "task_stream", "transient_gap")
}

type Batch struct {
	Events         []eventstream.Envelope `json:"events,omitempty"`
	ActivityID     string                 `json:"activity_id,omitempty"`
	ResumeMode     ResumeMode             `json:"resume_mode"`
	TransientGap   bool                   `json:"transient_gap,omitempty"`
	BoundaryCursor string                 `json:"boundary_cursor,omitempty"`
}

type Subscription interface {
	Events() <-chan eventstream.Envelope
	Close() error
	Err() error
	LastCursor() string
}

type SubscribeResult struct {
	Subscription   Subscription `json:"-"`
	ResumeMode     ResumeMode   `json:"resume_mode"`
	TransientGap   bool         `json:"transient_gap,omitempty"`
	BoundaryCursor string       `json:"boundary_cursor,omitempty"`
}

type Service interface {
	List(context.Context, Principal, ListRequest) (ListResult, error)
	Events(context.Context, Principal, ReadRequest) (Batch, error)
	Subscribe(context.Context, Principal, SubscribeRequest) (SubscribeResult, error)
}

type service struct {
	control controltaskstream.Service
}

func New(control controltaskstream.Service) Service {
	if control == nil {
		return nil
	}
	base := &service{control: control}
	if _, ok := control.(controltaskstream.DirectoryService); ok {
		return &serviceWithDirectory{service: base}
	}
	return base
}

func (s *service) List(ctx context.Context, principal Principal, req ListRequest) (ListResult, error) {
	return s.control.List(ctx, principal, req)
}

func (s *service) Events(ctx context.Context, principal Principal, req ReadRequest) (Batch, error) {
	result, err := s.control.Events(ctx, principal, req)
	if err != nil {
		return Batch{}, err
	}
	events := make([]eventstream.Envelope, 0, len(result.Records))
	for _, record := range result.Records {
		events = append(events, projectRecord(record)...)
	}
	return Batch{
		Events: events, ActivityID: result.ActivityID,
		ResumeMode: result.ResumeMode, TransientGap: result.TransientGap,
		BoundaryCursor: result.BoundaryCursor,
	}, nil
}

func (s *service) Subscribe(ctx context.Context, principal Principal, req SubscribeRequest) (SubscribeResult, error) {
	result, err := s.control.Subscribe(ctx, principal, req)
	if err != nil {
		return SubscribeResult{}, err
	}
	if result.Subscription == nil {
		return SubscribeResult{}, errorcode.New(errorcode.Unavailable, "taskstream: control subscription is unavailable")
	}
	sub := newSubscription(ctx, result.Subscription)
	return SubscribeResult{
		Subscription: sub, ResumeMode: result.ResumeMode, TransientGap: result.TransientGap,
		BoundaryCursor: result.BoundaryCursor,
	}, nil
}

type subscription struct {
	ctx    context.Context
	cancel context.CancelFunc
	inner  controltaskstream.Subscription
	out    chan eventstream.Envelope

	mu         sync.Mutex
	lastCursor string
	closeOnce  sync.Once
}

func newSubscription(parent context.Context, inner controltaskstream.Subscription) *subscription {
	ctx, cancel := context.WithCancel(parent)
	sub := &subscription{ctx: ctx, cancel: cancel, inner: inner, out: make(chan eventstream.Envelope)}
	go sub.forward()
	return sub
}

func (s *subscription) forward() {
	defer close(s.out)
	defer s.Close()
	for {
		select {
		case <-s.ctx.Done():
			return
		case record, open := <-s.inner.Records():
			if !open {
				return
			}
			for _, envelope := range projectRecord(record) {
				select {
				case <-s.ctx.Done():
					return
				case s.out <- envelope:
					s.mu.Lock()
					s.lastCursor = envelope.Cursor
					s.mu.Unlock()
				}
			}
		}
	}
}

func (s *subscription) Events() <-chan eventstream.Envelope { return s.out }

func (s *subscription) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.cancel()
		err = s.inner.Close()
	})
	return err
}

func (s *subscription) Err() error { return s.inner.Err() }

func (s *subscription) LastCursor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastCursor != "" {
		return s.lastCursor
	}
	return s.inner.LastCursor()
}

func projectRecord(record controltaskstream.Record) []eventstream.Envelope {
	if record.Gap != nil {
		return []eventstream.Envelope{stampEnvelope(record, gapEnvelope(record))}
	}
	if record.Frame == nil {
		return nil
	}
	frame := sdkstream.CloneFrame(*record.Frame)
	if frame.Ref.SessionID == "" {
		frame.Ref.SessionID = record.Task.SessionID
	}
	if frame.Ref.TaskID == "" {
		frame.Ref.TaskID = record.Task.TaskID
	}
	request := taskFrameProjectionRequestFor(record.Task, frame)
	projected := projectTaskStreamFrame(request, frame)
	for index := range projected {
		projected[index] = stampEnvelope(record, projected[index])
	}
	return projected
}

func taskFrameProjectionRequestFor(descriptor controltaskstream.TaskDescriptor, frame sdkstream.Frame) taskFrameProjectionRequest {
	toolName := strings.TrimSpace(descriptor.ParentTool.ToolName)
	if toolName == "" {
		switch descriptor.Kind {
		case task.KindSubagent:
			toolName = spawn.ToolName
		case task.KindCommand:
			toolName = shell.RunCommandToolName
		}
	}
	terminalID := firstString(frame.Ref.TerminalID, descriptor.CurrentTurnID)
	scope := eventstream.ScopeMain
	if descriptor.Kind == task.KindSubagent {
		scope = eventstream.ScopeSubagent
	}
	displayTerminalID := terminalID
	if descriptor.Kind == task.KindCommand && strings.TrimSpace(descriptor.ParentTool.ToolCallID) != "" {
		// A RunCommand Task stream is mounted on the parent ACP tool call.
		// The runtime terminal ID remains the physical stream address, while
		// stdio terminal output and exit metadata target the mounted call.
		displayTerminalID = strings.TrimSpace(descriptor.ParentTool.ToolCallID)
	}
	return taskFrameProjectionRequest{
		TurnID: terminalID, SessionID: descriptor.SessionID,
		CallID: descriptor.ParentTool.ToolCallID, ToolName: toolName, TaskHandle: descriptor.Handle,
		Ref:               sdkstream.Ref{SessionID: descriptor.SessionID, TaskID: descriptor.TaskID, TerminalID: terminalID},
		DisplayTerminalID: displayTerminalID, Scope: scope, ParticipantID: descriptor.ParticipantID,
	}
}

func stampEnvelope(record controltaskstream.Record, envelope eventstream.Envelope) eventstream.Envelope {
	envelope.Cursor = record.Cursor
	envelope.SessionID = record.Task.SessionID
	envelope.ActivityID = record.Task.ActivityID
	if envelope.Scope == "" {
		envelope.Scope = eventstream.ScopeMain
	}
	if record.Task.Kind == task.KindSubagent {
		envelope.Scope = eventstream.ScopeSubagent
		envelope.ScopeID = record.Task.TaskID
	}
	if envelope.ParentTool == nil && record.Task.ParentTool.ToolCallID != "" {
		envelope.ParentTool = &eventstream.ParentToolRelation{
			ToolCallID: record.Task.ParentTool.ToolCallID,
			ToolName:   record.Task.ParentTool.ToolName,
		}
	}
	envelope.Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
	envelope.Position = &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
		Generation: record.Generation, Sequence: record.Sequence,
	}}
	return envelope
}

func gapEnvelope(record controltaskstream.Record) eventstream.Envelope {
	scope := eventstream.ScopeMain
	scopeID := record.Task.SessionID
	if record.Task.Kind == task.KindSubagent {
		scope = eventstream.ScopeSubagent
		scopeID = record.Task.TaskID
	}
	return eventstream.Envelope{
		Kind: eventstream.KindNotice, SessionID: record.Task.SessionID,
		TurnID: record.Task.CurrentTurnID, Scope: scope, ScopeID: scopeID,
		Notice: "transient Task output before this boundary is no longer available",
		Meta: map[string]any{
			"task_stream": map[string]any{
				"task_id": record.Task.TaskID, "state": record.Task.State, "transient_gap": true,
			},
		},
	}
}

func firstString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

var _ Service = (*service)(nil)
var _ Subscription = (*subscription)(nil)
