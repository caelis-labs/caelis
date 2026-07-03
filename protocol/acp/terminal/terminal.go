package terminal

import "github.com/caelis-labs/caelis/protocol/acp"

const (
	MethodTerminalCreate      = acp.MethodTerminalCreate
	MethodTerminalOutput      = acp.MethodTerminalOutput
	MethodTerminalWaitForExit = acp.MethodTerminalWaitForExit
	MethodTerminalKill        = acp.MethodTerminalKill
	MethodTerminalRelease     = acp.MethodTerminalRelease
)

type CreateTerminalRequest = acp.CreateTerminalRequest
type CreateTerminalResponse = acp.CreateTerminalResponse
type TerminalOutputRequest = acp.TerminalOutputRequest
type TerminalExitStatus = acp.TerminalExitStatus
type TerminalOutputResponse = acp.TerminalOutputResponse
type TerminalWaitForExitRequest = acp.TerminalWaitForExitRequest
type TerminalWaitForExitResponse = acp.TerminalWaitForExitResponse
type TerminalKillRequest = acp.TerminalKillRequest
type TerminalReleaseRequest = acp.TerminalReleaseRequest
type TerminalAdapter = acp.TerminalAdapter
type TerminalClientCallbacks = acp.TerminalClientCallbacks

var AsTerminalClientCallbacks = acp.AsTerminalClientCallbacks
