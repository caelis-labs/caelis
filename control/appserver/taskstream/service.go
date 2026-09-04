// Package taskstream adapts Control-owned Task observation into the Envelope
// contract consumed by presentation clients. It owns no Task
// lifecycle, storage, authorization, cursor, or transport wire semantics.
package taskstream

import (
	"context"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
)

// Product Task semantics and directory DTOs have one owner in
// control/taskstream. This adapter aliases those values instead of maintaining
// a second ACP-shaped mirror.
type Principal = controltaskstream.Principal
type SourceClass = controltaskstream.SourceClass
type DeliveryKind = controltaskstream.DeliveryKind
type ParentTool = controltaskstream.ParentTool
type TaskDescriptor = controltaskstream.TaskDescriptor
type ListRequest = controltaskstream.ListRequest
type ListResult = controltaskstream.ListResult
type ReadRequest = controltaskstream.ReadRequest
type SubscribeRequest = controltaskstream.SubscribeRequest

const (
	SourceExact       = controltaskstream.SourceExact
	SourceReplacement = controltaskstream.SourceReplacement
	SourceStatus      = controltaskstream.SourceStatus

	DeliveryReplaceBegin = controltaskstream.DeliveryReplaceBegin
	DeliveryReplacePage  = controltaskstream.DeliveryReplacePage
	DeliveryReplaceEnd   = controltaskstream.DeliveryReplaceEnd
	DeliveryAppendPage   = controltaskstream.DeliveryAppendPage
	DeliveryStatus       = controltaskstream.DeliveryStatus
)

type Delivery struct {
	Kind       DeliveryKind           `json:"kind"`
	Source     SourceClass            `json:"source"`
	SnapshotID string                 `json:"snapshot_id,omitempty"`
	Page       uint32                 `json:"page,omitempty"`
	Events     []eventstream.Envelope `json:"events,omitempty"`
	NextCursor string                 `json:"next_cursor,omitempty"`
	ActivityID string                 `json:"activity_id,omitempty"`
}

type ReadResult struct {
	Deliveries []Delivery `json:"deliveries,omitempty"`
	ActivityID string     `json:"activity_id,omitempty"`
}

type Subscription interface {
	Deliveries() <-chan Delivery
	Close() error
	Err() error
}

type SubscribeResult struct {
	Subscription Subscription `json:"-"`
}

type Service interface {
	List(context.Context, Principal, ListRequest) (ListResult, error)
	Events(context.Context, Principal, ReadRequest) (ReadResult, error)
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

func (s *service) Events(ctx context.Context, principal Principal, req ReadRequest) (ReadResult, error) {
	result, err := s.control.Events(ctx, principal, req)
	if err != nil {
		return ReadResult{}, err
	}
	deliveries := make([]Delivery, 0, len(result.Deliveries))
	for _, delivery := range result.Deliveries {
		deliveries = append(deliveries, projectDelivery(delivery))
	}
	return ReadResult{Deliveries: deliveries, ActivityID: result.ActivityID}, nil
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
	return SubscribeResult{Subscription: sub}, nil
}

type subscription struct {
	ctx    context.Context
	cancel context.CancelFunc
	inner  controltaskstream.Subscription
	out    chan Delivery

	closeOnce sync.Once
}

func newSubscription(parent context.Context, inner controltaskstream.Subscription) *subscription {
	ctx, cancel := context.WithCancel(parent)
	sub := &subscription{ctx: ctx, cancel: cancel, inner: inner, out: make(chan Delivery)}
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
		case delivery, open := <-s.inner.Deliveries():
			if !open {
				return
			}
			projected := projectDelivery(delivery)
			select {
			case <-s.ctx.Done():
				return
			case s.out <- projected:
			}
		}
	}
}

func (s *subscription) Deliveries() <-chan Delivery { return s.out }

func (s *subscription) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.cancel()
		err = s.inner.Close()
	})
	return err
}

func (s *subscription) Err() error { return s.inner.Err() }

func projectDelivery(delivery controltaskstream.Delivery) Delivery {
	out := Delivery{
		Kind: delivery.Kind, Source: delivery.Source, SnapshotID: delivery.SnapshotID,
		Page: delivery.Page, NextCursor: delivery.NextCursor, ActivityID: delivery.ActivityID,
	}
	for _, record := range delivery.Records {
		out.Events = append(out.Events, projectRecord(record)...)
	}
	if delivery.Source == controltaskstream.SourceReplacement {
		for index := range out.Events {
			out.Events[index].Cursor = ""
			out.Events[index].Position = nil
		}
	}
	return out
}

func projectRecord(record controltaskstream.Record) []eventstream.Envelope {
	if record.Frame == nil {
		return nil
	}
	frame := *record.Frame
	request := taskFrameProjectionRequestFor(record.Task, frame)
	projected := projectTaskStreamFrame(request, frame)
	for index := range projected {
		projected[index] = stampEnvelope(record, projected[index])
	}
	return projected
}

func taskFrameProjectionRequestFor(descriptor controltaskstream.TaskDescriptor, frame controltaskstream.Frame) taskFrameProjectionRequest {
	toolName := strings.TrimSpace(descriptor.ParentTool.ToolName)
	if toolName == "" {
		switch descriptor.Kind {
		case task.KindSubagent:
			toolName = spawn.ToolName
		case task.KindCommand:
			toolName = shell.RunCommandToolName
		}
	}
	terminalID := firstString(frame.TerminalID, descriptor.CurrentTurnID)
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
		TaskID:            descriptor.TaskID,
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
