package runtimeinput

import (
	"context"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
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

// SubmissionCommitter persists one effective input through Runtime input hooks.
type SubmissionCommitter interface {
	CommitSubmission(agent.Submission) ([]model.Message, error)
}

// ContextEditor exposes only guarded context commits at a model safe point.
// It never hands an extension a Store or execution fence.
type ContextEditor interface {
	ContextEditSource() (agent.ContextSource, error)
	CommitContextEdit(string, string, agent.ContextEdit, func(context.Context) (func(), error)) ([]*session.Event, error)
}
