package runtimeinput

import (
	"context"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// FinalSubmissionDrainer closes admission atomically with an empty final drain.
// It is not used between tool steps, when the current Turn must remain open.
type FinalSubmissionDrainer interface {
	DrainFinalSubmissions() ([]agent.Submission, error)
}

// SubmissionCommitter applies input hooks and persists one complete admission
// before returning its canonical model messages, including for a batch.
type SubmissionCommitter interface {
	CommitSubmission(agent.Submission) ([]model.Message, error)
}

// ContextEditor exposes only guarded context commits at a model safe point.
// It never hands an extension a Store or execution fence.
type ContextEditor interface {
	ContextEditSource() (agent.ContextSource, error)
	CommitContextEdit(string, string, agent.ContextEdit, func(context.Context) (func(), error)) ([]*session.Event, error)
}
