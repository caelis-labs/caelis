// Package errorcode defines transport-neutral machine-readable SDK error
// categories. Human-readable messages are not a control-flow contract.
package errorcode

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Code is one stable machine-readable error category.
type Code string

const (
	Unknown            Code = "unknown"
	InvalidArgument    Code = "invalid_argument"
	NotFound           Code = "not_found"
	AlreadyExists      Code = "already_exists"
	Conflict           Code = "conflict"
	PermissionDenied   Code = "permission_denied"
	Unauthenticated    Code = "unauthenticated"
	FailedPrecondition Code = "failed_precondition"
	ResourceExhausted  Code = "resource_exhausted"
	RateLimited        Code = "rate_limited"
	Overloaded         Code = "overloaded"
	Timeout            Code = "timeout"
	Cancelled          Code = "cancelled"
	Unavailable        Code = "unavailable"
	Unsupported        Code = "unsupported"
	UnknownOutcome     Code = "unknown_outcome"
	Internal           Code = "internal"
)

// Coder is implemented by typed SDK errors.
type Coder interface {
	ErrorCode() Code
}

// Error carries a stable code while preserving a human message and cause.
type Error struct {
	Code    Code
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if message := strings.TrimSpace(e.Message); message != "" {
		return message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return string(e.Code)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *Error) ErrorCode() Code {
	if e == nil {
		return Unknown
	}
	return e.Code
}

// New returns one coded error with no underlying cause.
func New(code Code, message string) *Error {
	return &Error{Code: code, Message: strings.TrimSpace(message)}
}

// Wrap returns one coded error retaining err as its cause.
func Wrap(code Code, message string, err error) *Error {
	return &Error{Code: code, Message: strings.TrimSpace(message), Err: err}
}

// CodeOf finds the first typed code in an error chain. Caller cancellation and
// deadline causes are normalized without message inspection.
func CodeOf(err error) Code {
	if err == nil {
		return Unknown
	}
	var coded Coder
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	switch {
	case errors.Is(err, context.Canceled):
		return Cancelled
	case errors.Is(err, context.DeadlineExceeded):
		return Timeout
	default:
		return Unknown
	}
}

// Is reports whether any error in the chain has code.
func Is(err error, code Code) bool { return CodeOf(err) == code }

// Require returns an error when actual differs from expected. It is useful in
// black-box contract tests and adapters.
func Require(err error, expected Code) error {
	if actual := CodeOf(err); actual != expected {
		return fmt.Errorf("error code %q, want %q", actual, expected)
	}
	return nil
}
