package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	sdkcontroller "github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/authentication"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpcleanup"
	"github.com/caelis-labs/caelis/protocol/acp/client"
)

func (m *Manager) SteerParticipant(ctx context.Context, req sdkcontroller.ParticipantSteerRequest) error {
	req = sdkcontroller.NormalizeParticipantSteerRequest(req)
	if req.SessionRef.SessionID == "" || req.ParticipantID == "" || req.TurnID == "" {
		return errorcode.New(errorcode.InvalidArgument, "internal/acpagentbridge/controller: participant steering requires Session, participant, and Turn IDs")
	}
	if req.Commit == nil {
		return errorcode.New(errorcode.InvalidArgument, "internal/acpagentbridge/controller: participant steering commit is required")
	}
	key := participantKey(req.SessionRef.SessionID, req.ParticipantID)
	m.mu.RLock()
	run := m.participants[key]
	m.mu.RUnlock()
	if run == nil {
		return errorcode.New(errorcode.Conflict, "internal/acpagentbridge/controller: participant steering target is no longer active")
	}
	isolate, err := run.steerParticipant(ctx, req)
	if !isolate {
		return err
	}
	m.mu.Lock()
	if m.participants[key] == run {
		delete(m.participants, key)
	}
	m.mu.Unlock()
	closeErr := run.closeClient(ctx)
	return errors.Join(err, closeErr)
}

func (r *participantRun) closeClient(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.mu.Lock()
		acpClient := r.client
		r.mu.Unlock()
		r.closeErr = acpcleanup.CloseClient(ctx, acpClient)
	})
	return r.closeErr
}

func (r *participantRun) steerParticipant(ctx context.Context, req sdkcontroller.ParticipantSteerRequest) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()

	prompt := buildPromptParts(req.Input, req.ContentParts)
	if len(prompt) == 0 {
		return false, errorcode.New(errorcode.InvalidArgument, "internal/acpagentbridge/controller: participant steering input is required")
	}
	r.mu.Lock()
	switch {
	case r.promptAdmissionClosed:
		r.mu.Unlock()
		return false, errorcode.New(errorcode.Conflict, "internal/acpagentbridge/controller: participant steering target is detached")
	case r.handle == nil || strings.TrimSpace(r.turnID) != req.TurnID:
		r.mu.Unlock()
		return false, errorcode.New(errorcode.Conflict, "internal/acpagentbridge/controller: participant steering Turn changed")
	case !r.turnStream:
		r.mu.Unlock()
		return false, errorcode.New(errorcode.FailedPrecondition, "internal/acpagentbridge/controller: participant steering requires a live event stream")
	case !r.supportsSteering:
		r.mu.Unlock()
		return false, sdkcontroller.ErrParticipantSteeringUnsupported
	case contentPartsContainImage(req.ContentParts) && !r.promptCapabilities.Image:
		r.mu.Unlock()
		return false, errorcode.New(errorcode.Unsupported, "internal/acpagentbridge/controller: participant does not support image steering")
	}
	handle := r.handle
	rpcCtx, cancelRPC := context.WithCancel(ctx)
	r.steeringActive = true
	r.steeringCancel = cancelRPC
	r.steeringUpdates = nil
	r.mu.Unlock()

	if err := handle.synchronize(rpcCtx); err != nil {
		cancelRPC()
		r.settleSteeringBuffer(true)
		return false, errorcode.Wrap(errorcode.FailedPrecondition, "internal/acpagentbridge/controller: participant steering was not dispatched", err)
	}
	response, err := r.steerParts(rpcCtx, prompt)
	cancelRPC()
	if err != nil {
		unknown := errorcode.Wrap(errorcode.UnknownOutcome, "internal/acpagentbridge/controller: participant steering outcome cannot be proven", err)
		r.quarantineSteering(unknown)
		return true, unknown
	}
	switch response.Outcome {
	case client.SessionSteeringInjected:
		if err := req.Commit(); err != nil {
			unknown := errorcode.Wrap(errorcode.UnknownOutcome, "internal/acpagentbridge/controller: remote steering committed but canonical input persistence failed", err)
			r.quarantineSteering(unknown)
			return true, unknown
		}
		r.settleSteeringBuffer(true)
		return false, nil
	case client.SessionSteeringFailed, client.SessionSteeringPromptRequired:
		r.settleSteeringBuffer(true)
		return false, errorcode.New(errorcode.FailedPrecondition, fmt.Sprintf("internal/acpagentbridge/controller: participant steering was not injected: %s", response.Outcome))
	case client.SessionSteeringStartedNewTurn:
		unknown := errorcode.New(errorcode.UnknownOutcome, "internal/acpagentbridge/controller: participant steering started an unaddressed remote Turn")
		r.quarantineSteering(unknown)
		return true, unknown
	default:
		unknown := errorcode.New(errorcode.UnknownOutcome, fmt.Sprintf("internal/acpagentbridge/controller: participant steering returned unknown outcome %q", response.Outcome))
		r.quarantineSteering(unknown)
		return true, unknown
	}
}

func (r *participantRun) steerParts(ctx context.Context, prompt []json.RawMessage) (client.SessionSteeringResponse, error) {
	r.mu.Lock()
	acpClient := r.client
	remoteSessionID := strings.TrimSpace(r.remoteSessionID)
	agentID := strings.TrimSpace(r.agent)
	configured := controlagents.NormalizeAuthentication(r.authentication)
	methods := controlagents.CloneAuthenticationMethods(r.authenticationMethods)
	r.mu.Unlock()
	if acpClient == nil {
		return client.SessionSteeringResponse{}, errors.New("internal/acpagentbridge/controller: participant client is unavailable")
	}
	if remoteSessionID == "" {
		return client.SessionSteeringResponse{}, errors.New("internal/acpagentbridge/controller: participant remote session id is unavailable")
	}
	return authentication.RecoverConfiguredCall(
		ctx,
		acpClient,
		methods,
		agentID,
		configured,
		func(callCtx context.Context, activeClient *client.Client) (client.SessionSteeringResponse, error) {
			return activeClient.SteerParts(callCtx, remoteSessionID, prompt, nil)
		},
	)
}

func (r *participantRun) settleSteeringBuffer(release bool) {
	r.mu.Lock()
	updates := r.steeringUpdates
	handle := r.handle
	stream := r.turnStream
	r.steeringUpdates = nil
	r.steeringActive = false
	r.steeringCancel = nil
	if release && stream && handle != nil {
		for _, update := range updates {
			handle.publish(update)
		}
	}
	r.mu.Unlock()
}

func (r *participantRun) quarantineSteering(err error) {
	r.mu.Lock()
	handle := r.handle
	r.promptAdmissionClosed = true
	r.steeringUpdates = nil
	r.steeringActive = false
	r.steeringCancel = nil
	r.mu.Unlock()
	if handle != nil {
		handle.publishError(err)
		handle.Cancel()
	}
}
