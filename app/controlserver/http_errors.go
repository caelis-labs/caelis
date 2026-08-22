package controlserver

import (
	"errors"
	"net/http"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func statusForError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if errors.Is(err, eventstream.ErrInvalidCursor) ||
		errors.Is(err, eventstream.ErrCursorSessionMismatch) ||
		errors.Is(err, eventstream.ErrCursorVersion) {
		return http.StatusBadRequest
	}
	switch errorcode.CodeOf(err) {
	case errorcode.InvalidArgument:
		return http.StatusBadRequest
	case errorcode.Unauthenticated:
		return http.StatusUnauthorized
	case errorcode.PermissionDenied:
		return http.StatusForbidden
	case errorcode.NotFound:
		return http.StatusNotFound
	case errorcode.AlreadyExists, errorcode.Conflict, errorcode.FailedPrecondition:
		return http.StatusConflict
	case errorcode.Unavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func writeMappedError(w http.ResponseWriter, err error) {
	status := statusForError(err)
	code := errorcode.CodeOf(err)
	if code == errorcode.Unknown {
		code = errorCodeForStatus(status)
	}
	detail := "internal server error"
	switch status {
	case http.StatusBadRequest:
		detail = err.Error()
	case http.StatusUnauthorized:
		w.Header().Set("WWW-Authenticate", `Bearer realm="caelis-control"`)
		detail = "authentication required"
	case http.StatusForbidden:
		detail = "forbidden"
	case http.StatusNotFound:
		detail = "not found"
	case http.StatusConflict:
		detail = "conflict"
	case http.StatusServiceUnavailable:
		detail = "service unavailable"
	}
	writeErrorIdentity(w, status, detail, code, appserver.ErrorKindOf(err))
}

func errorCodeForStatus(status int) errorcode.Code {
	switch status {
	case http.StatusBadRequest:
		return errorcode.InvalidArgument
	case http.StatusUnauthorized:
		return errorcode.Unauthenticated
	case http.StatusForbidden:
		return errorcode.PermissionDenied
	case http.StatusNotFound:
		return errorcode.NotFound
	case http.StatusConflict:
		return errorcode.Conflict
	case http.StatusServiceUnavailable:
		return errorcode.Unavailable
	case http.StatusRequestEntityTooLarge:
		return errorcode.ResourceExhausted
	default:
		return errorcode.Internal
	}
}
