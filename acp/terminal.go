package acp

import (
	"context"

	schema "github.com/OnslaughtSnail/caelis/acp/schema"
)

const (
	MethodTerminalOutput      = schema.MethodTerminalOutput
	MethodTerminalWaitForExit = schema.MethodTerminalWaitForExit
	MethodTerminalKill        = schema.MethodTerminalKill
	MethodTerminalRelease     = schema.MethodTerminalRelease
)

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
