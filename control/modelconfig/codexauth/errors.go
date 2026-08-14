package codexauth

import "github.com/caelis-labs/caelis/agent-sdk/errorcode"

type terminalAuthenticationError struct {
	cause error
}

func (e *terminalAuthenticationError) Error() string {
	if e == nil || e.cause == nil {
		return "codex oauth authentication failed"
	}
	return e.cause.Error()
}

func (e *terminalAuthenticationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *terminalAuthenticationError) Retryable() bool { return false }

func (e *terminalAuthenticationError) ErrorCode() errorcode.Code {
	return errorcode.Unauthenticated
}
