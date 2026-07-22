// Package taskstream projects Control-owned Task stream records into ACP-shaped
// envelopes for presentation surfaces. It owns no Task lifecycle or storage.
package taskstream

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	sdkstream "github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/projector"
)

// Principal is authenticated product-wire context supplied by a Surface host.
type Principal struct {
	ID    string
	Roles []string
}

// ResumeMode describes whether the requested process-local observation
// boundary was retained.
type ResumeMode string

// ParentTool identifies the canonical parent tool call for one Task stream.
type ParentTool struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
}

// TaskDescriptor is the ACP-facing Task directory entry. It intentionally
// excludes transient output bodies.
type TaskDescriptor struct {
	SessionID      string     `json:"session_id"`
	TaskID         string     `json:"task_id"`
	Handle         string     `json:"handle"`
	AgentHandle    string     `json:"agent_handle,omitempty"`
	Kind           task.Kind  `json:"kind"`
	Title          string     `json:"title,omitempty"`
	State          task.State `json:"state"`
	Running        bool       `json:"running"`
	SupportsInput  bool       `json:"supports_input,omitempty"`
	SupportsCancel bool       `json:"supports_cancel,omitempty"`
	ParentTool     ParentTool `json:"parent_tool,omitempty"`
	ParticipantID  string     `json:"participant_id,omitempty"`
	CurrentTurnID  string     `json:"current_turn_id,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at,omitempty"`
}

// ListRequest selects the Task directory for one Session.
type ListRequest struct {
	SessionID string `json:"session_id"`
}

// ListResult is the ACP-facing Task directory.
type ListResult struct {
	Tasks []TaskDescriptor `json:"tasks,omitempty"`
}

// ReadRequest selects one Task stream and public Envelope cursor.
type ReadRequest struct {
	SessionID string `json:"session_id"`
	TaskID    string `json:"task_id"`
	Cursor    string `json:"cursor,omitempty"`
}

// SubscribeRequest selects one independently delivered Task stream.
type SubscribeRequest ReadRequest

const (
	ResumeModeExact        ResumeMode = "exact"
	ResumeModeCurrentState ResumeMode = "current_state"
)

var ErrSlowConsumer = controltaskstream.ErrSlowConsumer

type Batch struct {
	Events         []eventstream.Envelope `json:"events,omitempty"`
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
	return &service{control: control}
}

func (s *service) List(ctx context.Context, principal Principal, req ListRequest) (ListResult, error) {
	result, err := s.control.List(ctx, controlPrincipal(principal), controltaskstream.ListRequest{SessionID: req.SessionID})
	if err != nil {
		return ListResult{}, err
	}
	tasks := make([]TaskDescriptor, 0, len(result.Tasks))
	for _, descriptor := range result.Tasks {
		tasks = append(tasks, taskDescriptorFromControl(descriptor))
	}
	return ListResult{Tasks: tasks}, nil
}

func (s *service) Events(ctx context.Context, principal Principal, req ReadRequest) (Batch, error) {
	result, err := s.control.Events(ctx, controlPrincipal(principal), controltaskstream.ReadRequest{
		SessionID: req.SessionID, TaskID: req.TaskID, Cursor: req.Cursor,
	})
	if err != nil {
		return Batch{}, err
	}
	events := make([]eventstream.Envelope, 0, len(result.Records))
	for _, record := range result.Records {
		events = append(events, projectRecord(record)...)
	}
	return Batch{
		Events: events, ResumeMode: ResumeMode(result.ResumeMode), TransientGap: result.TransientGap,
		BoundaryCursor: result.BoundaryCursor,
	}, nil
}

func (s *service) Subscribe(ctx context.Context, principal Principal, req SubscribeRequest) (SubscribeResult, error) {
	result, err := s.control.Subscribe(ctx, controlPrincipal(principal), controltaskstream.SubscribeRequest{
		SessionID: req.SessionID, TaskID: req.TaskID, Cursor: req.Cursor,
	})
	if err != nil {
		return SubscribeResult{}, err
	}
	if result.Subscription == nil {
		return SubscribeResult{}, errorcode.New(errorcode.Unavailable, "taskstream: control subscription is unavailable")
	}
	sub := newSubscription(ctx, result.Subscription)
	return SubscribeResult{
		Subscription: sub, ResumeMode: ResumeMode(result.ResumeMode), TransientGap: result.TransientGap,
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
	request := projectorRequest(record.Task, frame)
	projected := projector.ProjectTaskStreamFrame(request, frame)
	for index := range projected {
		projected[index] = stampEnvelope(record, projected[index])
	}
	return projected
}

func projectorRequest(descriptor controltaskstream.TaskDescriptor, frame sdkstream.Frame) projector.StreamRequest {
	toolName := strings.TrimSpace(descriptor.ParentTool.ToolName)
	if toolName == "" {
		switch descriptor.Kind {
		case task.KindSubagent:
			toolName = identity.Spawn
		case task.KindCommand:
			toolName = identity.RunCommand
		}
	}
	terminalID := firstString(frame.Ref.TerminalID, descriptor.CurrentTurnID)
	scope := eventstream.ScopeMain
	if descriptor.Kind == task.KindSubagent {
		scope = eventstream.ScopeSubagent
	}
	return projector.StreamRequest{
		TurnID: terminalID, SessionRef: session.SessionRef{SessionID: descriptor.SessionID},
		SourceID: terminalID, CallID: descriptor.ParentTool.ToolCallID, ToolName: toolName,
		ParentCallID: descriptor.ParentTool.ToolCallID, ParentToolName: toolName, TaskHandle: descriptor.Handle,
		Ref:               sdkstream.Ref{SessionID: descriptor.SessionID, TaskID: descriptor.TaskID, TerminalID: terminalID},
		DisplayTerminalID: terminalID, Scope: scope, ParticipantID: descriptor.ParticipantID,
	}
}

func controlPrincipal(principal Principal) controltaskstream.Principal {
	return controltaskstream.Principal{ID: strings.TrimSpace(principal.ID), Roles: append([]string(nil), principal.Roles...)}
}

func taskDescriptorFromControl(descriptor controltaskstream.TaskDescriptor) TaskDescriptor {
	return TaskDescriptor{
		SessionID: descriptor.SessionID, TaskID: descriptor.TaskID, Kind: descriptor.Kind,
		Handle: descriptor.Handle, AgentHandle: descriptor.AgentHandle,
		Title: descriptor.Title, State: descriptor.State, Running: descriptor.Running,
		SupportsInput: descriptor.SupportsInput, SupportsCancel: descriptor.SupportsCancel,
		ParentTool: ParentTool{
			ToolCallID: descriptor.ParentTool.ToolCallID,
			ToolName:   descriptor.ParentTool.ToolName,
		},
		ParticipantID: descriptor.ParticipantID, CurrentTurnID: descriptor.CurrentTurnID,
		UpdatedAt: descriptor.UpdatedAt,
	}
}

func stampEnvelope(record controltaskstream.Record, envelope eventstream.Envelope) eventstream.Envelope {
	envelope.Cursor = record.Cursor
	envelope.SessionID = record.Task.SessionID
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
