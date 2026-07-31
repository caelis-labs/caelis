package taskstream

import (
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

const (
	// StreamDoneEventName marks an intentional clean end of one Task
	// subscription so clients can distinguish it from transport EOF.
	StreamDoneEventName = "caelis.task_stream.done"
	// StreamErrorEventName terminates an HTTP/SSE Task subscription with a
	// typed observation error. Ordinary observer detach closes the stream
	// without this event.
	StreamErrorEventName = "caelis.task_stream.error"

	StreamErrorCodeSlowConsumer = "slow_consumer"
)

// StreamError is the sanitized terminal error carried by a Task SSE stream.
type StreamError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// EncodeStreamError maps one in-process observation failure to its safe wire
// representation.
func EncodeStreamError(err error) StreamError {
	if errors.Is(err, ErrSlowConsumer) {
		return StreamError{
			Code:    StreamErrorCodeSlowConsumer,
			Message: "Task stream observer is too slow",
		}
	}
	code := errorcode.CodeOf(err)
	if code == "" {
		code = errorcode.Unknown
	}
	message := "Task stream subscription failed"
	if code == errorcode.Unavailable {
		message = "Task stream is unavailable"
	}
	return StreamError{Code: string(code), Message: message}
}

// DecodeStreamError rebuilds the typed client-side observation failure.
func DecodeStreamError(wire StreamError) error {
	if wire.Code == StreamErrorCodeSlowConsumer {
		return ErrSlowConsumer
	}
	code := errorcode.Code(wire.Code)
	if code == "" {
		code = errorcode.Unknown
	}
	message := wire.Message
	if message == "" {
		message = "Task stream subscription failed"
	}
	return errorcode.New(code, message)
}
