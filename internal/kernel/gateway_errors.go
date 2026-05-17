package kernel

import (
	"errors"

	"github.com/OnslaughtSnail/caelis/ports/session"
)

func wrapSessionError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, session.ErrSessionNotFound):
		return &Error{
			Kind:        KindNotFound,
			Code:        CodeSessionNotFound,
			UserVisible: true,
			Message:     "gateway: session not found",
			Cause:       err,
		}
	case errors.Is(err, session.ErrAmbiguousSession):
		return &Error{
			Kind:        KindValidation,
			Code:        CodeInvalidRequest,
			UserVisible: true,
			Message:     "gateway: session workspace is ambiguous",
			Cause:       err,
		}
	case errors.Is(err, session.ErrInvalidSession):
		return &Error{
			Kind:        KindValidation,
			Code:        CodeInvalidRequest,
			UserVisible: true,
			Message:     "gateway: invalid session request",
			Cause:       err,
		}
	default:
		return err
	}
}
