package taskstream

import (
	"context"
	"errors"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
)

const DirectorySnapshotEventName = "caelis.task_directory.snapshot"

// DirectoryWatchRequest selects the lightweight current-state Task directory
// for one Session. Content subscriptions remain independent.
type DirectoryWatchRequest struct {
	SessionID string `json:"session_id"`
}

// DirectorySnapshot is one complete, replaceable Task status index.
type DirectorySnapshot struct {
	Revision uint64           `json:"revision"`
	Tasks    []TaskDescriptor `json:"tasks,omitempty"`
}

type DirectorySubscription interface {
	Snapshots() <-chan DirectorySnapshot
	Close() error
	Err() error
}

type DirectoryWatchResult struct {
	Subscription DirectorySubscription `json:"-"`
}

// DirectoryService is the optional realtime status capability implemented by
// Task services that can observe committed directory changes.
type DirectoryService interface {
	WatchDirectory(context.Context, Principal, DirectoryWatchRequest) (DirectoryWatchResult, error)
}

// DirectoryClient is the independently bound Surface-facing status capability.
type DirectoryClient interface {
	WatchDirectory(context.Context, DirectoryWatchRequest) (DirectoryWatchResult, error)
}

type serviceWithDirectory struct {
	*service
}

func (s *serviceWithDirectory) WatchDirectory(
	ctx context.Context,
	principal Principal,
	req DirectoryWatchRequest,
) (DirectoryWatchResult, error) {
	directory, ok := s.control.(controltaskstream.DirectoryService)
	if !ok {
		return DirectoryWatchResult{}, errorcode.New(errorcode.Unavailable, "taskstream: Task directory observation is unavailable")
	}
	result, err := directory.WatchDirectory(ctx, controlPrincipal(principal), controltaskstream.DirectoryWatchRequest{
		SessionID: req.SessionID,
	})
	if err != nil {
		return DirectoryWatchResult{}, err
	}
	if result.Subscription == nil {
		return DirectoryWatchResult{}, errorcode.New(errorcode.Unavailable, "taskstream: Control directory subscription is unavailable")
	}
	return DirectoryWatchResult{Subscription: newDirectorySubscription(ctx, result.Subscription)}, nil
}

type directorySubscription struct {
	ctx    context.Context
	cancel context.CancelFunc
	inner  controltaskstream.DirectorySubscription
	out    chan DirectorySnapshot

	closeOnce sync.Once
}

func newDirectorySubscription(parent context.Context, inner controltaskstream.DirectorySubscription) *directorySubscription {
	ctx, cancel := context.WithCancel(parent)
	sub := &directorySubscription{ctx: ctx, cancel: cancel, inner: inner, out: make(chan DirectorySnapshot, 1)}
	go sub.forward()
	return sub
}

func (s *directorySubscription) forward() {
	defer close(s.out)
	defer s.Close()
	for {
		select {
		case <-s.ctx.Done():
			return
		case snapshot, open := <-s.inner.Snapshots():
			if !open {
				return
			}
			projected := DirectorySnapshot{
				Revision: snapshot.Revision,
				Tasks:    make([]TaskDescriptor, 0, len(snapshot.Tasks)),
			}
			for _, descriptor := range snapshot.Tasks {
				projected.Tasks = append(projected.Tasks, taskDescriptorFromControl(descriptor))
			}
			if !s.publish(projected) {
				return
			}
		}
	}
}

func (s *directorySubscription) publish(snapshot DirectorySnapshot) bool {
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

func (s *directorySubscription) Snapshots() <-chan DirectorySnapshot { return s.out }

func (s *directorySubscription) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.cancel()
		err = s.inner.Close()
	})
	return err
}

func (s *directorySubscription) Err() error {
	if s == nil || s.inner == nil {
		return errors.New("taskstream: Task directory subscription is unavailable")
	}
	return s.inner.Err()
}

var _ Service = (*serviceWithDirectory)(nil)
var _ DirectoryService = (*serviceWithDirectory)(nil)
var _ DirectorySubscription = (*directorySubscription)(nil)
