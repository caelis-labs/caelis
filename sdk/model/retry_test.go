package model

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"
)

type retryTestContextKey struct{}

type retryTestLLM struct {
	calls         int
	events        [][]*StreamEvent
	errs          []error
	seenReqs      []*Request
	seenTexts     []string
	seenCtxValues []string
	mutateRequest bool
}

func (m *retryTestLLM) Name() string { return "retry-test" }

func (m *retryTestLLM) Generate(ctx context.Context, req *Request) iter.Seq2[*StreamEvent, error] {
	call := m.calls
	m.calls++
	m.seenReqs = append(m.seenReqs, req)
	m.seenTexts = append(m.seenTexts, firstRequestText(req))
	if value, _ := ctx.Value(retryTestContextKey{}).(string); value != "" {
		m.seenCtxValues = append(m.seenCtxValues, value)
	}
	if m.mutateRequest && req != nil && len(req.Messages) > 0 && len(req.Messages[0].Parts) > 0 && req.Messages[0].Parts[0].Text != nil {
		req.Messages[0].Parts[0].Text.Text = "mutated"
	}
	return func(yield func(*StreamEvent, error) bool) {
		for _, event := range eventsForCall(m.events, call) {
			if !yield(event, nil) {
				return
			}
		}
		if err := errForCall(m.errs, call); err != nil {
			yield(nil, err)
		}
	}
}

func TestWithRetryRetriesSameLLMRequestBeforeEmission(t *testing.T) {
	t.Parallel()

	final := StreamEventFromResponse(&Response{
		Message:      NewTextMessage(RoleAssistant, "ok"),
		TurnComplete: true,
	})
	inner := &retryTestLLM{
		events: [][]*StreamEvent{
			nil,
			{final},
		},
		errs: []error{
			errors.New("model: http status 529 body={\"error\":\"overloaded_error\"}"),
			nil,
		},
		mutateRequest: true,
	}
	llm := WithRetry(inner, RetryConfig{
		MaxRetries:          2,
		BaseDelay:           time.Nanosecond,
		MaxDelay:            time.Nanosecond,
		RateLimitMaxRetries: 2,
		RateLimitBaseDelay:  time.Nanosecond,
		RateLimitMaxDelay:   time.Nanosecond,
	})
	req := &Request{
		Messages: []Message{NewTextMessage(RoleUser, "hello")},
		Tools:    []ToolSpec{NewFunctionToolSpec("lookup", "lookup", map[string]any{"type": "object"})},
		Output:   &OutputSpec{Mode: OutputModeJSON, JSONSchema: map[string]any{"type": "object"}},
		Stream:   true,
	}
	ctx := context.WithValue(context.Background(), retryTestContextKey{}, "same-context")

	var gotText string
	for event, err := range llm.Generate(ctx, req) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if event != nil && event.Response != nil {
			gotText = event.Response.Message.TextContent()
		}
	}

	if got, want := inner.calls, 2; got != want {
		t.Fatalf("calls = %d, want %d", got, want)
	}
	if gotText != "ok" {
		t.Fatalf("final text = %q, want ok", gotText)
	}
	if got := strings.Join(inner.seenCtxValues, ","); got != "same-context,same-context" {
		t.Fatalf("seen context values = %q, want same context on each attempt", got)
	}
	if got := strings.Join(inner.seenTexts, ","); got != "hello,hello" {
		t.Fatalf("seen request texts = %q, want original request text on each attempt", got)
	}
	if req.Messages[0].TextContent() != "hello" {
		t.Fatalf("original request text = %q, want unchanged", req.Messages[0].TextContent())
	}
	if len(inner.seenReqs) != 2 || inner.seenReqs[0] == req || inner.seenReqs[1] == req || inner.seenReqs[0] == inner.seenReqs[1] {
		t.Fatalf("expected each attempt to receive a fresh request clone, got %#v", inner.seenReqs)
	}
}

func TestWithRetryDoesNotRetryAfterEmission(t *testing.T) {
	t.Parallel()

	inner := &retryTestLLM{
		events: [][]*StreamEvent{
			{{Type: StreamEventPartDelta, PartDelta: &PartDelta{TextDelta: "partial"}}},
			{StreamEventFromResponse(&Response{Message: NewTextMessage(RoleAssistant, "should-not-run"), TurnComplete: true})},
		},
		errs: []error{
			errors.New("model: http status 529 body={\"error\":\"overloaded_error\"}"),
			nil,
		},
	}
	llm := WithRetry(inner, RetryConfig{
		MaxRetries:          2,
		BaseDelay:           time.Nanosecond,
		MaxDelay:            time.Nanosecond,
		RateLimitMaxRetries: 2,
		RateLimitBaseDelay:  time.Nanosecond,
		RateLimitMaxDelay:   time.Nanosecond,
	})

	var (
		gotEvents int
		gotErr    error
	)
	for event, err := range llm.Generate(context.Background(), &Request{Messages: []Message{NewTextMessage(RoleUser, "hello")}}) {
		if err != nil {
			gotErr = err
			continue
		}
		if event != nil {
			gotEvents++
		}
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "overloaded_error") {
		t.Fatalf("Generate() error = %v, want original transient stream error", gotErr)
	}
	if got, want := inner.calls, 1; got != want {
		t.Fatalf("calls = %d, want %d", got, want)
	}
	if gotEvents != 1 {
		t.Fatalf("event count = %d, want 1", gotEvents)
	}
}

func TestWithRetryReturnsNonRetryableErrorUnwrapped(t *testing.T) {
	t.Parallel()

	inner := &retryTestLLM{errs: []error{errors.New("model: http status 400 body={\"error\":\"bad request\"}")}}
	llm := WithRetry(inner, RetryConfig{MaxRetries: 2, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})

	var gotErr error
	for _, err := range llm.Generate(context.Background(), &Request{Messages: []Message{NewTextMessage(RoleUser, "hello")}}) {
		if err != nil {
			gotErr = err
		}
	}
	if gotErr == nil {
		t.Fatal("Generate() error = nil, want non-retryable error")
	}
	if got, want := gotErr.Error(), "model: http status 400 body={\"error\":\"bad request\"}"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	if got, want := inner.calls, 1; got != want {
		t.Fatalf("calls = %d, want %d", got, want)
	}
}

func TestIsBackpressureLLMErrorTreatsProviderOverloadAsBackpressure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "too many requests",
			err:  errors.New("model: http status 429 body={\"error\":\"too many requests\"}"),
			want: true,
		},
		{
			name: "provider overload status",
			err:  errors.New("model: http status 529 body={\"error\":\"overloaded_error\"}"),
			want: true,
		},
		{
			name: "codefree control packet",
			err:  errors.New("model: codefree server overloaded (retCode=51 body={\"retCode\":51})"),
			want: true,
		},
		{
			name: "plain transport failure",
			err:  errors.New("dial tcp: connection reset by peer"),
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsBackpressureLLMError(tc.err); got != tc.want {
				t.Fatalf("IsBackpressureLLMError(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestRetryPolicyForErrorUsesBackpressureBudgetForProviderOverload(t *testing.T) {
	t.Parallel()

	cfg := RetryConfig{
		MaxRetries:          2,
		BaseDelay:           25 * time.Millisecond,
		MaxDelay:            50 * time.Millisecond,
		RateLimitMaxRetries: 7,
		RateLimitBaseDelay:  5 * time.Second,
		RateLimitMaxDelay:   45 * time.Second,
	}
	policy := retryPolicyForError(cfg, errors.New("model: http status 529 body={\"error\":\"overloaded_error\"}"))
	if !policy.backpressure {
		t.Fatal("policy.backpressure = false, want true")
	}
	if got, want := policy.maxRetries, cfg.RateLimitMaxRetries; got != want {
		t.Fatalf("policy.maxRetries = %d, want %d", got, want)
	}
	if got, want := policy.baseDelay, cfg.RateLimitBaseDelay; got != want {
		t.Fatalf("policy.baseDelay = %s, want %s", got, want)
	}
	if got, want := policy.maxDelay, cfg.RateLimitMaxDelay; got != want {
		t.Fatalf("policy.maxDelay = %s, want %s", got, want)
	}
}

func firstRequestText(req *Request) string {
	if req == nil || len(req.Messages) == 0 {
		return ""
	}
	return req.Messages[0].TextContent()
}

func eventsForCall(events [][]*StreamEvent, call int) []*StreamEvent {
	if call < 0 || call >= len(events) {
		return nil
	}
	return events[call]
}

func errForCall(errs []error, call int) error {
	if call < 0 || call >= len(errs) {
		return nil
	}
	return errs[call]
}
