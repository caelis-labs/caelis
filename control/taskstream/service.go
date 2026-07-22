package taskstream

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

type Authorizer interface {
	AuthorizeTaskStream(context.Context, Principal, string) error
}

type Config struct {
	Tasks      task.Store
	Streams    func() stream.Service
	Authorizer Authorizer
	Secret     []byte
	Generation string
}

type service struct {
	tasks      task.Store
	streams    func() stream.Service
	authorizer Authorizer
	cursors    cursorCodec
}

func New(config Config) (Service, error) {
	if config.Tasks == nil || config.Streams == nil || config.Authorizer == nil {
		return nil, fmt.Errorf("taskstream: tasks, streams, and authorizer are required")
	}
	if len(config.Secret) < 32 {
		return nil, fmt.Errorf("taskstream: cursor secret must be at least 32 bytes")
	}
	generation := strings.TrimSpace(config.Generation)
	if generation == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, fmt.Errorf("taskstream: generate process generation: %w", err)
		}
		generation = base64.RawURLEncoding.EncodeToString(raw[:])
	}
	return &service{
		tasks: config.Tasks, streams: config.Streams, authorizer: config.Authorizer,
		cursors: cursorCodec{secret: append([]byte(nil), config.Secret...), generation: generation},
	}, nil
}

func (s *service) List(ctx context.Context, principal Principal, req ListRequest) (ListResult, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if err := s.authorize(ctx, principal, sessionID); err != nil {
		return ListResult{}, err
	}
	entries, err := s.tasks.ListSession(ctx, session.SessionRef{SessionID: sessionID})
	if err != nil {
		return ListResult{}, err
	}
	result := ListResult{Tasks: make([]TaskDescriptor, 0, len(entries))}
	for _, entry := range entries {
		if entry == nil || strings.TrimSpace(entry.Session.SessionID) != sessionID {
			continue
		}
		result.Tasks = append(result.Tasks, descriptorFromEntry(entry))
	}
	return result, nil
}

func (s *service) Events(ctx context.Context, principal Principal, req ReadRequest) (Batch, error) {
	entry, point, sameGeneration, err := s.prepare(ctx, principal, req)
	if err != nil {
		return Batch{}, err
	}
	snapshot, events, mode, gap, point, err := s.initialRead(ctx, entry, point, sameGeneration)
	if err != nil {
		return Batch{}, err
	}
	point.Cursor = stream.CloneCursor(snapshot.Cursor)
	boundary, err := s.cursors.encode(entry.Session.SessionID, entry.TaskID, point)
	if err != nil {
		return Batch{}, err
	}
	return Batch{Records: events, ResumeMode: mode, TransientGap: gap, BoundaryCursor: boundary}, nil
}

func (s *service) Subscribe(ctx context.Context, principal Principal, req SubscribeRequest) (SubscribeResult, error) {
	entry, point, sameGeneration, err := s.prepare(ctx, principal, req)
	if err != nil {
		return SubscribeResult{}, err
	}
	snapshot, initial, mode, gap, point, err := s.initialRead(ctx, entry, point, sameGeneration)
	if err != nil {
		return SubscribeResult{}, err
	}
	point.Cursor = stream.CloneCursor(snapshot.Cursor)
	boundary, err := s.cursors.encode(entry.Session.SessionID, entry.TaskID, point)
	if err != nil {
		return SubscribeResult{}, err
	}
	sub := newSubscription(ctx)
	streams := s.streams()
	if streams == nil {
		_ = sub.Close()
		return SubscribeResult{}, errorcode.New(errorcode.Unavailable, "taskstream: runtime streams are unavailable")
	}
	go s.forward(sub, streams, entry, point, initial)
	return SubscribeResult{
		Subscription: sub, ResumeMode: mode, TransientGap: gap, BoundaryCursor: boundary,
	}, nil
}

func (s *service) forward(sub *subscription, streams stream.Service, entry *task.Entry, point cursorPoint, initial []Record) {
	defer sub.finish(nil)
	for _, record := range initial {
		if !sub.enqueue(record) {
			return
		}
	}
	req := stream.SubscribeRequest{
		Ref:    stream.Ref{SessionID: entry.Session.SessionID, TaskID: entry.TaskID},
		Cursor: point.Cursor, FollowContinues: entry.Kind == task.KindSubagent,
	}
	for frame, err := range streams.Subscribe(sub.ctx, req) {
		if err != nil {
			sub.finish(err)
			return
		}
		if frame == nil {
			continue
		}
		descriptor := descriptorForFrame(entry, *frame)
		if frame.EventsTruncatedBefore > point.Cursor.Events || frame.TruncatedBefore > point.Cursor.Output {
			point.Cursor.Events = max(point.Cursor.Events, frame.EventsTruncatedBefore)
			point.Cursor.Output = max(point.Cursor.Output, frame.TruncatedBefore)
			gap, next, stampErr := s.gapRecord(entry, descriptor, point)
			if stampErr != nil || !sub.enqueue(gap) {
				sub.finish(stampErr)
				return
			}
			point = next
		}
		records, next, projectErr := s.recordFrame(entry, descriptor, *frame, point)
		if projectErr != nil {
			sub.finish(projectErr)
			return
		}
		point = next
		for _, record := range records {
			if !sub.enqueue(record) {
				return
			}
		}
	}
}

func (s *service) prepare(ctx context.Context, principal Principal, req ReadRequest) (*task.Entry, cursorPoint, bool, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	taskID := strings.TrimSpace(req.TaskID)
	if err := s.authorize(ctx, principal, sessionID); err != nil {
		return nil, cursorPoint{}, false, err
	}
	if taskID == "" {
		return nil, cursorPoint{}, false, errorcode.New(errorcode.InvalidArgument, "taskstream: task_id is required")
	}
	entry, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return nil, cursorPoint{}, false, err
	}
	if entry == nil || strings.TrimSpace(entry.Session.SessionID) != sessionID {
		return nil, cursorPoint{}, false, errorcode.New(errorcode.PermissionDenied, "taskstream: task is not visible in this session")
	}
	point, sameGeneration, err := s.cursors.decode(sessionID, taskID, req.Cursor)
	if err != nil {
		return nil, cursorPoint{}, false, errorcode.Wrap(errorcode.InvalidArgument, "taskstream: invalid cursor", err)
	}
	return task.CloneEntry(entry), point, sameGeneration, nil
}

func (s *service) initialRead(ctx context.Context, entry *task.Entry, point cursorPoint, sameGeneration bool) (stream.Snapshot, []Record, ResumeMode, bool, cursorPoint, error) {
	streams := s.streams()
	if streams == nil {
		return stream.Snapshot{}, nil, "", false, point, errorcode.New(errorcode.Unavailable, "taskstream: runtime streams are unavailable")
	}
	readCursor := stream.CloneCursor(point.Cursor)
	mode := ResumeModeExact
	gap := false
	if !sameGeneration {
		readCursor = stream.Cursor{Output: math.MaxInt64, Events: math.MaxInt64}
		mode = ResumeModeCurrentState
		gap = true
	}
	snapshot, err := streams.Read(ctx, stream.ReadRequest{
		Ref: stream.Ref{SessionID: entry.Session.SessionID, TaskID: entry.TaskID}, Cursor: readCursor,
	})
	if err != nil {
		return stream.Snapshot{}, nil, "", false, point, err
	}
	if snapshot.EventsTruncatedBefore > readCursor.Events || snapshot.TruncatedBefore > readCursor.Output {
		mode = ResumeModeCurrentState
		gap = true
	}
	descriptor := descriptorForSnapshot(entry, snapshot)
	events := make([]Record, 0, len(snapshot.Frames)+1)
	if gap {
		if !sameGeneration {
			point.Cursor = stream.CloneCursor(snapshot.Cursor)
		} else {
			point.Cursor.Events = max(point.Cursor.Events, snapshot.EventsTruncatedBefore)
			point.Cursor.Output = max(point.Cursor.Output, snapshot.TruncatedBefore)
		}
		var record Record
		record, point, err = s.gapRecord(entry, descriptor, point)
		if err != nil {
			return stream.Snapshot{}, nil, "", false, point, err
		}
		events = append(events, record)
	}
	if !sameGeneration {
		point.Cursor = stream.CloneCursor(snapshot.Cursor)
		if stream.IsTerminalState(snapshot.State) {
			frame := stream.Frame{
				Ref:   stream.Ref{SessionID: entry.Session.SessionID, TaskID: entry.TaskID, TerminalID: descriptor.CurrentTurnID},
				State: snapshot.State, Cursor: snapshot.Cursor, Closed: true, UpdatedAt: snapshot.UpdatedAt,
			}
			var projected []Record
			projected, point, err = s.recordFrame(entry, descriptor, frame, point)
			if err != nil {
				return stream.Snapshot{}, nil, "", false, point, err
			}
			events = append(events, projected...)
		}
		return snapshot, events, mode, gap, point, nil
	}
	for _, frame := range stream.FramesForSnapshot(snapshot) {
		if frame.EventsTruncatedBefore > point.Cursor.Events || frame.TruncatedBefore > point.Cursor.Output {
			point.Cursor.Events = max(point.Cursor.Events, frame.EventsTruncatedBefore)
			point.Cursor.Output = max(point.Cursor.Output, frame.TruncatedBefore)
			gapRecord, next, gapErr := s.gapRecord(entry, descriptor, point)
			if gapErr != nil {
				return stream.Snapshot{}, nil, "", false, point, gapErr
			}
			point = next
			events = append(events, gapRecord)
			mode = ResumeModeCurrentState
			gap = true
		}
		projected, next, projectErr := s.recordFrame(entry, descriptor, frame, point)
		if projectErr != nil {
			return stream.Snapshot{}, nil, "", false, point, projectErr
		}
		point = next
		events = append(events, projected...)
	}
	return snapshot, events, mode, gap, point, nil
}

func (s *service) recordFrame(entry *task.Entry, descriptor TaskDescriptor, frame stream.Frame, point cursorPoint) ([]Record, cursorPoint, error) {
	point.Cursor = stream.CloneCursor(frame.Cursor)
	if point.Cursor.Output == 0 && point.Cursor.Events == 0 {
		return nil, point, nil
	}
	point.Sequence++
	cursor, err := s.cursors.encode(entry.Session.SessionID, entry.TaskID, point)
	if err != nil {
		return nil, point, err
	}
	cloned := stream.CloneFrame(frame)
	return []Record{{
		Cursor: cursor, Generation: s.cursors.generation, Sequence: point.Sequence,
		Task: descriptor, Frame: &cloned,
	}}, point, nil
}

func (s *service) gapRecord(entry *task.Entry, descriptor TaskDescriptor, point cursorPoint) (Record, cursorPoint, error) {
	point.Sequence++
	cursor, err := s.cursors.encode(entry.Session.SessionID, entry.TaskID, point)
	if err != nil {
		return Record{}, point, err
	}
	return Record{
		Cursor: cursor, Generation: s.cursors.generation, Sequence: point.Sequence, Task: descriptor,
		Gap: &Gap{SessionID: descriptor.SessionID, TaskID: descriptor.TaskID, Kind: descriptor.Kind, State: descriptor.State},
	}, point, nil
}

func descriptorForSnapshot(entry *task.Entry, snapshot stream.Snapshot) TaskDescriptor {
	descriptor := descriptorFromEntry(entry)
	if state := strings.TrimSpace(snapshot.State); state != "" {
		descriptor.State = task.State(state)
	}
	descriptor.Running = snapshot.Running
	descriptor.SupportsInput = snapshot.SupportsInput
	if turnID := strings.TrimSpace(snapshot.Ref.TerminalID); turnID != "" {
		descriptor.CurrentTurnID = turnID
	}
	if !snapshot.UpdatedAt.IsZero() {
		descriptor.UpdatedAt = snapshot.UpdatedAt
	}
	return descriptor
}

func descriptorForFrame(entry *task.Entry, frame stream.Frame) TaskDescriptor {
	descriptor := descriptorFromEntry(entry)
	state := strings.TrimSpace(frame.State)
	switch {
	case state != "":
		descriptor.State = task.State(state)
		descriptor.Running = frame.Running
		descriptor.SupportsInput = entry != nil && entry.Kind == task.KindSubagent &&
			!frame.Running && descriptor.State == task.StateCompleted
	case frame.Running:
		descriptor.State = task.StateRunning
		descriptor.Running = true
		descriptor.SupportsInput = false
	case frame.Closed:
		descriptor.Running = false
	}
	if turnID := strings.TrimSpace(frame.Ref.TerminalID); turnID != "" {
		descriptor.CurrentTurnID = turnID
	}
	if !frame.UpdatedAt.IsZero() {
		descriptor.UpdatedAt = frame.UpdatedAt
	}
	return descriptor
}

func (s *service) authorize(ctx context.Context, principal Principal, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return errorcode.New(errorcode.InvalidArgument, "taskstream: session_id is required")
	}
	return s.authorizer.AuthorizeTaskStream(ctx, principal, sessionID)
}

func descriptorFromEntry(entry *task.Entry) TaskDescriptor {
	if entry == nil {
		return TaskDescriptor{}
	}
	parentCall := firstString(mapString(entry.Metadata, "parent_call"), mapString(entry.Spec, "parent_call"))
	parentTool := firstString(mapString(entry.Metadata, "parent_tool"), mapString(entry.Spec, "parent_tool"))
	if parentTool == "" {
		switch entry.Kind {
		case task.KindSubagent:
			parentTool = identity.Spawn
		case task.KindCommand:
			parentTool = identity.RunCommand
		}
	}
	return TaskDescriptor{
		SessionID: strings.TrimSpace(entry.Session.SessionID), TaskID: strings.TrimSpace(entry.TaskID),
		Handle:      firstString(entry.Handle, mapString(entry.Metadata, "handle"), mapString(entry.Spec, "handle"), entry.TaskID),
		AgentHandle: firstString(mapString(entry.Metadata, "agent"), mapString(entry.Spec, "agent")),
		Kind:        entry.Kind, Title: strings.TrimSpace(entry.Title), State: entry.State, Running: entry.Running,
		SupportsInput: entry.SupportsInput, SupportsCancel: entry.SupportsCancel,
		ParentTool:    ParentTool{ToolCallID: parentCall, ToolName: parentTool},
		ParticipantID: firstString(mapString(entry.Metadata, "agent_id"), mapString(entry.Spec, "agent_id")),
		CurrentTurnID: firstString(mapString(entry.Metadata, "turn_id"), mapString(entry.Spec, "turn_id"), entry.Terminal.TerminalID),
		UpdatedAt:     entry.UpdatedAt,
	}
}

func mapString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
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
