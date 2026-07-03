package kernel

import gateway "github.com/caelis-labs/caelis/ports/gateway"

type ErrorKind = gateway.ErrorKind

const (
	KindValidation  = gateway.KindValidation
	KindConflict    = gateway.KindConflict
	KindNotFound    = gateway.KindNotFound
	KindInternal    = gateway.KindInternal
	KindApproval    = gateway.KindApproval
	KindUnsupported = gateway.KindUnsupported
)

const (
	CodeNotImplemented          = gateway.CodeNotImplemented
	CodeInternal                = gateway.CodeInternal
	CodeActiveRunConflict       = gateway.CodeActiveRunConflict
	CodeInvalidRequest          = gateway.CodeInvalidRequest
	CodeCursorNotFound          = gateway.CodeCursorNotFound
	CodeSubmissionUnsupported   = gateway.CodeSubmissionUnsupported
	CodeApprovalNotPending      = gateway.CodeApprovalNotPending
	CodeSessionNotFound         = gateway.CodeSessionNotFound
	CodeSessionAmbiguous        = gateway.CodeSessionAmbiguous
	CodeBindingNotFound         = gateway.CodeBindingNotFound
	CodeNoResumableSession      = gateway.CodeNoResumableSession
	CodeNoActiveRun             = gateway.CodeNoActiveRun
	CodeModeNotFound            = gateway.CodeModeNotFound
	CodeControlPlaneUnsupported = gateway.CodeControlPlaneUnsupported
)

type Error = gateway.Error

var EventError = gateway.EventError
var As = gateway.As
