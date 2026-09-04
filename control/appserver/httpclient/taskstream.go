package httpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
)

// TaskClient is the authenticated HTTP implementation of the independent Task
// observation contract.
type TaskClient struct {
	remote *Client
}

// NewTaskClient constructs one Task observation client from an authenticated
// Control HTTP client. Principal binding is already fixed by bearer auth.
func NewTaskClient(client *Client) (*TaskClient, error) {
	if client == nil {
		return nil, errors.New("control http client: Task client requires a Control client")
	}
	return &TaskClient{remote: client}, nil
}

func (c *TaskClient) List(ctx context.Context, request taskstream.ListRequest) (taskstream.ListResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return taskstream.ListResult{}, err
	}
	response, err := c.remote.do(ctx, http.MethodGet, "/sessions/"+sessionID+"/tasks", nil, nil, nil)
	if err != nil {
		return taskstream.ListResult{}, err
	}
	defer response.Body.Close()
	raw, err := readRemoteResponse(response)
	if err != nil {
		return taskstream.ListResult{}, err
	}
	var result taskstream.ListResult
	if err := wirev1.Unmarshal(raw, &result); err != nil {
		return taskstream.ListResult{}, fmt.Errorf("control http client: decode Task list: %w", err)
	}
	return result, nil
}

func (c *TaskClient) Events(ctx context.Context, request taskstream.ReadRequest) (taskstream.ReadResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return taskstream.ReadResult{}, err
	}
	taskID, err := remotePathID("task", request.TaskID)
	if err != nil {
		return taskstream.ReadResult{}, err
	}
	query := make(url.Values)
	if cursor := strings.TrimSpace(request.Cursor); cursor != "" {
		query.Set("after", cursor)
	}
	if activityID := strings.TrimSpace(request.ExpectedActivityID); activityID != "" {
		query.Set("activity_id", activityID)
	}
	response, err := c.remote.do(ctx, http.MethodGet, "/sessions/"+sessionID+"/tasks/"+taskID+"/events", query, nil, nil)
	if err != nil {
		return taskstream.ReadResult{}, err
	}
	defer response.Body.Close()
	raw, err := readRemoteResponse(response)
	if err != nil {
		return taskstream.ReadResult{}, err
	}
	return decodeTaskBatch(raw)
}

func (c *TaskClient) Subscribe(ctx context.Context, request taskstream.SubscribeRequest) (taskstream.SubscribeResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return taskstream.SubscribeResult{}, err
	}
	taskID, err := remotePathID("task", request.TaskID)
	if err != nil {
		return taskstream.SubscribeResult{}, err
	}
	query := make(url.Values)
	if cursor := strings.TrimSpace(request.Cursor); cursor != "" {
		query.Set("after", cursor)
	}
	if request.Follow {
		query.Set("follow", "true")
	}
	response, err := c.remote.do(ctx, http.MethodGet, "/sessions/"+sessionID+"/tasks/"+taskID+"/subscribe", query, nil, nil)
	if err != nil {
		return taskstream.SubscribeResult{}, err
	}
	contentType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil || contentType != "text/event-stream" {
		response.Body.Close()
		return taskstream.SubscribeResult{}, errors.New("control http client: Task subscribe response is not an SSE stream")
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), c.remote.maxEventBytes)
	subscription := newRemoteTaskSubscription(response, scanner)
	return taskstream.SubscribeResult{Subscription: subscription}, nil
}

func decodeTaskBatch(raw []byte) (taskstream.ReadResult, error) {
	var wire wirev1.TaskStreamReadResult
	if err := wirev1.Unmarshal(raw, &wire); err != nil {
		return taskstream.ReadResult{}, fmt.Errorf("control http client: decode Task read result: %w", err)
	}
	result := taskstream.ReadResult{
		Deliveries: make([]taskstream.Delivery, 0, len(wire.Deliveries)),
		ActivityID: wire.ActivityID,
	}
	for _, delivery := range wire.Deliveries {
		decoded, err := decodeTaskDelivery(delivery)
		if err != nil {
			return taskstream.ReadResult{}, err
		}
		result.Deliveries = append(result.Deliveries, decoded)
	}
	return result, nil
}

func decodeTaskDelivery(wire wirev1.TaskStreamDelivery) (taskstream.Delivery, error) {
	delivery := taskstream.Delivery{
		Kind: taskstream.DeliveryKind(wire.Kind), Source: taskstream.SourceClass(wire.Source),
		SnapshotID: wire.SnapshotID, Page: wire.Page,
		NextCursor: wire.NextCursor, ActivityID: wire.ActivityID,
		Events: make([]eventstream.Envelope, 0, len(wire.Events)),
	}
	for _, item := range wire.Events {
		envelope, err := wirev1.UnmarshalEnvelope(item)
		if err != nil {
			return taskstream.Delivery{}, fmt.Errorf("control http client: decode Task Envelope: %w", err)
		}
		delivery.Events = append(delivery.Events, envelope)
	}
	return delivery, nil
}

type remoteTaskSubscription struct {
	response   *http.Response
	scanner    *bufio.Scanner
	deliveries chan taskstream.Delivery
	stop       chan struct{}
	done       chan struct{}

	closeOnce sync.Once
	mu        sync.Mutex
	err       error
}

func newRemoteTaskSubscription(response *http.Response, scanner *bufio.Scanner) *remoteTaskSubscription {
	subscription := &remoteTaskSubscription{
		response:   response,
		scanner:    scanner,
		deliveries: make(chan taskstream.Delivery),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	go subscription.readLoop()
	return subscription
}

func (s *remoteTaskSubscription) Deliveries() <-chan taskstream.Delivery { return s.deliveries }

func (s *remoteTaskSubscription) Close() error {
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

func (s *remoteTaskSubscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *remoteTaskSubscription) readLoop() {
	defer close(s.done)
	defer close(s.deliveries)
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
		case wirev1.TaskStreamDeliveryEventName:
			var wire wirev1.TaskStreamDelivery
			decodeErr := wirev1.Unmarshal(frame.data, &wire)
			if decodeErr != nil {
				s.setErr(errorcode.Wrap(errorcode.InvalidArgument, "control http client: decode Task SSE delivery", decodeErr))
				return
			}
			delivery, decodeErr := decodeTaskDelivery(wire)
			if decodeErr != nil {
				s.setErr(errorcode.Wrap(errorcode.InvalidArgument, "control http client: decode Task SSE delivery", decodeErr))
				return
			}
			if frame.id != "" {
				delivery.NextCursor = frame.id
			}
			if !s.publish(delivery) {
				return
			}
		case wirev1.TaskStreamDoneEventName:
			return
		case wirev1.TaskStreamErrorEventName:
			var wire wirev1.TaskStreamError
			if unmarshalErr := json.Unmarshal(frame.data, &wire); unmarshalErr != nil {
				s.setErr(errorcode.Wrap(errorcode.InvalidArgument, "control http client: decode Task SSE error", unmarshalErr))
				return
			}
			s.setErr(wirev1.DecodeTaskStreamError(wire))
			return
		default:
			// Ignore heartbeats and unknown event names.
		}
	}
}

func (s *remoteTaskSubscription) publish(delivery taskstream.Delivery) bool {
	select {
	case s.deliveries <- delivery:
		return true
	case <-s.stop:
		return false
	}
}

// classifyTaskStreamReadError maps abrupt transport truncation to retryable
// Unavailable while preserving non-retryable decode/frame-size failures.
func classifyTaskStreamReadError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		// Wire contract: only an explicit done event is a clean end.
		return errorcode.New(errorcode.Unavailable, "control http client: Task stream ended without a done event")
	}
	if errors.Is(err, bufio.ErrTooLong) {
		return errorcode.New(errorcode.InvalidArgument, "control http client: Task stream frame is too large")
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return errorcode.New(errorcode.Unavailable, "control http client: Task stream transport interrupted")
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "connection reset") ||
		strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "use of closed network connection") {
		return errorcode.New(errorcode.Unavailable, "control http client: Task stream transport interrupted")
	}
	return err
}

func (s *remoteTaskSubscription) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *remoteTaskSubscription) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
	}
}
