package appserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/application"
)

const ActionWorkerCreate Action = "worker.create"

const (
	CapabilitySharedWorkers = "shared-native-workers-v1"
	CapabilityTurnSteering  = "turn-steering-receipts-v1"
)

// CreateWorkerRequest creates an ordinary Host-configured Session with a scoped
// application grant. The Host allocates identity; no application profile is inherited.
type CreateWorkerRequest struct {
	WriteBase
	CWD   string `json:"cwd"`
	Title string `json:"title,omitempty"`
	// Model optionally selects a configured Host model for this Session only.
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	FastMode        bool   `json:"fast_mode,omitempty"`
}

// workerCreateOperation identifies the complete create-and-configure command.
// A grant or Session alone cannot prove that its optional model selection finished.
type workerCreateOperation struct {
	Kind    Action              `json:"kind"`
	Request CreateWorkerRequest `json:"request"`
}

func (s *ApplicationService) CreateWorker(ctx context.Context, p Principal, req CreateWorkerRequest) (CommandResult, error) {
	if req.SessionID != "" || req.ExpectedRevision != nil || req.ExpectedControllerEpoch != "" || !filepath.IsAbs(req.CWD) || strings.TrimSpace(req.CWD) != req.CWD {
		return CommandResult{}, errorcode.New(errorcode.InvalidArgument, "worker creation requires an absolute cwd and no Session or revision")
	}
	if req.Model == "" && (req.ReasoningEffort != "" || req.FastMode) {
		return CommandResult{}, errorcode.New(errorcode.InvalidArgument, "worker model options require a model")
	}
	return s.execute(ctx, p, req.OperationID, workerCreateOperation{Kind: ActionWorkerCreate, Request: req}, func() (CommandResult, error) {
		return s.config.Commands.execute(ctx, p, ActionWorkerCreate, req.WriteBase, "", req)
	})
}

func (s *ApplicationService) observeWorkerCreation(ctx context.Context, p Principal, scope application.Scope, op application.Operation) (ApplicationOperation, error) {
	out := ApplicationOperation{OperationID: op.ID, Outcome: OutcomeUnknown}
	var saved workerCreateOperation
	if err := json.Unmarshal(op.Request, &saved); err != nil {
		return out, err
	}
	if saved.Kind != ActionWorkerCreate || saved.Request.OperationID != op.ID {
		return out, application.ErrConflict
	}
	intent, err := commandOperationIntent(p, ActionWorkerCreate, saved.Request.WriteBase, "", saved.Request)
	if err != nil {
		return out, err
	}
	record, found, err := s.config.Commands.config.Operations.Lookup(ctx, intent)
	if err != nil || !found || record.Result == nil {
		return out, err
	}
	if record.Result.Outcome == OutcomeCommitted && record.Result.SessionID != application.WorkerSessionID(scope, op.ID) {
		return out, application.ErrConflict
	}
	data, err := json.Marshal(record.Result)
	if err != nil {
		return out, err
	}
	completion, cancel := operationCompletionContext(ctx)
	defer cancel()
	if err := s.config.Store.CompleteOperation(completion, scope, op.ID, data); err != nil {
		return out, err
	}
	out.Result = record.Result
	out.Outcome = record.Result.Outcome
	return out, nil
}

// WorkerPrompt uses the ordinary Turn owner and a permanent application intent.
func (s *ApplicationService) WorkerPrompt(ctx context.Context, p Principal, req PromptRequest) (CommandResult, error) {
	return s.execute(ctx, p, req.OperationID, req, func() (CommandResult, error) {
		return s.config.Commands.Prompt(ctx, p, req)
	})
}

// Steer persists an application intent before submitting to the exact Turn.
// The command receipt proves admission; the canonical input event proves application.
func (s *ApplicationService) Steer(ctx context.Context, p Principal, req SteerRequest) (CommandResult, error) {
	return s.execute(ctx, p, req.OperationID, req, func() (CommandResult, error) {
		return s.config.Commands.Steer(ctx, p, req)
	})
}
