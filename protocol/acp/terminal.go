package acp

import (
	"context"

	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

const (
	MethodTerminalCreate      = schema.MethodTerminalCreate
	MethodTerminalOutput      = schema.MethodTerminalOutput
	MethodTerminalWaitForExit = schema.MethodTerminalWaitForExit
	MethodTerminalKill        = schema.MethodTerminalKill
	MethodTerminalRelease     = schema.MethodTerminalRelease
)

type CreateTerminalRequest = schema.CreateTerminalRequest
type CreateTerminalResponse = schema.CreateTerminalResponse
type TerminalOutputRequest = schema.TerminalOutputRequest
type TerminalExitStatus = schema.TerminalExitStatus
type TerminalOutputResponse = schema.TerminalOutputResponse
type TerminalWaitForExitRequest = schema.TerminalWaitForExitRequest
type TerminalWaitForExitResponse = schema.TerminalWaitForExitResponse
type TerminalKillRequest = schema.TerminalKillRequest
type TerminalReleaseRequest = schema.TerminalReleaseRequest

type TerminalAdapter interface {
	Output(context.Context, TerminalOutputRequest) (TerminalOutputResponse, error)
	WaitForExit(context.Context, TerminalWaitForExitRequest) (TerminalWaitForExitResponse, error)
	Kill(context.Context, TerminalKillRequest) error
	Release(context.Context, TerminalReleaseRequest) error
}

type TerminalClientCallbacks interface {
	CreateTerminal(context.Context, CreateTerminalRequest) (CreateTerminalResponse, error)
	TerminalOutput(context.Context, TerminalOutputRequest) (TerminalOutputResponse, error)
	TerminalWaitForExit(context.Context, TerminalWaitForExitRequest) (TerminalWaitForExitResponse, error)
	TerminalKill(context.Context, TerminalKillRequest) error
	TerminalRelease(context.Context, TerminalReleaseRequest) error
}

func AsTerminalClientCallbacks(callbacks PromptCallbacks) (TerminalClientCallbacks, bool) {
	adapter, ok := callbacks.(TerminalClientCallbacks)
	return adapter, ok
}
