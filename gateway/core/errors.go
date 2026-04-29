package core

import "errors"

type ErrorKind string

const (
	KindValidation  ErrorKind = "validation"
	KindConflict    ErrorKind = "conflict"
	KindNotFound    ErrorKind = "not_found"
	KindInternal    ErrorKind = "internal"
	KindApproval    ErrorKind = "approval"
	KindUnsupported ErrorKind = "unsupported"
)

const (
	CodeNotImplemented          = "not_implemented"
	CodeInternal                = "internal_error"
	CodeActiveRunConflict       = "active_run_conflict"
	CodeInvalidRequest          = "invalid_request"
	CodeCursorNotFound          = "cursor_not_found"
	CodeSubmissionUnsupported   = "submission_unsupported"
	CodeApprovalNotPending      = "approval_not_pending"
	CodeSessionNotFound         = "session_not_found"
	CodeSessionAmbiguous        = "session_ambiguous"
	CodeBindingNotFound         = "binding_not_found"
	CodeNoResumableSession      = "no_resumable_session"
	CodeNoActiveRun             = "no_active_run"
	CodeModeNotFound            = "mode_not_found"
	CodeControlPlaneUnsupported = "control_plane_unsupported"
)

type Error struct {
	Kind        ErrorKind `json:"kind"`
	Code        string    `json:"code,omitempty"`
	Retryable   bool      `json:"retryable,omitempty"`
	UserVisible bool      `json:"user_visible,omitempty"`
	Message     string    `json:"message,omitempty"`
	Detail      string    `json:"detail,omitempty"`
	Cause       error     `json:"-"`
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return string(e.Kind) + ":" + e.Code
	}
	return string(e.Kind)
}

func (e *Error) Unwrap() error { return e.Cause }

func EventError(err error) *Error {
	if err == nil {
		return nil
	}
	var gatewayErr *Error
	if errors.As(err, &gatewayErr) {
		return gatewayErr
	}
	return &Error{
		Kind:    KindInternal,
		Code:    CodeInternal,
		Message: err.Error(),
		Cause:   err,
	}
}

func As(err error, target any) bool { return errors.As(err, target) }
