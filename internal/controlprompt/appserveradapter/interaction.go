package appserveradapter

import (
	"context"
	"errors"
	"slices"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/google/uuid"
)

// RecordInteraction is the shared router's durable command-input admission.
// The same native principal-bound client handles TUI, ACP and headless use.
func (a *SessionClientAdapter) RecordInteraction(ctx context.Context, text string) (string, error) {
	return a.recordInteraction(ctx, text, false)
}

// RecordInteractionIfIdle checks the foreground condition atomically in the
// same native write gate that persists the command's conversation receipt.
func (a *SessionClientAdapter) RecordInteractionIfIdle(ctx context.Context, text string) (string, error) {
	return a.recordInteraction(ctx, text, true)
}

func (a *SessionClientAdapter) recordInteraction(ctx context.Context, text string, requireIdle bool) (string, error) {
	if a == nil || a.sessionClient == nil || a.clientSessionID() == "" {
		return "", nil
	}
	client, ok := a.sessionClient.(appserver.InteractionClient)
	if !ok {
		// Older embedded clients do not promise a durable conversation receipt.
		// Stable surfaces retain their pre-capability command behavior; a v2
		// ingress requires advertised support before admitting any work.
		return "", nil
	}
	info, err := a.sessionClient.Initialize(ctx)
	if err != nil {
		return "", err
	}
	if !slices.Contains(info.Capabilities, appserver.CapabilityConversationInteractions) {
		return "", nil
	}
	state, err := a.currentClientSessionState(ctx)
	if err != nil {
		return "", err
	}
	result, err := client.RecordInteraction(ctx, appserver.RecordInteractionRequest{
		WriteBase: appserver.WriteBase{OperationID: "interaction-" + uuid.NewString(), SessionID: state.SessionID,
			ExpectedRevision: &state.Revision, ExpectedControllerEpoch: state.Controller.EpochID},
		Text: text, RequireIdleRun: requireIdle,
	})
	var id string
	if result.Resource != nil && result.Resource.Kind == appserver.CommandResourceConversationMessage {
		id = result.Resource.Ref
	}
	if mutationErr := appserver.CommandMutationError(result, err); mutationErr != nil {
		// A failed ledger receipt can follow a successful durable append. Keep
		// the known identity while preventing the router's command effect.
		return id, mutationErr
	}
	if id == "" {
		return "", errors.New("control prompt: interaction receipt has no durable message identity")
	}
	return id, nil
}
