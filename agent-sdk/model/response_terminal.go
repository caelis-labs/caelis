package model

// TerminalResponseError rejects an explicitly unsuccessful provider termination.
// It preserves the normalized reason when an adapter cannot emit a final
// Response. Callers must not retry it or use another generation path to bypass
// the rejection. An interrupted transport is a separate failure class.
type TerminalResponseError struct {
	Reason FinishReason
	Detail string
}

func (e *TerminalResponseError) Error() string { return e.Detail }

// Retryable is false because the provider has already terminated this attempt.
func (*TerminalResponseError) Retryable() bool { return false }
