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
	"github.com/caelis-labs/caelis/control/client/wirev1"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
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

func (c *TaskClient) Events(ctx context.Context, request taskstream.ReadRequest) (taskstream.Batch, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return taskstream.Batch{}, err
	}
	taskID, err := remotePathID("task", request.TaskID)
	if err != nil {
		return taskstream.Batch{}, err
	}
	query := make(url.Values)
	if cursor := strings.TrimSpace(request.Cursor); cursor != "" {
		query.Set("after", cursor)
	}
	response, err := c.remote.do(ctx, http.MethodGet, "/sessions/"+sessionID+"/tasks/"+taskID+"/events", query, nil, nil)
	if err != nil {
		return taskstream.Batch{}, err
	}
	defer response.Body.Close()
	raw, err := readRemoteResponse(response)
	if err != nil {
		return taskstream.Batch{}, err
	}
	return decodeTaskBatch(raw, response.Header)
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
	batch, err := decodeTaskBatchHeaders(response.Header)
	if err != nil {
		response.Body.Close()
		return taskstream.SubscribeResult{}, err
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), c.remote.maxEventBytes)
	subscription := newRemoteTaskSubscription(response, scanner, c.remote.eventBuffer)
	return taskstream.SubscribeResult{
		Subscription:   subscription,
		ResumeMode:     batch.ResumeMode,
		TransientGap:   batch.TransientGap,
		BoundaryCursor: batch.BoundaryCursor,
	}, nil
}

type taskBatchWire struct {
	Events         []json.RawMessage     `json:"events,omitempty"`
	ResumeMode     taskstream.ResumeMode `json:"resume_mode"`
	TransientGap   bool                  `json:"transient_gap,omitempty"`
	BoundaryCursor string                `json:"boundary_cursor,omitempty"`
}

func decodeTaskBatch(raw []byte, headers http.Header) (taskstream.Batch, error) {
	var wire taskBatchWire
	if err := wirev1.Unmarshal(raw, &wire); err != nil {
		return taskstream.Batch{}, fmt.Errorf("control http client: decode Task batch: %w", err)
	}
	batch := taskstream.Batch{
		Events:         make([]eventstream.Envelope, 0, len(wire.Events)),
		ResumeMode:     wire.ResumeMode,
		TransientGap:   wire.TransientGap,
		BoundaryCursor: wire.BoundaryCursor,
	}
	if headers != nil {
		if mode := headers.Get(wirev1.ResumeModeHeader); mode != "" {
			batch.ResumeMode = taskstream.ResumeMode(mode)
		}
		if cursor := headers.Get(wirev1.BoundaryCursorHeader); cursor != "" {
			batch.BoundaryCursor = cursor
		}
		if headers.Get(wirev1.TransientGapHeader) == "true" {
			batch.TransientGap = true
		}
	}
	for _, item := range wire.Events {
		envelope, err := wirev1.UnmarshalEnvelope(item)
		if err != nil {
			return taskstream.Batch{}, fmt.Errorf("control http client: decode Task Envelope: %w", err)
		}
		batch.Events = append(batch.Events, envelope)
	}
	return batch, nil
}

func decodeTaskBatchHeaders(headers http.Header) (taskstream.Batch, error) {
	if headers == nil {
		return taskstream.Batch{}, errors.New("control http client: Task subscribe headers are required")
	}
	return taskstream.Batch{
		ResumeMode:     taskstream.ResumeMode(headers.Get(wirev1.ResumeModeHeader)),
		TransientGap:   headers.Get(wirev1.TransientGapHeader) == "true",
		BoundaryCursor: headers.Get(wirev1.BoundaryCursorHeader),
	}, nil
}

type remoteTaskSubscription struct {
	response *http.Response
	scanner  *bufio.Scanner
	events   chan eventstream.Envelope
	stop     chan struct{}
	done     chan struct{}

	closeOnce sync.Once
	mu        sync.Mutex
	err       error
	last      string
}

func newRemoteTaskSubscription(response *http.Response, scanner *bufio.Scanner, capacity int) *remoteTaskSubscription {
	if capacity <= 0 {
		capacity = defaultRemoteEventBuffer
	}
	subscription := &remoteTaskSubscription{
		response: response,
		scanner:  scanner,
		events:   make(chan eventstream.Envelope, capacity),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go subscription.readLoop()
	return subscription
}

func (s *remoteTaskSubscription) Events() <-chan eventstream.Envelope { return s.events }

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

func (s *remoteTaskSubscription) LastCursor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *remoteTaskSubscription) readLoop() {
	defer close(s.done)
	defer close(s.events)
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
		case "", "message":
			envelope, decodeErr := wirev1.UnmarshalEnvelope(frame.data)
			if decodeErr != nil {
				// Malformed payloads are not transport gaps; fail closed without retry.
				s.setErr(errorcode.Wrap(errorcode.InvalidArgument, "control http client: decode Task SSE Envelope", decodeErr))
				return
			}
			if frame.id != "" {
				envelope.Cursor = frame.id
			}
			if !s.publish(envelope) {
				return
			}
		case taskstream.StreamDoneEventName:
			return
		case taskstream.StreamErrorEventName:
			var wire taskstream.StreamError
			if unmarshalErr := json.Unmarshal(frame.data, &wire); unmarshalErr != nil {
				s.setErr(errorcode.Wrap(errorcode.InvalidArgument, "control http client: decode Task SSE error", unmarshalErr))
				return
			}
			s.setErr(taskstream.DecodeStreamError(wire))
			return
		default:
			// Ignore heartbeats and unknown event names.
		}
	}
}

func (s *remoteTaskSubscription) publish(envelope eventstream.Envelope) bool {
	select {
	case <-s.stop:
		return false
	default:
	}
	// Hold the cursor lock across the non-blocking send so a receiver that sees
	// the event cannot observe a stale LastCursor. Failed delivery does not
	// advance last, so reconnect cannot skip undelivered events.
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case s.events <- envelope:
		if cursor := strings.TrimSpace(envelope.Cursor); cursor != "" {
			s.last = cursor
		}
		return true
	case <-s.stop:
		return false
	default:
		if s.err == nil {
			s.err = taskstream.ErrSlowConsumer
		}
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
