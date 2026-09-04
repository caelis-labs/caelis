package taskstream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/control/streamspool"
)

const (
	maxDeliveryRecords    = 256
	maxDeliveryBytes      = 1 << 20
	maxReplacementRecords = 8192
	maxReplacementBytes   = 32 << 20
	fallbackPollInterval  = 50 * time.Millisecond
)

type Authorizer interface {
	AuthorizeTaskStream(context.Context, Principal, string) error
}

// SessionLoader supplies the durable parent Session required to reload one
// exact child endpoint through ACP session/load.
type SessionLoader interface {
	LoadSession(context.Context, session.LoadSessionRequest) (session.LoadedSession, error)
}

type Config struct {
	Tasks           task.Store
	Spool           streamspool.Store
	Sessions        SessionLoader
	Directory       *DirectoryIndex
	SubagentHistory subagent.HistoryRunner
	Authorizer      Authorizer
	Secret          []byte
}

type service struct {
	tasks           task.Store
	spool           streamspool.Store
	sessions        SessionLoader
	subagentHistory subagent.HistoryRunner
	directory       *DirectoryIndex
	authorizer      Authorizer
	cursors         cursorCodec
}

func New(config Config) (Service, error) {
	if config.Tasks == nil || config.Authorizer == nil {
		return nil, fmt.Errorf("taskstream: tasks and authorizer are required")
	}
	if len(config.Secret) < 32 {
		return nil, fmt.Errorf("taskstream: cursor secret must be at least 32 bytes")
	}
	return &service{
		tasks: config.Tasks, spool: config.Spool, sessions: config.Sessions,
		subagentHistory: config.SubagentHistory, directory: config.Directory,
		authorizer: config.Authorizer, cursors: cursorCodec{secret: append([]byte(nil), config.Secret...)},
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
		if entry != nil && strings.TrimSpace(entry.Session.SessionID) == sessionID {
			result.Tasks = append(result.Tasks, descriptorFromEntry(entry))
		}
	}
	return result, nil
}

type exactSource struct {
	key    streamspool.Key
	offset streamspool.Offset
	seq    uint64
	bounds streamspool.Bounds
}

func (s *service) Events(ctx context.Context, principal Principal, req ReadRequest) (ReadResult, error) {
	entry, point, cursorPresent, err := s.prepare(ctx, principal, req)
	if err != nil {
		return ReadResult{}, err
	}
	source, exact, err := s.selectExact(ctx, entry, point, cursorPresent)
	if err != nil {
		return ReadResult{}, err
	}
	if !exact {
		deliveries, fallbackErr := s.fallbackDeliveries(ctx, entry)
		return ReadResult{Deliveries: deliveries, ActivityID: descriptorFromEntry(entry).ActivityID}, fallbackErr
	}
	records, next, err := s.readAvailable(ctx, entry, source)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ReadResult{}, ctxErr
		}
		deliveries, fallbackErr := s.fallbackDeliveries(ctx, entry)
		return ReadResult{Deliveries: deliveries, ActivityID: descriptorFromEntry(entry).ActivityID}, fallbackErr
	}
	boundary, err := s.cursors.encode(entry.Session.SessionID, entry.TaskID, next)
	if err != nil {
		return ReadResult{}, err
	}
	return ReadResult{Deliveries: []Delivery{{
		Kind: DeliveryAppendPage, Source: SourceExact, Records: records,
		NextCursor: boundary, ActivityID: descriptorFromEntry(entry).ActivityID,
	}}, ActivityID: descriptorFromEntry(entry).ActivityID}, nil
}

func (s *service) Subscribe(ctx context.Context, principal Principal, req SubscribeRequest) (SubscribeResult, error) {
	entry, point, cursorPresent, err := s.prepare(ctx, principal, ReadRequest{
		SessionID: req.SessionID, TaskID: req.TaskID, Cursor: req.Cursor,
	})
	if err != nil {
		return SubscribeResult{}, err
	}
	source, exact, err := s.selectExact(ctx, entry, point, cursorPresent)
	if err != nil {
		return SubscribeResult{}, err
	}
	sub := newSubscription(ctx)
	if !exact {
		go s.forwardFallback(sub, entry, nil)
		return SubscribeResult{Subscription: sub}, nil
	}
	go s.forwardExact(sub, entry, source, req.Follow)
	return SubscribeResult{Subscription: sub}, nil
}

func (s *service) forwardExact(sub *subscription, entry *task.Entry, source exactSource, follow bool) {
	reader, err := s.spool.Reader(sub.ctx, source.key, source.offset)
	if err != nil {
		s.forwardFallback(sub, entry, err)
		return
	}
	defer reader.Close()
	defer sub.finish(nil)
	point := cursorPoint{Key: source.key, Offset: source.offset, Sequence: source.seq}
	for {
		raw, readErr := reader.Next(sub.ctx)
		if readErr != nil {
			if errors.Is(readErr, io.EOF) || errors.Is(readErr, context.Canceled) {
				sub.finish(nil)
				return
			}
			_ = reader.Close()
			s.forwardFallback(sub, entry, readErr)
			return
		}
		record, next, decodeErr := s.projectSpoolRecord(entry, raw, point)
		if decodeErr != nil {
			_ = reader.Close()
			s.forwardFallback(sub, entry, decodeErr)
			return
		}
		point = next
		if !sub.deliver(Delivery{
			Kind: DeliveryAppendPage, Source: SourceExact,
			Records: []Record{record}, NextCursor: record.Cursor,
			ActivityID: record.Task.ActivityID,
		}) {
			return
		}
		if record.Frame != nil && record.Frame.Closed && (!follow || entry.Kind != task.KindSubagent) {
			sub.finish(nil)
			return
		}
	}
}

func (s *service) forwardFallback(sub *subscription, entry *task.Entry, exactErr error) {
	if sub == nil {
		return
	}
	if ctxErr := sub.ctx.Err(); ctxErr != nil {
		sub.finish(ctxErr)
		return
	}
	current := task.CloneEntry(entry)
	if current != nil && current.Running {
		descriptor := descriptorFromEntry(current)
		if !sub.deliver(Delivery{
			Kind: DeliveryStatus, Source: SourceStatus,
			Records: []Record{{Sequence: 1, Task: descriptor}}, ActivityID: descriptor.ActivityID,
		}) {
			sub.finish(sub.ctx.Err())
			return
		}
		ticker := time.NewTicker(fallbackPollInterval)
		defer ticker.Stop()
		for current.Running {
			select {
			case <-sub.ctx.Done():
				sub.finish(sub.ctx.Err())
				return
			case <-ticker.C:
			}
			loaded, loadErr := s.tasks.Get(sub.ctx, current.TaskID)
			if loadErr != nil {
				sub.finish(errors.Join(s.exactReadError(exactErr), loadErr))
				return
			}
			if loaded == nil || loaded.Session.SessionID != current.Session.SessionID {
				sub.finish(errorcode.New(errorcode.PermissionDenied, "taskstream: Task moved outside the authorized session"))
				return
			}
			current = task.CloneEntry(loaded)
		}
	}
	deliveries, err := s.fallbackDeliveries(sub.ctx, current)
	if err != nil {
		sub.finish(errors.Join(s.exactReadError(exactErr), err))
		return
	}
	for _, delivery := range deliveries {
		if !sub.deliver(delivery) {
			sub.finish(sub.ctx.Err())
			return
		}
	}
	sub.finish(nil)
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
	if expected := strings.TrimSpace(req.ExpectedActivityID); expected != "" && expected != descriptorFromEntry(entry).ActivityID {
		return nil, cursorPoint{}, false, errorcode.New(errorcode.Conflict, "taskstream: Task activity changed before history read")
	}
	point, present, err := s.cursors.decode(sessionID, taskID, req.Cursor)
	if err != nil {
		return nil, cursorPoint{}, present, errorcode.Wrap(errorcode.InvalidArgument, "taskstream: invalid cursor", err)
	}
	return task.CloneEntry(entry), point, present, nil
}

func (s *service) selectExact(ctx context.Context, entry *task.Entry, point cursorPoint, cursorPresent bool) (exactSource, bool, error) {
	if s.spool == nil {
		return exactSource{}, false, nil
	}
	if cursorPresent {
		bounds, err := s.spool.Bounds(ctx, point.Key)
		if err != nil || point.Offset < bounds.Low || point.Offset > bounds.High {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return exactSource{}, false, ctxErr
			}
			// The signed cursor proved its product identity, but the spool is
			// cache-only. Missing, expired, poisoned, or cross-epoch bytes select
			// one atomic authoritative replacement instead of making the cursor a
			// second availability authority.
			return exactSource{}, false, nil
		}
		if bounds.State == streamspool.StatePoisoned || bounds.State == streamspool.StateStoreClosed || bounds.State == streamspool.StateEmptyTerminal {
			return exactSource{}, false, nil
		}
		return exactSource{key: point.Key, offset: point.Offset, seq: point.Sequence, bounds: bounds}, true, nil
	}
	logical := streamspool.LogicalKey{
		Namespace: streamspool.NamespaceTask,
		Digest:    streamspool.DigestStrings(entry.Session.SessionID, entry.TaskID),
	}
	key, bounds, err := s.spool.Resolve(ctx, logical)
	if err == nil {
		if !bounds.OriginComplete || bounds.Low != 0 || bounds.State == streamspool.StatePoisoned ||
			bounds.State == streamspool.StateStoreClosed || bounds.State == streamspool.StateEmptyTerminal {
			return exactSource{}, false, nil
		}
		return exactSource{key: key, bounds: bounds}, true, nil
	}
	return exactSource{}, false, nil
}

func (s *service) readAvailable(ctx context.Context, entry *task.Entry, source exactSource) ([]Record, cursorPoint, error) {
	point := cursorPoint{Key: source.key, Offset: source.offset, Sequence: source.seq}
	if source.offset == source.bounds.High {
		return nil, point, nil
	}
	reader, err := s.spool.Reader(ctx, source.key, source.offset)
	if err != nil {
		return nil, point, s.exactReadError(err)
	}
	defer reader.Close()
	records := make([]Record, 0, min(maxDeliveryRecords, int(source.bounds.High-source.offset)))
	encodedBytes := 0
	for point.Offset < source.bounds.High && len(records) < maxDeliveryRecords {
		raw, readErr := reader.Next(ctx)
		if readErr != nil {
			return nil, point, s.exactReadError(readErr)
		}
		record, next, decodeErr := s.projectSpoolRecord(entry, raw, point)
		if decodeErr != nil {
			return nil, point, decodeErr
		}
		rawRecord, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			return nil, point, marshalErr
		}
		if len(records) > 0 && encodedBytes+len(rawRecord) > maxDeliveryBytes {
			break
		}
		if len(rawRecord) > maxDeliveryBytes {
			return nil, point, errorcode.New(errorcode.ResourceExhausted, "taskstream: Task record exceeds delivery limit")
		}
		records = append(records, record)
		encodedBytes += len(rawRecord)
		point = next
	}
	return records, point, nil
}

func (s *service) projectSpoolRecord(entry *task.Entry, raw streamspool.Record, point cursorPoint) (Record, cursorPoint, error) {
	if raw.Type != taskOutputRecordType || raw.Offset != point.Offset {
		return Record{}, point, errorcode.New(errorcode.Unavailable, "taskstream: cached Task record is invalid")
	}
	var recorded recordedTaskOutput
	if err := json.Unmarshal(raw.Payload, &recorded); err != nil {
		return Record{}, point, errorcode.Wrap(errorcode.Unavailable, "taskstream: decode cached Task record", err)
	}
	point.Offset++
	point.Sequence++
	cursor, err := s.cursors.encode(entry.Session.SessionID, entry.TaskID, point)
	if err != nil {
		return Record{}, point, err
	}
	descriptor := descriptorFromEntry(entry)
	if recorded.ActivityID != "" {
		descriptor.ActivityID = strings.TrimSpace(recorded.ActivityID)
	}
	if recorded.TerminalID != "" {
		descriptor.CurrentTurnID = strings.TrimSpace(recorded.TerminalID)
	}
	event := recorded.Event
	if event.State != "" {
		descriptor.State = task.State(event.State)
	}
	descriptor.Running = event.Running && !task.IsTerminalState(task.State(event.State))
	if !event.OccurredAt.IsZero() {
		descriptor.UpdatedAt = event.OccurredAt
	}
	frame := Frame{
		TerminalID: recorded.TerminalID,
		Text:       event.Text, State: event.State, ActivityID: recorded.ActivityID,
		Running: event.Running, Closed: event.Closed, ExitCode: event.ExitCode,
		Event: session.CloneEvent(event.Event), UpdatedAt: event.OccurredAt,
	}
	return Record{
		Cursor: cursor, Generation: hex.EncodeToString(point.Key.Epoch[:]), Sequence: point.Sequence,
		Task: descriptor, Frame: &frame,
	}, point, nil
}

func (s *service) fallbackDeliveries(ctx context.Context, entry *task.Entry) ([]Delivery, error) {
	descriptor := descriptorFromEntry(entry)
	if entry.Running {
		return []Delivery{{Kind: DeliveryStatus, Source: SourceStatus, Records: []Record{{Sequence: 1, Task: descriptor}}, ActivityID: descriptor.ActivityID}}, nil
	}
	var snapshot fallbackSnapshot
	loadedACPHistory := false
	if entry.Kind == task.KindSubagent {
		loaded, err := s.loadDurableSubagentHistory(ctx, entry)
		if err == nil {
			if err := s.verifyFiniteSubagentRead(ctx, entry); err != nil {
				return nil, err
			}
			snapshot = loaded
			loadedACPHistory = true
		} else {
			snapshot = terminalSubagentFallbackSnapshot(entry)
		}
	} else {
		snapshot = terminalCommandFallbackSnapshot(entry)
	}
	records := recordsForSnapshot(entry, snapshot)
	if len(records) == 0 {
		return []Delivery{{Kind: DeliveryStatus, Source: SourceStatus, Records: []Record{{Sequence: 1, Task: descriptor}}, ActivityID: descriptor.ActivityID}}, nil
	}
	deliveries, err := replacementDeliveries(entry, records)
	if err == nil {
		return deliveries, nil
	}
	if loadedACPHistory && errorcode.Is(err, errorcode.ResourceExhausted) {
		fallback := terminalSubagentFallbackSnapshot(entry)
		records = recordsForSnapshot(entry, fallback)
		deliveries, err = replacementDeliveries(entry, records)
		if err == nil {
			return deliveries, nil
		}
	}
	if errorcode.Is(err, errorcode.ResourceExhausted) {
		return []Delivery{{Kind: DeliveryStatus, Source: SourceStatus, Records: []Record{{Sequence: 1, Task: descriptor}}, ActivityID: descriptor.ActivityID}}, nil
	}
	return nil, err
}

func recordsForSnapshot(entry *task.Entry, snapshot fallbackSnapshot) []Record {
	records := make([]Record, 0, len(snapshot.Frames)+1)
	for _, frame := range framesForFallback(snapshot) {
		cloned := cloneFrame(frame)
		records = append(records, Record{
			Sequence: uint64(len(records) + 1), Task: descriptorForFrame(entry, cloned), Frame: &cloned,
		})
	}
	return records
}

func replacementDeliveries(entry *task.Entry, records []Record) ([]Delivery, error) {
	if len(records) > maxReplacementRecords {
		return nil, errorcode.New(errorcode.ResourceExhausted, "taskstream: replacement exceeds record limit")
	}
	totalBytes := 0
	encoded := make([][]byte, len(records))
	for i, record := range records {
		raw, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		if len(raw) > maxDeliveryBytes || totalBytes+len(raw) > maxReplacementBytes {
			return nil, errorcode.New(errorcode.ResourceExhausted, "taskstream: replacement exceeds byte limit")
		}
		encoded[i] = raw
		totalBytes += len(raw)
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(strings.TrimSpace(entry.TaskID)))
	for _, raw := range encoded {
		_, _ = digest.Write(raw)
	}
	snapshotID := hex.EncodeToString(digest.Sum(nil))
	descriptor := descriptorFromEntry(entry)
	deliveries := []Delivery{{Kind: DeliveryReplaceBegin, Source: SourceReplacement, SnapshotID: snapshotID, ActivityID: descriptor.ActivityID}}
	page := uint32(0)
	for start := 0; start < len(records); {
		end := start
		pageBytes := 0
		for end < len(records) && end-start < maxDeliveryRecords {
			if end > start && pageBytes+len(encoded[end]) > maxDeliveryBytes {
				break
			}
			pageBytes += len(encoded[end])
			end++
		}
		deliveries = append(deliveries, Delivery{
			Kind: DeliveryReplacePage, Source: SourceReplacement, SnapshotID: snapshotID,
			Page: page, Records: append([]Record(nil), records[start:end]...), ActivityID: descriptor.ActivityID,
		})
		page++
		start = end
	}
	deliveries = append(deliveries, Delivery{Kind: DeliveryReplaceEnd, Source: SourceReplacement, SnapshotID: snapshotID, Page: page, ActivityID: descriptor.ActivityID})
	return deliveries, nil
}

func (s *service) verifyFiniteSubagentRead(ctx context.Context, before *task.Entry) error {
	if before == nil || before.Kind != task.KindSubagent || before.Running || !task.IsTerminalState(before.State) {
		return errorcode.New(errorcode.FailedPrecondition, "taskstream: finite subagent history requires a terminal activity")
	}
	after, err := s.tasks.Get(ctx, before.TaskID)
	if err != nil {
		return errorcode.Wrap(errorcode.Unavailable, "taskstream: verify finite subagent history activity", err)
	}
	if !sameFiniteSubagentActivity(before, after) {
		return errorcode.New(errorcode.Conflict, "taskstream: Task activity changed during history read")
	}
	return nil
}

func sameFiniteSubagentActivity(before, after *task.Entry) bool {
	if before == nil || after == nil || before.TaskID != after.TaskID ||
		before.Session.SessionID != after.Session.SessionID || after.Kind != task.KindSubagent ||
		after.Running || !task.IsTerminalState(after.State) {
		return false
	}
	beforeDescriptor := descriptorFromEntry(before)
	afterDescriptor := descriptorFromEntry(after)
	return before.State == after.State &&
		beforeDescriptor.ActivityID == afterDescriptor.ActivityID &&
		beforeDescriptor.CurrentTurnID == afterDescriptor.CurrentTurnID &&
		beforeDescriptor.Handle == afterDescriptor.Handle &&
		beforeDescriptor.AgentHandle == afterDescriptor.AgentHandle &&
		beforeDescriptor.ParticipantID == afterDescriptor.ParticipantID &&
		taskHistoryChildSessionID(before) == taskHistoryChildSessionID(after) &&
		reflect.DeepEqual(taskHistoryTarget(before), taskHistoryTarget(after))
}

func terminalCommandFallbackSnapshot(entry *task.Entry) fallbackSnapshot {
	if entry == nil {
		return fallbackSnapshot{}
	}
	descriptor := descriptorFromEntry(entry)
	text := strings.TrimSpace(mapString(entry.Result, "result"))
	if text == "" {
		text = strings.TrimSpace(mapString(entry.Result, "error"))
	}
	var exitCode *int
	if code, ok := integerValue(entry.Result["exit_code"]); ok {
		exitCode = &code
	}
	frame := Frame{
		TerminalID: descriptor.CurrentTurnID,
		Text:       text, State: string(entry.State), Closed: true, ExitCode: exitCode, UpdatedAt: entry.UpdatedAt,
	}
	return fallbackSnapshot{
		State: frame.State, Running: false, TerminalFramed: true,
		ExitCode: exitCode, UpdatedAt: entry.UpdatedAt, Frames: []Frame{frame}, FinalText: text,
	}
}

func integerValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), int64(int(typed)) == typed
	case float64:
		return int(typed), float64(int(typed)) == typed
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil && int64(int(parsed)) == parsed
	default:
		return 0, false
	}
}

func (s *service) exactReadError(err error) error {
	if err == nil {
		err = streamspool.ErrUnavailable
	}
	return errorcode.Wrap(errorcode.Unavailable, "taskstream: exact cached history is unavailable", err)
}

func descriptorForFrame(entry *task.Entry, frame Frame) TaskDescriptor {
	descriptor := descriptorFromEntry(entry)
	if activityID := strings.TrimSpace(frame.ActivityID); activityID != "" {
		descriptor.ActivityID = activityID
	}
	if state := strings.TrimSpace(frame.State); state != "" {
		descriptor.State = task.State(state)
	}
	descriptor.Running = frame.Running && !task.IsTerminalState(task.State(frame.State))
	if turnID := strings.TrimSpace(frame.TerminalID); turnID != "" {
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
			parentTool = spawn.ToolName
		case task.KindCommand:
			parentTool = shell.RunCommandToolName
		}
	}
	return TaskDescriptor{
		SessionID: strings.TrimSpace(entry.Session.SessionID), TaskID: strings.TrimSpace(entry.TaskID),
		Handle:      firstString(entry.Handle, mapString(entry.Metadata, "handle"), mapString(entry.Spec, "handle"), entry.TaskID),
		AgentHandle: firstString(mapString(entry.Metadata, "agent"), mapString(entry.Spec, "agent")),
		Kind:        entry.Kind, Title: strings.TrimSpace(entry.Title), State: entry.State, Running: entry.Running,
		SupportsInput: entry.Kind == task.KindCommand && entry.SupportsInput, SupportsCancel: entry.SupportsCancel,
		ParentTool:    ParentTool{ToolCallID: parentCall, ToolName: parentTool},
		ParticipantID: firstString(mapString(entry.Metadata, "agent_id"), mapString(entry.Spec, "agent_id")),
		ActivityID:    firstString(mapString(entry.Metadata, "child_activity_id"), mapString(entry.Spec, "child_activity_id")),
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
