package wirev1

import (
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
)

const (
	// TaskStreamDoneEventName marks an intentional clean end of one Task
	// subscription so clients can distinguish it from transport EOF.
	TaskStreamDoneEventName = "caelis.task_stream.done"
	// TaskStreamErrorEventName terminates an HTTP/SSE Task subscription with a
	// typed observation error. Ordinary observer detach closes the stream
	// without this event.
	TaskStreamErrorEventName = "caelis.task_stream.error"
	// TaskDirectorySnapshotEventName carries one complete replaceable Task
	// directory snapshot.
	TaskDirectorySnapshotEventName = "caelis.task_directory.snapshot"

	TaskStreamErrorCodeSlowConsumer = "slow_consumer"
)

// TaskStreamError is the sanitized terminal error carried by a Task SSE stream.
type TaskStreamError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// EncodeTaskStreamError maps one in-process observation failure to its safe wire
// representation.
func EncodeTaskStreamError(err error) TaskStreamError {
	if errors.Is(err, controltaskstream.ErrSlowConsumer) {
		return TaskStreamError{
			Code:    TaskStreamErrorCodeSlowConsumer,
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
	return TaskStreamError{Code: string(code), Message: message}
}

// DecodeTaskStreamError rebuilds the typed client-side observation failure.
func DecodeTaskStreamError(wire TaskStreamError) error {
	if wire.Code == TaskStreamErrorCodeSlowConsumer {
		return controltaskstream.ErrSlowConsumer
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
