package model

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ProviderError is the provider-neutral control-plane error contract returned
// by model adapters. It preserves enough structured information for shared
// services and future retry/backoff policy without parsing provider text.
type ProviderError struct {
	Provider        string `json:"provider,omitempty"`
	Operation       string `json:"operation,omitempty"`
	StatusCode      int    `json:"status_code,omitempty"`
	Status          string `json:"status,omitempty"`
	Code            string `json:"code,omitempty"`
	Type            string `json:"type,omitempty"`
	Message         string `json:"message,omitempty"`
	Body            string `json:"body,omitempty"`
	Temporary       bool   `json:"temporary,omitempty"`
	RateLimited     bool   `json:"rate_limited,omitempty"`
	ContextOverflow bool   `json:"context_overflow,omitempty"`
}

func NewProviderError(in ProviderError) *ProviderError {
	in.Provider = strings.TrimSpace(in.Provider)
	in.Operation = strings.TrimSpace(in.Operation)
	in.Status = strings.TrimSpace(in.Status)
	if in.Status == "" && in.StatusCode > 0 {
		in.Status = strings.TrimSpace(fmt.Sprintf("%d %s", in.StatusCode, http.StatusText(in.StatusCode)))
	}
	in.Code = strings.TrimSpace(in.Code)
	in.Type = strings.TrimSpace(in.Type)
	in.Message = strings.TrimSpace(in.Message)
	in.Body = strings.TrimSpace(in.Body)
	if !in.ContextOverflow {
		in.ContextOverflow = providerErrorLooksLikeContextOverflow(in)
	}
	return &in
}

func (e *ProviderError) Error() string {
	if e == nil {
		return "model: provider error"
	}
	var b strings.Builder
	b.WriteString("model")
	if e.Provider != "" {
		b.WriteString("/")
		b.WriteString(e.Provider)
	}
	if e.Operation != "" {
		b.WriteString(": ")
		b.WriteString(e.Operation)
		b.WriteString(" failed")
	} else {
		b.WriteString(": provider request failed")
	}
	if e.Status != "" {
		b.WriteString(": ")
		b.WriteString(e.Status)
	} else if e.StatusCode > 0 {
		b.WriteString(": HTTP ")
		b.WriteString(fmt.Sprint(e.StatusCode))
	}
	labels := compactNonEmpty(e.Type, e.Code)
	if len(labels) > 0 {
		b.WriteString(": ")
		b.WriteString(strings.Join(labels, "/"))
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

func (e *ProviderError) Retryable() bool {
	if e == nil || e.ContextOverflow {
		return false
	}
	if e.Temporary || e.Backpressure() {
		return true
	}
	if e.StatusCode >= 500 {
		return true
	}
	switch e.StatusCode {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

func (e *ProviderError) Backpressure() bool {
	if e == nil {
		return false
	}
	if e.RateLimited {
		return true
	}
	if e.StatusCode == http.StatusTooManyRequests || e.StatusCode == 529 {
		return true
	}
	text := strings.ToLower(strings.Join(compactNonEmpty(e.Type, e.Code, e.Message, e.Body), " "))
	return strings.Contains(text, "rate limit") ||
		strings.Contains(text, "ratelimit") ||
		strings.Contains(text, "too many requests") ||
		strings.Contains(text, "overloaded") ||
		strings.Contains(text, "retcode=51") ||
		strings.Contains(text, `"retcode":51`)
}

func IsProviderError(err error) bool {
	var providerErr *ProviderError
	return errors.As(err, &providerErr)
}

func ProviderErrorFrom(err error) (*ProviderError, bool) {
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return providerErr, true
	}
	return nil, false
}

func IsContextOverflow(err error) bool {
	var providerErr *ProviderError
	return errors.As(err, &providerErr) && providerErr.ContextOverflow
}

func providerErrorLooksLikeContextOverflow(err ProviderError) bool {
	text := strings.ToLower(strings.Join(compactNonEmpty(err.Type, err.Code, err.Message, err.Body), " "))
	if text == "" {
		return false
	}
	for _, phrase := range []string{
		"context length",
		"context_length",
		"context window",
		"context_window",
		"context too long",
		"maximum context",
		"max context",
		"too many tokens",
		"token limit",
		"tokens exceed",
		"input is too long",
	} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func compactNonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
