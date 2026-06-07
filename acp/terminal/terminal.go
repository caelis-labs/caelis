// Package terminal exposes the ACP terminal subdomain without making callers
// depend on the large root acp package surface.
package terminal

import "github.com/OnslaughtSnail/caelis/acp"

const (
	MethodCreate      = acp.MethodTerminalCreate
	MethodOutput      = acp.MethodTerminalOutput
	MethodWaitForExit = acp.MethodTerminalWaitForExit
	MethodKill        = acp.MethodTerminalKill
	MethodRelease     = acp.MethodTerminalRelease

	MethodTerminalCreate      = acp.MethodTerminalCreate
	MethodTerminalOutput      = acp.MethodTerminalOutput
	MethodTerminalWaitForExit = acp.MethodTerminalWaitForExit
	MethodTerminalKill        = acp.MethodTerminalKill
	MethodTerminalRelease     = acp.MethodTerminalRelease
)

type EnvVariable = acp.EnvVariable
type CreateRequest = acp.CreateTerminalRequest
type CreateResponse = acp.CreateTerminalResponse
type OutputRequest = acp.TerminalOutputRequest
type OutputResponse = acp.TerminalOutputResponse
type ExitStatus = acp.TerminalExitStatus
type WaitForExitRequest = acp.TerminalWaitForExitRequest
type WaitForExitResponse = acp.TerminalWaitForExitResponse
type KillRequest = acp.TerminalKillRequest
type ReleaseRequest = acp.TerminalReleaseRequest
type Provider = acp.TerminalProvider
type ClientCallbacks = acp.TerminalClientCallbacks

type CreateTerminalRequest = acp.CreateTerminalRequest
type CreateTerminalResponse = acp.CreateTerminalResponse
type TerminalOutputRequest = acp.TerminalOutputRequest
type TerminalOutputResponse = acp.TerminalOutputResponse
type TerminalExitStatus = acp.TerminalExitStatus
type TerminalWaitForExitRequest = acp.TerminalWaitForExitRequest
type TerminalWaitForExitResponse = acp.TerminalWaitForExitResponse
type TerminalKillRequest = acp.TerminalKillRequest
type TerminalReleaseRequest = acp.TerminalReleaseRequest
type TerminalProvider = acp.TerminalProvider
type TerminalClientCallbacks = acp.TerminalClientCallbacks
