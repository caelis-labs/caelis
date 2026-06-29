package model

import (
	"context"
	"encoding/json"
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

type retryProviderToolUsageLLM struct {
	*retryTestLLM
	usesProviderTools bool
}

func (m *retryProviderToolUsageLLM) UsesProviderExecutedTools(*Request) bool {
	return m.usesProviderTools
}

type retrySearchLLM struct {
	*retryTestLLM
	searchCalls int
	searchErrs  []error
}

func (m *retrySearchLLM) SearchWeb(_ context.Context, req WebSearchRequest) (WebSearchResponse, error) {
	call := m.searchCalls
	m.searchCalls++
	if err := errForCall(m.searchErrs, call); err != nil {
		return WebSearchResponse{}, err
	}
	return WebSearchResponse{Query: req.Query, Provider: "test", Answer: "ok"}, nil
}

type retryUnavailableReasonLLM struct {
	*retryTestLLM
	reason string
}

func (m *retryUnavailableReasonLLM) WebSearchUnavailableReason() string {
	return m.reason
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

func TestWithRetryRetriesSearchWebTransientFailure(t *testing.T) {
	t.Parallel()

	inner := &retrySearchLLM{
		retryTestLLM: &retryTestLLM{},
		searchErrs: []error{
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
	searcher, ok := llm.(WebSearcher)
	if !ok {
		t.Fatal("WithRetry() did not preserve WebSearcher capability")
	}
	resp, err := searcher.SearchWeb(context.Background(), WebSearchRequest{Query: "latest"})
	if err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	if got, want := inner.searchCalls, 2; got != want {
		t.Fatalf("search calls = %d, want %d", got, want)
	}
	if resp.Answer != "ok" {
		t.Fatalf("answer = %q, want ok", resp.Answer)
	}
}

func TestWithRetryDoesNotExposeWebSearcherForUnsupportedProvider(t *testing.T) {
	t.Parallel()

	llm := WithRetry(&retryTestLLM{}, RetryConfig{})
	if _, ok := llm.(WebSearcher); ok {
		t.Fatal("WithRetry() exposes WebSearcher for inner LLM without SearchWeb")
	}
}

func TestWithRetryPreservesWebSearchUnavailableReason(t *testing.T) {
	t.Parallel()

	const want = "Xiaomi Token Plan endpoints do not support provider-native web_search"
	llm := WithRetry(&retryUnavailableReasonLLM{
		retryTestLLM: &retryTestLLM{},
		reason:       want,
	}, RetryConfig{})
	reasoner, ok := llm.(WebSearchAvailability)
	if !ok {
		t.Fatal("WithRetry() did not preserve WebSearchUnavailableReason capability")
	}
	if got := reasoner.WebSearchUnavailableReason(); got != want {
		t.Fatalf("WebSearchUnavailableReason() = %q, want %q", got, want)
	}
}

func TestWithRetryNoRetryAfterImplicitProviderExecutedToolSemanticEmission(t *testing.T) {
	t.Parallel()

	inner := &retryProviderToolUsageLLM{
		retryTestLLM: &retryTestLLM{
			events: [][]*StreamEvent{
				{{Type: StreamEventPartDelta, PartDelta: &PartDelta{TextDelta: "partial"}}},
				{StreamEventFromResponse(&Response{Message: NewTextMessage(RoleAssistant, "should-not-run"), TurnComplete: true})},
			},
			errs: []error{
				errors.New("model: http status 529 body={\"error\":\"overloaded_error\"}"),
				nil,
			},
		},
		usesProviderTools: true,
	}
	llm := WithRetry(inner, RetryConfig{
		MaxRetries:          2,
		BaseDelay:           time.Nanosecond,
		MaxDelay:            time.Nanosecond,
		RateLimitMaxRetries: 2,
		RateLimitBaseDelay:  time.Nanosecond,
		RateLimitMaxDelay:   time.Nanosecond,
	})

	var gotErr error
	for _, err := range llm.Generate(context.Background(), &Request{
		Messages: []Message{NewTextMessage(RoleUser, "hello")},
	}) {
		if err != nil {
			gotErr = err
		}
	}

	if gotErr == nil || !strings.Contains(gotErr.Error(), "overloaded_error") {
		t.Fatalf("Generate() error = %v, want original transient stream error", gotErr)
	}
	if got, want := inner.calls, 1; got != want {
		t.Fatalf("calls = %d, want %d (implicit provider tool should block retry after semantic output)", got, want)
	}
}

func TestWithRetryDoesNotRetryAfterProviderExecutedToolSemanticEmission(t *testing.T) {
	t.Parallel()

	inner := &retryProviderToolUsageLLM{
		retryTestLLM: &retryTestLLM{
			events: [][]*StreamEvent{
				{{Type: StreamEventPartDelta, PartDelta: &PartDelta{TextDelta: "partial"}}},
				{StreamEventFromResponse(&Response{Message: NewTextMessage(RoleAssistant, "should-not-run"), TurnComplete: true})},
			},
			errs: []error{
				errors.New("model: http status 529 body={\"error\":\"overloaded_error\"}"),
				nil,
			},
		},
		usesProviderTools: true,
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
	req := &Request{
		Messages: []Message{NewTextMessage(RoleUser, "hello")},
		Tools: []ToolSpec{
			NewProviderExecutedToolSpec("test-provider", "server_search", nil),
		},
	}
	for event, err := range llm.Generate(context.Background(), req) {
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

func TestWithRetryRetriesAfterEmptyEventEmission(t *testing.T) {
	t.Parallel()

	final := StreamEventFromResponse(&Response{
		Message:      NewTextMessage(RoleAssistant, "ok"),
		TurnComplete: true,
	})
	inner := &retryTestLLM{
		events: [][]*StreamEvent{
			{{Type: StreamEventPartDelta, PartDelta: &PartDelta{}}},
			{final},
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

	var gotText string
	for event, err := range llm.Generate(context.Background(), &Request{Messages: []Message{NewTextMessage(RoleUser, "hello")}}) {
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
}

func TestWithRetryRetriesProviderBadRequestUntilExhausted(t *testing.T) {
	t.Parallel()

	err := errors.New("model: http status 400 body={\"error\":\"bad request\"}")
	inner := &retryTestLLM{errs: []error{err, err, err}}
	llm := WithRetry(inner, RetryConfig{MaxRetries: 2, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})

	var gotErr error
	for _, err := range llm.Generate(context.Background(), &Request{Messages: []Message{NewTextMessage(RoleUser, "hello")}}) {
		if err != nil {
			gotErr = err
		}
	}
	if gotErr == nil {
		t.Fatal("Generate() error = nil, want exhausted retry error")
	}
	if got := gotErr.Error(); !strings.Contains(got, "failed after 2 retries") || !strings.Contains(got, "http status 400") {
		t.Fatalf("error = %q, want exhausted retry wrapping 400", got)
	}
	if got, want := inner.calls, 3; got != want {
		t.Fatalf("calls = %d, want %d", got, want)
	}
}

func TestIsRetryableLLMErrorTreatsStreamFirstEventTimeoutAsTransient(t *testing.T) {
	t.Parallel()

	err := errors.New("providers: stream first event timeout after 5m0s")
	if !IsRetryableLLMError(err) {
		t.Fatalf("IsRetryableLLMError(%q) = false, want true", err)
	}
}

func TestIsRetryableLLMErrorRetriesBadRequestButNotOverflow(t *testing.T) {
	t.Parallel()

	badRequest := errors.New("model: http status 400 body={\"error\":{\"message\":\"Multimodal data is corrupted or cannot be processed.\"}}")
	if !IsRetryableLLMError(badRequest) {
		t.Fatalf("IsRetryableLLMError(%q) = false, want true", badRequest)
	}

	overflow := &ContextOverflowError{Cause: errors.New("model: http status 400 body={\"error\":\"context length exceeded\"}")}
	if IsRetryableLLMError(overflow) {
		t.Fatalf("IsRetryableLLMError(%q) = true, want false for compaction overflow", overflow)
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

func TestWithRetrySpeculativeAttemptRetry(t *testing.T) {
	t.Parallel()

	final := StreamEventFromResponse(&Response{
		Message:      NewTextMessage(RoleAssistant, "final-ok"),
		TurnComplete: true,
	})
	inner := &retryTestLLM{
		events: [][]*StreamEvent{
			{
				{Type: StreamEventPartDelta, PartDelta: &PartDelta{TextDelta: "partial text"}},
			},
			{final},
		},
		errs: []error{
			errors.New("providers: sse scanner: unexpected EOF"),
			nil,
		},
	}
	llm := WithRetry(inner, RetryConfig{
		MaxRetries: 2,
		BaseDelay:  time.Nanosecond,
		MaxDelay:   time.Nanosecond,
	})

	var (
		gotEvents []*StreamEvent
		gotErr    error
	)
	for event, err := range llm.Generate(context.Background(), &Request{Messages: []Message{NewTextMessage(RoleUser, "hello")}}) {
		if err != nil {
			gotErr = err
			continue
		}
		if event != nil {
			gotEvents = append(gotEvents, event)
		}
	}

	if gotErr != nil {
		t.Fatalf("unexpected error: %v", gotErr)
	}
	if got, want := inner.calls, 2; got != want {
		t.Fatalf("calls = %d, want %d", got, want)
	}

	// Expected event order:
	// 1. partial text delta
	// 2. attempt_reset
	// 3. final response event
	if len(gotEvents) != 3 {
		t.Fatalf("len(gotEvents) = %d, want 3", len(gotEvents))
	}
	if gotEvents[0].PartDelta == nil || gotEvents[0].PartDelta.TextDelta != "partial text" {
		t.Errorf("gotEvents[0] = %#v, want partial text delta", gotEvents[0])
	}
	if gotEvents[1].Type != StreamEventAttemptReset || gotEvents[1].AttemptReset == nil || gotEvents[1].AttemptReset.Attempt != 1 {
		t.Errorf("gotEvents[1] = %#v, want attempt_reset", gotEvents[1])
	}
	if gotEvents[2].Response == nil || gotEvents[2].Response.Message.TextContent() != "final-ok" {
		t.Errorf("gotEvents[2] = %#v, want final-ok response", gotEvents[2])
	}
}

func TestWithRetryNoRetryForProviderExecutedTools(t *testing.T) {
	t.Parallel()

	inner := &retryProviderToolUsageLLM{
		retryTestLLM: &retryTestLLM{
			events: [][]*StreamEvent{
				{
					{Type: StreamEventPartDelta, PartDelta: &PartDelta{TextDelta: "partial text"}},
				},
				{
					StreamEventFromResponse(&Response{Message: NewTextMessage(RoleAssistant, "should-not-reach"), TurnComplete: true}),
				},
			},
			errs: []error{
				errors.New("providers: sse scanner: unexpected EOF"),
				nil,
			},
		},
		usesProviderTools: true,
	}
	llm := WithRetry(inner, RetryConfig{
		MaxRetries: 2,
		BaseDelay:  time.Nanosecond,
		MaxDelay:   time.Nanosecond,
	})

	req := &Request{
		Messages: []Message{NewTextMessage(RoleUser, "hello")},
		Tools: []ToolSpec{
			NewProviderExecutedToolSpec("test-provider", "server_search", nil),
		},
	}

	var gotErr error
	for _, err := range llm.Generate(context.Background(), req) {
		if err != nil {
			gotErr = err
		}
	}

	if gotErr == nil || !strings.Contains(gotErr.Error(), "unexpected EOF") {
		t.Fatalf("gotErr = %v, want unexpected EOF", gotErr)
	}
	if got, want := inner.calls, 1; got != want {
		t.Fatalf("calls = %d, want %d (no retry should happen)", got, want)
	}
}

func TestWithRetryRetriesWhenProviderHookReportsNoProviderExecutedTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec ToolSpec
	}{
		{
			name: "active spec ignored without provider hook match",
			spec: NewProviderExecutedToolSpec("test-provider", "server_search", nil),
		},
		{
			name: "empty spec ignored without provider hook match",
			spec: ToolSpec{
				Kind:             ToolSpecKindProviderExecuted,
				ProviderExecuted: &ProviderExecutedToolSpec{},
			},
		},
		{
			name: "disabled spec",
			spec: NewProviderExecutedToolSpec("test-provider", "server_search", map[string]json.RawMessage{
				"disabled": json.RawMessage(`true`),
			}),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			inner := &retryProviderToolUsageLLM{
				retryTestLLM: &retryTestLLM{
					events: [][]*StreamEvent{
						{
							{Type: StreamEventPartDelta, PartDelta: &PartDelta{TextDelta: "partial text"}},
						},
						{
							StreamEventFromResponse(&Response{Message: NewTextMessage(RoleAssistant, "retried"), TurnComplete: true}),
						},
					},
					errs: []error{
						errors.New("providers: sse scanner: unexpected EOF"),
						nil,
					},
				},
				usesProviderTools: false,
			}
			llm := WithRetry(inner, RetryConfig{
				MaxRetries: 2,
				BaseDelay:  time.Nanosecond,
				MaxDelay:   time.Nanosecond,
			})
			req := &Request{
				Messages: []Message{NewTextMessage(RoleUser, "hello")},
				Tools:    []ToolSpec{tc.spec},
			}

			var gotErr error
			for _, err := range llm.Generate(context.Background(), req) {
				if err != nil {
					gotErr = err
				}
			}

			if gotErr != nil {
				t.Fatalf("Generate() error = %v, want retry to recover", gotErr)
			}
			if got, want := inner.calls, 2; got != want {
				t.Fatalf("calls = %d, want %d", got, want)
			}
		})
	}
}

func TestWithRetryNoRetryAfterFinalResponse(t *testing.T) {
	t.Parallel()

	final := StreamEventFromResponse(&Response{
		Message:      NewTextMessage(RoleAssistant, "final-ok"),
		TurnComplete: true,
	})
	inner := &retryTestLLM{
		events: [][]*StreamEvent{
			{final},
			{final},
		},
		errs: []error{
			errors.New("providers: sse scanner: unexpected EOF"),
			nil,
		},
	}
	llm := WithRetry(inner, RetryConfig{
		MaxRetries: 2,
		BaseDelay:  time.Nanosecond,
		MaxDelay:   time.Nanosecond,
	})

	var gotErr error
	for _, err := range llm.Generate(context.Background(), &Request{Messages: []Message{NewTextMessage(RoleUser, "hello")}}) {
		if err != nil {
			gotErr = err
		}
	}

	if gotErr == nil || !strings.Contains(gotErr.Error(), "unexpected EOF") {
		t.Fatalf("gotErr = %v, want unexpected EOF", gotErr)
	}
	if got, want := inner.calls, 1; got != want {
		t.Fatalf("calls = %d, want %d (no retry should happen after final response)", got, want)
	}
}
