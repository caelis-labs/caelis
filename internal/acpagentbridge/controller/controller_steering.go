package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	sdkcontroller "github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/authentication"
	"github.com/caelis-labs/caelis/protocol/acp/client"
)

func (m *Manager) SteerController(ctx context.Context, req sdkcontroller.ControllerSteerRequest) error {
	req = sdkcontroller.NormalizeControllerSteerRequest(req)
	if req.SessionRef.SessionID == "" || req.ControllerID == "" || req.ControllerEpoch == "" ||
		req.RemoteSessionID == "" || req.TurnID == "" {
		return errorcode.New(errorcode.InvalidArgument, "internal/acpagentbridge/controller: main steering requires Session, controller, epoch, remote Session, and Turn IDs")
	}
	if req.Commit == nil {
		return errorcode.New(errorcode.InvalidArgument, "internal/acpagentbridge/controller: main steering commit is required")
	}
	m.mu.RLock()
	run := m.controllers[req.SessionRef.SessionID]
	m.mu.RUnlock()
	if run == nil {
		return mainControllerInputClosedError("main steering target is no longer active")
	}
	run.operationMu.Lock()
	isolate, err := run.steerControllerLocked(ctx, req, func() bool { return m.isActiveControllerRun(run) })
	run.operationMu.Unlock()
	if !isolate {
		return err
	}
	m.mu.Lock()
	if m.controllers[req.SessionRef.SessionID] == run {
		delete(m.controllers, req.SessionRef.SessionID)
	}
	m.mu.Unlock()
	m.shutdownControllerRun(context.WithoutCancel(ctx), run, false)
	return err
}

func (r *controllerRun) steerControllerLocked(
	ctx context.Context,
	req sdkcontroller.ControllerSteerRequest,
	isActive func() bool,
) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	prompt := buildPromptParts(req.Input, req.ContentParts)
	if len(prompt) == 0 {
		return false, errorcode.New(errorcode.InvalidArgument, "internal/acpagentbridge/controller: main steering input is required")
	}
	r.mu.Lock()
	switch {
	case r.turnAdmissionClosed:
		r.mu.Unlock()
		return false, mainControllerInputClosedError("main steering target is detached")
	case r.handle == nil || strings.TrimSpace(r.turnID) != req.TurnID:
		r.mu.Unlock()
		return false, mainControllerInputClosedError("main steering Turn changed")
	case strings.TrimSpace(r.binding.ControllerID) != req.ControllerID || strings.TrimSpace(r.binding.EpochID) != req.ControllerEpoch:
		r.mu.Unlock()
		return false, errorcode.New(errorcode.Conflict, "internal/acpagentbridge/controller: main steering controller changed")
	case strings.TrimSpace(r.remoteSessionID) != req.RemoteSessionID:
		r.mu.Unlock()
		return false, errorcode.New(errorcode.Conflict, "internal/acpagentbridge/controller: main steering remote Session changed")
	case !r.turnStream:
		r.mu.Unlock()
		return false, errorcode.New(errorcode.FailedPrecondition, "internal/acpagentbridge/controller: main steering requires a live event stream")
	case !r.supportsSteering:
		r.mu.Unlock()
		return false, sdkcontroller.ErrControllerSteeringUnsupported
	case contentPartsContainImage(req.ContentParts) && !r.promptCapabilities.Image:
		r.mu.Unlock()
		return false, errorcode.New(errorcode.Unsupported, "internal/acpagentbridge/controller: main controller does not support image steering")
	}
	handle := r.handle
	rpcCtx, cancelRPC := context.WithCancel(ctx)
	r.steeringActive = true
	r.steeringCancel = cancelRPC
	r.steeringUpdates = nil
	r.mu.Unlock()

	if isActive == nil || !isActive() {
		cancelRPC()
		r.settleControllerSteeringBuffer(true)
		return false, mainControllerInputClosedError("main steering controller was replaced")
	}
	if err := handle.synchronize(rpcCtx); err != nil {
		cancelRPC()
		r.settleControllerSteeringBuffer(true)
		if errors.Is(err, sdkcontroller.ErrNotActive) {
			return false, mainControllerInputClosedError("main steering event stream is closed")
		}
		return false, errorcode.Wrap(errorcode.FailedPrecondition, "internal/acpagentbridge/controller: main steering was not dispatched", err)
	}
	response, err := r.steerParts(rpcCtx, prompt)
	cancelRPC()
	if err != nil {
		unknown := errorcode.Wrap(errorcode.UnknownOutcome, "internal/acpagentbridge/controller: main steering outcome cannot be proven", err)
		r.quarantineControllerSteering(unknown)
		return true, unknown
	}
	switch response.Outcome {
	case client.SessionSteeringInjected:
		if err := req.Commit(); err != nil {
			unknown := errorcode.Wrap(errorcode.UnknownOutcome, "internal/acpagentbridge/controller: remote main steering committed but canonical input persistence failed", err)
			r.quarantineControllerSteering(unknown)
			return true, unknown
		}
		r.settleControllerSteeringBuffer(true)
		return false, nil
	case client.SessionSteeringFailed, client.SessionSteeringPromptRequired:
		r.settleControllerSteeringBuffer(true)
		return false, errorcode.New(errorcode.FailedPrecondition, fmt.Sprintf("internal/acpagentbridge/controller: main steering was not injected: %s", response.Outcome))
	case client.SessionSteeringStartedNewTurn:
		unknown := errorcode.New(errorcode.UnknownOutcome, "internal/acpagentbridge/controller: main steering started an unaddressed remote Turn")
		r.quarantineControllerSteering(unknown)
		return true, unknown
	default:
		unknown := errorcode.New(errorcode.UnknownOutcome, fmt.Sprintf("internal/acpagentbridge/controller: main steering returned unknown outcome %q", response.Outcome))
		r.quarantineControllerSteering(unknown)
		return true, unknown
	}
}

func mainControllerInputClosedError(detail string) error {
	return fmt.Errorf("internal/acpagentbridge/controller: %s: %w", strings.TrimSpace(detail), agent.ErrRunInputClosed)
}

func (r *controllerRun) steerParts(ctx context.Context, prompt []json.RawMessage) (client.SessionSteeringResponse, error) {
	r.mu.Lock()
	acpClient := r.client
	remoteSessionID := strings.TrimSpace(r.remoteSessionID)
	agentID := firstNonEmpty(r.agent, r.cfg.Name)
	configured := controlagents.NormalizeAuthentication(r.cfg.Authentication)
	methods := controlagents.CloneAuthenticationMethods(r.authenticationMethods)
	r.mu.Unlock()
	if acpClient == nil {
		return client.SessionSteeringResponse{}, errors.New("internal/acpagentbridge/controller: main controller client is unavailable")
	}
	if remoteSessionID == "" {
		return client.SessionSteeringResponse{}, errors.New("internal/acpagentbridge/controller: main controller remote Session ID is unavailable")
	}
	return authentication.RecoverConfiguredCall(
		ctx,
		acpClient,
		methods,
		agentID,
		configured,
		func(callCtx context.Context, activeClient *client.Client) (client.SessionSteeringResponse, error) {
			return activeClient.SteerPartsWithAbort(callCtx, remoteSessionID, prompt, nil, func() {
				_ = activeClient.Close(callCtx)
			})
		},
	)
}

func (r *controllerRun) settleControllerSteeringBuffer(release bool) {
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

func (r *controllerRun) quarantineControllerSteering(err error) {
	r.mu.Lock()
	handle := r.handle
	r.turnAdmissionClosed = true
	r.steeringUpdates = nil
	r.steeringActive = false
	r.steeringCancel = nil
	r.mu.Unlock()
	if handle != nil {
		handle.publishError(err)
		handle.Cancel()
	}
}
