package taskstream

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/task"
)

const directoryChangeCoalesceWindow = 10 * time.Millisecond

// DirectoryIndex fans committed status changes out to Session-scoped
// observers. It retains no Task content and no Session without observers.
type DirectoryIndex struct {
	mu       sync.Mutex
	sessions map[string]*directoryIndexSession
}

type directoryIndexSession struct {
	revision uint64
	nextID   uint64
	status   map[string]string
	watchers map[uint64]chan struct{}
}

// NewDirectoryIndex creates one process-scoped lightweight Task status index.
func NewDirectoryIndex() *DirectoryIndex {
	return &DirectoryIndex{sessions: map[string]*directoryIndexSession{}}
}

// Notify records one committed Task entry. Output-only revision changes are
// folded away; lifecycle, activity identity, routing identity, and capability
// changes wake every current observer independently.
func (i *DirectoryIndex) Notify(entry *task.Entry) {
	if i == nil || entry == nil {
		return
	}
	sessionID := strings.TrimSpace(entry.Session.SessionID)
	taskID := strings.TrimSpace(entry.TaskID)
	if sessionID == "" || taskID == "" {
		return
	}
	i.mu.Lock()
	state := i.sessions[sessionID]
	if state == nil {
		i.mu.Unlock()
		return
	}
	// Keep the unobserved hot path allocation-free: output commits are common,
	// while the directory exists only for Sessions with active observers.
	status := directoryStatusFingerprint(entry)
	if state.status[taskID] == status {
		i.mu.Unlock()
		return
	}
	state.status[taskID] = status
	state.revision++
	for _, watcher := range state.watchers {
		select {
		case watcher <- struct{}{}:
		default:
		}
	}
	i.mu.Unlock()
}

func directoryStatusFingerprint(entry *task.Entry) string {
	return directoryDescriptorFingerprint(descriptorFromEntry(entry))
}

func directoryDescriptorFingerprint(descriptor TaskDescriptor) string {
	return strings.Join([]string{
		descriptor.TaskID,
		descriptor.Handle,
		descriptor.AgentHandle,
		string(descriptor.Kind),
		descriptor.Title,
		string(descriptor.State),
		boolString(descriptor.Running),
		boolString(descriptor.SupportsInput),
		boolString(descriptor.SupportsCancel),
		descriptor.ParentTool.ToolCallID,
		descriptor.ParentTool.ToolName,
		descriptor.ParticipantID,
		descriptor.CurrentTurnID,
		descriptor.ActivityID,
	}, "\x00")
}

func boolString(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func (i *DirectoryIndex) subscribe(sessionID string) (uint64, <-chan struct{}, uint64, func()) {
	sessionID = strings.TrimSpace(sessionID)
	i.mu.Lock()
	state := i.sessions[sessionID]
	if state == nil {
		state = &directoryIndexSession{
			revision: 1,
			status:   map[string]string{},
			watchers: map[uint64]chan struct{}{},
		}
		i.sessions[sessionID] = state
	}
	state.nextID++
	id := state.nextID
	signal := make(chan struct{}, 1)
	state.watchers[id] = signal
	revision := state.revision
	i.mu.Unlock()
	var once sync.Once
	return id, signal, revision, func() {
		once.Do(func() {
			i.mu.Lock()
			current := i.sessions[sessionID]
			if current != nil {
				delete(current.watchers, id)
				if len(current.watchers) == 0 {
					delete(i.sessions, sessionID)
				}
			}
			i.mu.Unlock()
		})
	}
}

func (i *DirectoryIndex) revision(sessionID string) uint64 {
	if i == nil {
		return 0
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if state := i.sessions[strings.TrimSpace(sessionID)]; state != nil {
		return state.revision
	}
	return 0
}

func (i *DirectoryIndex) seed(sessionID string, tasks []TaskDescriptor) {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	state := i.sessions[strings.TrimSpace(sessionID)]
	if state == nil {
		return
	}
	for _, descriptor := range tasks {
		taskID := strings.TrimSpace(descriptor.TaskID)
		if taskID == "" {
			continue
		}
		if _, observed := state.status[taskID]; !observed {
			state.status[taskID] = directoryDescriptorFingerprint(descriptor)
		}
	}
}

func (s *service) WatchDirectory(
	ctx context.Context,
	principal Principal,
	req DirectoryWatchRequest,
) (DirectoryWatchResult, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if err := s.authorize(ctx, principal, sessionID); err != nil {
		return DirectoryWatchResult{}, err
	}
	if s.directory == nil {
		return DirectoryWatchResult{}, errorcode.New(errorcode.Unavailable, "taskstream: Task directory observation is unavailable")
	}
	sub := newDirectorySubscription(ctx, s, principal, sessionID)
	return DirectoryWatchResult{Subscription: sub}, nil
}

type directorySubscription struct {
	ctx       context.Context
	cancel    context.CancelFunc
	service   *service
	principal Principal
	sessionID string
	out       chan DirectorySnapshot
	done      chan struct{}

	mu        sync.Mutex
	err       error
	closeOnce sync.Once
}

func newDirectorySubscription(parent context.Context, service *service, principal Principal, sessionID string) *directorySubscription {
	ctx, cancel := context.WithCancel(parent)
	sub := &directorySubscription{
		ctx: ctx, cancel: cancel, service: service, principal: principal,
		sessionID: sessionID, out: make(chan DirectorySnapshot, 1), done: make(chan struct{}),
	}
	go sub.run()
	return sub
}

func (s *directorySubscription) run() {
	defer close(s.done)
	defer close(s.out)
	_, changes, initialRevision, unsubscribe := s.service.directory.subscribe(s.sessionID)
	defer unsubscribe()
	if !s.publishCurrent(max(initialRevision, 1)) {
		return
	}
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-changes:
		}
		timer := time.NewTimer(directoryChangeCoalesceWindow)
	coalesce:
		for {
			select {
			case <-s.ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-changes:
			case <-timer.C:
				break coalesce
			}
		}
		if !s.publishCurrent(s.service.directory.revision(s.sessionID)) {
			return
		}
	}
}

func (s *directorySubscription) publishCurrent(revision uint64) bool {
	result, err := s.service.List(s.ctx, s.principal, ListRequest{SessionID: s.sessionID})
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.setErr(err)
		}
		return false
	}
	s.service.directory.seed(s.sessionID, result.Tasks)
	snapshot := DirectorySnapshot{Revision: revision, Tasks: cloneTaskDescriptors(result.Tasks)}
	select {
	case <-s.ctx.Done():
		return false
	case s.out <- snapshot:
		return true
	default:
	}
	select {
	case <-s.out:
	default:
	}
	select {
	case <-s.ctx.Done():
		return false
	case s.out <- snapshot:
		return true
	}
}

func cloneTaskDescriptors(in []TaskDescriptor) []TaskDescriptor {
	if len(in) == 0 {
		return nil
	}
	out := make([]TaskDescriptor, len(in))
	copy(out, in)
	return out
}

func (s *directorySubscription) Snapshots() <-chan DirectorySnapshot { return s.out }

func (s *directorySubscription) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(s.cancel)
	<-s.done
	return nil
}

func (s *directorySubscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *directorySubscription) setErr(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

var _ DirectoryService = (*service)(nil)
var _ DirectorySubscription = (*directorySubscription)(nil)
