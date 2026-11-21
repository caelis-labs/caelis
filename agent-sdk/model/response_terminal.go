package model

// TerminalResponseError rejects an explicitly unsuccessful provider termination.
// The detail reports why the adapter cannot emit a final Response. Callers
// must stop generation on this error.
type TerminalResponseError struct {
	Detail string
}

func (e *TerminalResponseError) Error() string { return e.Detail }

// Retryable is false because the provider has already terminated this attempt.
func (*TerminalResponseError) Retryable() bool { return false }
