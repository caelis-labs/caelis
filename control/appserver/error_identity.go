package appserver

import "errors"

// ErrorKind identifies Control errors whose exact identity, beyond their
// general error code, is part of client recovery behavior.
type ErrorKind string

const (
	ErrorKindSessionClosed         ErrorKind = "session_closed"
	ErrorKindUnauthorized          ErrorKind = "unauthorized"
	ErrorKindOperationConflict     ErrorKind = "operation_conflict"
	ErrorKindStateRevisionConflict ErrorKind = "state_revision_conflict"
)

// ErrorKindOf returns the stable Control identity for a known domain error.
func ErrorKindOf(err error) ErrorKind {
	switch {
	case errors.Is(err, ErrSessionClosed):
		return ErrorKindSessionClosed
	case errors.Is(err, ErrUnauthorized):
		return ErrorKindUnauthorized
	case errors.Is(err, ErrOperationConflict):
		return ErrorKindOperationConflict
	case errors.Is(err, ErrStateRevisionConflict):
		return ErrorKindStateRevisionConflict
	default:
		return ""
	}
}
