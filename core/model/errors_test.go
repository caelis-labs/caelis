package model

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestProviderErrorClassifiesBackpressureAndRetryability(t *testing.T) {
	err := NewProviderError(ProviderError{
		Provider:   "openai",
		Operation:  "chat completion",
		StatusCode: http.StatusTooManyRequests,
		Type:       "rate_limit_exceeded",
		Message:    "too many requests",
	})
	if !IsProviderError(err) || !err.Backpressure() || !err.Retryable() {
		t.Fatalf("provider error classification = provider:%v backpressure:%v retryable:%v", IsProviderError(err), err.Backpressure(), err.Retryable())
	}
	if !strings.Contains(err.Error(), "model/openai: chat completion failed: 429") ||
		!strings.Contains(err.Error(), "rate_limit_exceeded") {
		t.Fatalf("error string = %q, want provider/status/type", err.Error())
	}
}

func TestProviderErrorDetectsContextOverflow(t *testing.T) {
	err := NewProviderError(ProviderError{
		Provider:   "anthropic",
		Operation:  "messages",
		StatusCode: http.StatusBadRequest,
		Message:    "input is too long for the maximum context window",
	})
	wrapped := errors.New("outer: " + err.Error())
	if IsContextOverflow(wrapped) {
		t.Fatal("string wrapping without %w should not satisfy structured context overflow")
	}
	wrapped = errors.Join(errors.New("outer"), err)
	if !IsContextOverflow(wrapped) || err.Retryable() {
		t.Fatalf("context overflow = %v retryable = %v, want overflow and non-retryable", IsContextOverflow(wrapped), err.Retryable())
	}
}
