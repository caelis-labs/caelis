package appserver

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
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

func (s *ApplicationService) CreateWorker(ctx context.Context, p Principal, req CreateWorkerRequest) (CommandResult, error) {
	if req.SessionID != "" || req.ExpectedRevision != nil || req.ExpectedControllerEpoch != "" || !filepath.IsAbs(req.CWD) || strings.TrimSpace(req.CWD) != req.CWD {
		return CommandResult{}, errorcode.New(errorcode.InvalidArgument, "worker creation requires an absolute cwd and no Session or revision")
	}
	if req.Model == "" && (req.ReasoningEffort != "" || req.FastMode) {
		return CommandResult{}, errorcode.New(errorcode.InvalidArgument, "worker model options require a model")
	}
	return s.execute(ctx, p, req.OperationID, req, func() (CommandResult, error) {
		return s.config.Commands.execute(ctx, p, ActionWorkerCreate, req.WriteBase, "", req)
	})
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
