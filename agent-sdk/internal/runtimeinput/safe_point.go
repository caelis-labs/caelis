package runtimeinput

import (
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// BatchCommitter is installed by Runtime on a local Agent context. The Agent
// calls it synchronously at a safe point; Runtime persists and publishes the
// complete admission before returning its canonical model messages.
type BatchCommitter interface {
	CommitAgentInputBatch([]agent.AgentCommunicationInput) ([]model.Message, error)
}

// FinalSubmissionDrainer closes admission atomically with an empty final drain.
// It is not used between tool steps, when the current Turn must remain open.
type FinalSubmissionDrainer interface {
	DrainFinalSubmissions() []agent.Submission
}
