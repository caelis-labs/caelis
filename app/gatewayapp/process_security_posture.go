package gatewayapp

import (
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

const yoloSecuritySummary = "YOLO: unrestricted host access; approval review disabled"

// processSecurityPosture resolves process-owned execution semantics separately
// from mutable user and Session configuration.
type processSecurityPosture struct {
	FullAccessMode         bool
	DisplayMode            string
	PolicyMode             string
	RequiredSandboxBackend sandbox.Backend
}

func resolveProcessSecurityPosture(config stackRuntimeConfig) processSecurityPosture {
	posture := processSecurityPosture{
		DisplayMode: approvalMode(config.ApprovalMode),
		PolicyMode:  policyProfile(config.PolicyProfile),
	}
	if !config.DangerouslySkipPermissions {
		return posture
	}
	posture.FullAccessMode = true
	posture.DisplayMode = dangerouslySkipPermissionsModeLabel
	posture.PolicyMode = presets.ModeDangerFullAccess
	posture.RequiredSandboxBackend = sandbox.BackendHost
	return posture
}

func (s *runtimeComposition) processSecurityPosture() processSecurityPosture {
	if s == nil {
		return resolveProcessSecurityPosture(stackRuntimeConfig{})
	}
	config := s.runtimeProcessSnapshot().runtime
	return resolveProcessSecurityPosture(config)
}

func (p processSecurityPosture) validateSessionModeMutation() error {
	if !p.FullAccessMode {
		return nil
	}
	return errors.New("gatewayapp: session approval mode cannot be changed while YOLO mode is active; restart without --dangerously-skip-permissions to restore approval routing")
}

func (p processSecurityPosture) validateSandboxBackend(backend sandbox.Backend) error {
	if p.RequiredSandboxBackend == "" || strings.EqualFold(string(backend), string(p.RequiredSandboxBackend)) {
		return nil
	}
	return errors.New("gatewayapp: YOLO mode fixes the sandbox backend to Host for this process")
}

func (p processSecurityPosture) applySandboxStatus(status SandboxStatus) SandboxStatus {
	if !p.FullAccessMode {
		return status
	}
	status.RequestedBackend = string(p.RequiredSandboxBackend)
	status.ResolvedBackend = string(p.RequiredSandboxBackend)
	status.Route = string(sandbox.RouteHost)
	status.SecuritySummary = yoloSecuritySummary
	status.FullAccessMode = true
	return status
}
