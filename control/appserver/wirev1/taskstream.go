package wirev1

import (
	"encoding/json"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

const (
	// TaskStreamDoneEventName marks an intentional clean end of one Task
	// subscription so clients can distinguish it from transport EOF.
	TaskStreamDoneEventName = "caelis.task_stream.done"
	// TaskStreamErrorEventName terminates an HTTP/SSE Task subscription with a
	// typed observation error. Ordinary observer detach closes the stream
	// without this event.
	TaskStreamErrorEventName = "caelis.task_stream.error"
	// TaskStreamDeliveryEventName carries one atomic Task delivery unit. A
	// replacement becomes visible only after its matching replace_end arrives.
	TaskStreamDeliveryEventName = "caelis.task_stream.delivery"
	// TaskDirectorySnapshotEventName carries one complete replaceable Task
	// directory snapshot.
	TaskDirectorySnapshotEventName = "caelis.task_directory.snapshot"
)

// TaskStreamDelivery is the JSON-safe wire representation of one Task
// delivery. Events stay raw so Envelope integer fields retain wire-v1 string
// encoding through MarshalEnvelope and UnmarshalEnvelope.
type TaskStreamDelivery struct {
	Kind       string            `json:"kind"`
	Source     string            `json:"source"`
	SnapshotID string            `json:"snapshot_id,omitempty"`
	Page       uint32            `json:"page,omitempty"`
	Events     []json.RawMessage `json:"events,omitempty"`
	NextCursor string            `json:"next_cursor,omitempty"`
	ActivityID string            `json:"activity_id,omitempty"`
}

type TaskStreamReadResult struct {
	Deliveries []TaskStreamDelivery `json:"deliveries,omitempty"`
	ActivityID string               `json:"activity_id,omitempty"`
}

// TaskStreamError is the sanitized terminal error carried by a Task SSE stream.
type TaskStreamError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// EncodeTaskStreamError maps one in-process observation failure to its safe wire
// representation.
func EncodeTaskStreamError(err error) TaskStreamError {
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
