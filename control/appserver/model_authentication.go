package appserver

// CapabilityModelAuthStream advertises interactive ConnectModel HTTP snapshots.
const CapabilityModelAuthStream = "model-auth-stream-v1"

// ModelAuthenticationSnapshot is transient presentation state for the caller of
// a streamed ConnectModel command. It is not Session history or a command receipt.
// Result, when present, is the existing command ledger's authoritative outcome.
type ModelAuthenticationSnapshot struct {
	OperationID     string         `json:"operation_id"`
	Sequence        uint64         `json:"sequence"`
	Phase           string         `json:"phase"`
	VerificationURL string         `json:"verification_url,omitempty"`
	UserCode        string         `json:"user_code,omitempty"`
	ChallengeID     string         `json:"challenge_id,omitempty"`
	Prompt          string         `json:"prompt,omitempty"`
	Result          *CommandResult `json:"result,omitempty"`
}

// ModelAuthenticationInput is a write-only response to one live, principal-bound
// input challenge. Input must never be logged, persisted or sent to a Session.
type ModelAuthenticationInput struct {
	ChallengeID string `json:"challenge_id"`
	Input       string `json:"input"`
}
