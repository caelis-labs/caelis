package httpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

func (c *TaskClient) WatchDirectory(
	ctx context.Context,
	request taskstream.DirectoryWatchRequest,
) (taskstream.DirectoryWatchResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return taskstream.DirectoryWatchResult{}, err
	}
	response, err := c.remote.do(ctx, http.MethodGet, "/sessions/"+sessionID+"/tasks/watch", nil, nil, nil)
	if err != nil {
		return taskstream.DirectoryWatchResult{}, err
	}
	contentType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil || contentType != "text/event-stream" {
		response.Body.Close()
		return taskstream.DirectoryWatchResult{}, errors.New("control http client: Task directory watch response is not an SSE stream")
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), c.remote.maxEventBytes)
	subscription := newRemoteTaskDirectorySubscription(response, scanner)
	return taskstream.DirectoryWatchResult{Subscription: subscription}, nil
}

type remoteTaskDirectorySubscription struct {
	response  *http.Response
	scanner   *bufio.Scanner
	snapshots chan taskstream.DirectorySnapshot
	stop      chan struct{}
	done      chan struct{}

	closeOnce sync.Once
	mu        sync.Mutex
	err       error
}

func newRemoteTaskDirectorySubscription(response *http.Response, scanner *bufio.Scanner) *remoteTaskDirectorySubscription {
	subscription := &remoteTaskDirectorySubscription{
		response: response, scanner: scanner,
		snapshots: make(chan taskstream.DirectorySnapshot, 1),
		stop:      make(chan struct{}), done: make(chan struct{}),
	}
	go subscription.readLoop()
	return subscription
}

func (s *remoteTaskDirectorySubscription) Snapshots() <-chan taskstream.DirectorySnapshot {
	return s.snapshots
}

func (s *remoteTaskDirectorySubscription) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		close(s.stop)
		if s.response != nil && s.response.Body != nil {
			_ = s.response.Body.Close()
		}
	})
	<-s.done
	return nil
}

func (s *remoteTaskDirectorySubscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *remoteTaskDirectorySubscription) readLoop() {
	defer close(s.done)
	defer close(s.snapshots)
	for {
		frame, err := readRemoteSSEFrame(s.scanner)
		if err != nil {
			if s.stopped() {
				return
			}
			s.setErr(classifyTaskStreamReadError(err))
			return
		}
		switch frame.event {
		case taskstream.DirectorySnapshotEventName:
			snapshot, decodeErr := decodeTaskDirectorySnapshot(frame.data)
			if decodeErr != nil {
				s.setErr(errorcode.Wrap(errorcode.InvalidArgument, "control http client: decode Task directory snapshot", decodeErr))
				return
			}
			if !s.publish(snapshot) {
				return
			}
		case taskstream.StreamDoneEventName:
			return
		case taskstream.StreamErrorEventName:
			var wire taskstream.StreamError
			if unmarshalErr := json.Unmarshal(frame.data, &wire); unmarshalErr != nil {
				s.setErr(errorcode.Wrap(errorcode.InvalidArgument, "control http client: decode Task directory SSE error", unmarshalErr))
				return
			}
			s.setErr(taskstream.DecodeStreamError(wire))
			return
		default:
			// Ignore heartbeats and unknown event names.
		}
	}
}

type taskDirectorySnapshotWire struct {
	Revision string                      `json:"revision"`
	Tasks    []taskstream.TaskDescriptor `json:"tasks,omitempty"`
}

func decodeTaskDirectorySnapshot(raw []byte) (taskstream.DirectorySnapshot, error) {
	var wire taskDirectorySnapshotWire
	if err := wirev1.Unmarshal(raw, &wire); err != nil {
		return taskstream.DirectorySnapshot{}, err
	}
	revision, err := wirev1.ParseUint64Decimal(strings.TrimSpace(wire.Revision))
	if err != nil {
		return taskstream.DirectorySnapshot{}, err
	}
	return taskstream.DirectorySnapshot{Revision: revision, Tasks: wire.Tasks}, nil
}

func (s *remoteTaskDirectorySubscription) publish(snapshot taskstream.DirectorySnapshot) bool {
	select {
	case <-s.stop:
		return false
	case s.snapshots <- snapshot:
		return true
	default:
	}
	select {
	case <-s.snapshots:
	default:
	}
	select {
	case <-s.stop:
		return false
	case s.snapshots <- snapshot:
		return true
	}
}

func (s *remoteTaskDirectorySubscription) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *remoteTaskDirectorySubscription) setErr(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

var _ taskstream.DirectoryClient = (*TaskClient)(nil)
var _ taskstream.DirectorySubscription = (*remoteTaskDirectorySubscription)(nil)
