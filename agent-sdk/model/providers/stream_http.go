package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

var errStreamResponseHeaderTimeout = errors.New("providers: stream response header timeout")

const defaultStreamResponseHeaderTimeout = 5 * time.Minute

type streamResponseHeaderTimeoutError struct {
	timeout time.Duration
	trace   streamHeaderTraceSnapshot
}

func (e streamResponseHeaderTimeoutError) Error() string {
	message := errStreamResponseHeaderTimeout.Error()
	if e.timeout > 0 {
		message = fmt.Sprintf("%s after %s", message, e.timeout)
	}
	return fmt.Sprintf(
		"%s (got_conn=%t reused=%t was_idle=%t idle_time=%s wrote_request=%t first_response_byte=%t)",
		message,
		e.trace.gotConn,
		e.trace.reused,
		e.trace.wasIdle,
		e.trace.idleTime,
		e.trace.wroteRequest,
		e.trace.gotFirstResponseByte,
	)
}

func (e streamResponseHeaderTimeoutError) Unwrap() error {
	return errStreamResponseHeaderTimeout
}

func (e streamResponseHeaderTimeoutError) Retryable() bool {
	return true
}

func (e streamResponseHeaderTimeoutError) RetryMaxRetries() int {
	return streamTimeoutMaxRetries
}

func (e streamResponseHeaderTimeoutError) ErrorCode() errorcode.Code {
	return errorcode.Timeout
}

func normalizeStreamResponseHeaderTimeout(timeout time.Duration) time.Duration {
	if timeout < 0 {
		return 0
	}
	if timeout == 0 {
		return defaultStreamResponseHeaderTimeout
	}
	return timeout
}

type streamHTTPResult struct {
	response *http.Response
	err      error
}

// doStreamingRequest bounds only the wait for response headers. A successful
// response keeps its derived request context alive until Body.Close, so long
// running streams remain governed by caller cancellation and SSE idle timers.
func doStreamingRequest(client *http.Client, request *http.Request, timeout time.Duration) (*http.Response, error) {
	if client == nil {
		return nil, errors.New("providers: streaming http client is nil")
	}
	if request == nil {
		return nil, errors.New("providers: streaming http request is nil")
	}
	if timeout <= 0 {
		return client.Do(request)
	}

	requestCtx, cancel := context.WithCancel(request.Context())
	traceState := &streamHeaderTrace{}
	request = request.Clone(httptrace.WithClientTrace(requestCtx, traceState.clientTrace()))
	resultCh := make(chan streamHTTPResult)
	go func() {
		response, err := client.Do(request)
		select {
		case resultCh <- streamHTTPResult{response: response, err: err}:
		case <-requestCtx.Done():
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
		}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-resultCh:
		if result.err != nil {
			if result.response != nil && result.response.Body != nil {
				_ = result.response.Body.Close()
			}
			cancel()
			return nil, result.err
		}
		if result.response == nil {
			cancel()
			return nil, errors.New("providers: streaming http client returned an empty response")
		}
		if result.response.Body == nil {
			cancel()
			return nil, errors.New("providers: streaming http response body is nil")
		}
		result.response.Body = &cancelOnCloseReadCloser{ReadCloser: result.response.Body, cancel: cancel}
		return result.response, nil
	case <-timer.C:
		cancel()
		return nil, streamResponseHeaderTimeoutError{timeout: timeout, trace: traceState.snapshot()}
	case <-request.Context().Done():
		cancel()
		return nil, request.Context().Err()
	}
}

type cancelOnCloseReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (r *cancelOnCloseReadCloser) Close() error {
	if r == nil || r.ReadCloser == nil {
		return nil
	}
	r.once.Do(r.cancel)
	return r.ReadCloser.Close()
}

type streamHeaderTrace struct {
	mu                   sync.Mutex
	gotConn              bool
	reused               bool
	wasIdle              bool
	idleTime             time.Duration
	wroteRequest         bool
	gotFirstResponseByte bool
}

type streamHeaderTraceSnapshot struct {
	gotConn              bool
	reused               bool
	wasIdle              bool
	idleTime             time.Duration
	wroteRequest         bool
	gotFirstResponseByte bool
}

func (t *streamHeaderTrace) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.gotConn = true
			t.reused = info.Reused
			t.wasIdle = info.WasIdle
			t.idleTime = info.IdleTime
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.wroteRequest = true
		},
		GotFirstResponseByte: func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.gotFirstResponseByte = true
		},
	}
}

func (t *streamHeaderTrace) snapshot() streamHeaderTraceSnapshot {
	if t == nil {
		return streamHeaderTraceSnapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return streamHeaderTraceSnapshot{
		gotConn:              t.gotConn,
		reused:               t.reused,
		wasIdle:              t.wasIdle,
		idleTime:             t.idleTime,
		wroteRequest:         t.wroteRequest,
		gotFirstResponseByte: t.gotFirstResponseByte,
	}
}
