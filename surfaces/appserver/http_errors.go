package appserver

import (
	"errors"
	"net/http"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
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
	case errorcode.AlreadyExists, errorcode.Conflict, errorcode.FailedPrecondition:
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func writeMappedError(w http.ResponseWriter, err error) {
	status := statusForError(err)
	detail := "internal server error"
	switch status {
	case http.StatusBadRequest:
		detail = err.Error()
	case http.StatusUnauthorized:
		w.Header().Set("WWW-Authenticate", `Bearer realm="caelis-control"`)
		detail = "authentication required"
	case http.StatusForbidden:
		detail = "forbidden"
	case http.StatusConflict:
		detail = "conflict"
	}
	writeError(w, status, detail)
}
