package controller

import (
	"context"
	"encoding/json"
	"fmt"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func (m *Manager) promptControllerRun(
	ctx context.Context,
	run *controllerRun,
	prompt []json.RawMessage,
	contextTransfer agent.ContextTransfer,
	contextSyncSeq uint64,
	contextFresh bool,
	req controller.TurnRequest,
) (agent.ContextTransfer, uint64, bool, error) {
	attemptedContext := agent.CloneContextTransfer(contextTransfer)
	attemptedContextSyncSeq := contextSyncSeq
	attemptedContextFresh := contextFresh
	reconnect := func() error {
		fresh, err := m.reconnectControllerRun(ctx, run)
		if fresh {
			// Retain the bootstrap even if configuration or binding publication
			// fails after creating the remote. The next Turn still needs it.
			attemptedContext = agent.CloneContextTransfer(req.FreshContext)
			attemptedContextSyncSeq = req.ContextSyncSeq
			attemptedContextFresh = true
		}
		return err
	}
	run.mu.Lock()
	ready := run.client == nil || run.client.CollaborationReady()
	run.mu.Unlock()
	if !ready {
		if err := reconnect(); err != nil {
			return attemptedContext, attemptedContextSyncSeq, attemptedContextFresh, err
		}
	}
	if req.CommitBinding != nil {
		if err := req.CommitBinding(ctx); err != nil {
			return attemptedContext, attemptedContextSyncSeq, attemptedContextFresh, err
		}
	}
	if _, err := run.promptWithCollaboration(ctx, composeACPContextPrompt(prompt, attemptedContext)); err != nil {
		if client.DispatchMayHaveCommitted(err) {
			return attemptedContext, attemptedContextSyncSeq, attemptedContextFresh, errorcode.Wrap(errorcode.UnknownOutcome, "internal/acpagentbridge/controller: controller prompt outcome cannot be proven", err)
		}
		if !isACPClientConnectionError(err) {
			return attemptedContext, attemptedContextSyncSeq, attemptedContextFresh, err
		}
		if !client.SubmissionProvenNotStarted(err) {
			return attemptedContext, attemptedContextSyncSeq, attemptedContextFresh, errorcode.Wrap(errorcode.UnknownOutcome, "internal/acpagentbridge/controller: controller prompt outcome cannot be proven", err)
		}
		if reconnectErr := reconnect(); reconnectErr != nil {
			return attemptedContext, attemptedContextSyncSeq, attemptedContextFresh, fmt.Errorf("%w; reconnect failed: %w", err, reconnectErr)
		}
		if req.CommitBinding != nil {
			if err := req.CommitBinding(ctx); err != nil {
				return attemptedContext, attemptedContextSyncSeq, attemptedContextFresh, err
			}
		}
		_, err = run.promptWithCollaboration(ctx, composeACPContextPrompt(prompt, attemptedContext))
		if err != nil && (client.DispatchMayHaveCommitted(err) || !client.SubmissionProvenNotStarted(err)) {
			err = errorcode.Wrap(errorcode.UnknownOutcome, "internal/acpagentbridge/controller: retried controller prompt outcome cannot be proven", err)
		}
		return attemptedContext, attemptedContextSyncSeq, attemptedContextFresh, err
	}
	return attemptedContext, attemptedContextSyncSeq, attemptedContextFresh, nil
}
