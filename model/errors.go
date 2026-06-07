package model

import "errors"

// ContextOverflowError indicates that a request exceeded the model context
// window. Providers should wrap their native error in this type when possible.
type ContextOverflowError struct {
	Cause error
}

func (e *ContextOverflowError) Error() string {
	if e == nil || e.Cause == nil {
		return "model context overflow"
	}
	return "model context overflow: " + e.Cause.Error()
}

func (e *ContextOverflowError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// IsContextOverflow reports whether err indicates a context-window overflow.
func IsContextOverflow(err error) bool {
	var overflow *ContextOverflowError
	return errors.As(err, &overflow)
}
