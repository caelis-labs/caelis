package acp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

const (
	MethodSessionSteering = schema.MethodSessionSteering

	StopReasonEndTurn   = schema.StopReasonEndTurn
	StopReasonCancelled = schema.StopReasonCancelled
)

var ErrCapabilityUnsupported = errors.New("acp: capability unsupported")

type PromptCallbacks interface {
	SessionUpdate(context.Context, SessionNotification) error
	RequestPermission(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error)
}

type SessionLoader interface {
	LoadSession(context.Context, LoadSessionRequest, PromptCallbacks) (LoadSessionResponse, error)
}

type ModeProvider interface {
	SessionModes(context.Context, session.Session) (*SessionModeState, error)
	SetSessionMode(context.Context, SetSessionModeRequest) (SetSessionModeResponse, error)
}

type ConfigProvider interface {
	SessionConfigOptions(context.Context, session.Session) ([]SessionConfigOption, error)
	SetSessionConfigOption(context.Context, SetSessionConfigOptionRequest) (SetSessionConfigOptionResponse, error)
}

type CommandProvider interface {
	AvailableCommands(context.Context, string) ([]AvailableCommand, error)
}

type Implementation = schema.Implementation
type InitializeRequest = schema.InitializeRequest
type AgentCapabilities = schema.AgentCapabilities
type MCPCapabilities = schema.MCPCapabilities
type PromptCapabilities = schema.PromptCapabilities
type InitializeResponse = schema.InitializeResponse
type AuthenticateRequest = schema.AuthenticateRequest
type AuthenticateResponse = schema.AuthenticateResponse
type NewSessionRequest = schema.NewSessionRequest
type NewSessionResponse = schema.NewSessionResponse
type LoadSessionRequest = schema.LoadSessionRequest
type LoadSessionResponse = schema.LoadSessionResponse
type ResumeSessionRequest = schema.ResumeSessionRequest
type ResumeSessionResponse = schema.ResumeSessionResponse
type CloseSessionRequest = schema.CloseSessionRequest
type CloseSessionResponse = schema.CloseSessionResponse
type SessionMode = schema.SessionMode
type SessionModeState = schema.SessionModeState
type SetSessionModeRequest = schema.SetSessionModeRequest
type SetSessionModeResponse = schema.SetSessionModeResponse
type SessionConfigSelectOption = schema.SessionConfigSelectOption
type SessionConfigOption = schema.SessionConfigOption
type SetSessionConfigOptionRequest = schema.SetSessionConfigOptionRequest
type SetSessionConfigOptionResponse = schema.SetSessionConfigOptionResponse
type PromptRequest = schema.PromptRequest
type PromptResponse = schema.PromptResponse
type SessionSteeringOutcome = schema.SessionSteeringOutcome
type SessionSteeringCapability = schema.SessionSteeringCapability
type SessionSteeringOptions = schema.SessionSteeringOptions
type SessionSteeringRequest = schema.SessionSteeringRequest
type SessionSteeringResponse = schema.SessionSteeringResponse
type CancelNotification = schema.CancelNotification
type AvailableCommandInput = schema.AvailableCommandInput
type AvailableCommand = schema.AvailableCommand
type AvailableCommandsUpdate = schema.AvailableCommandsUpdate

const (
	SessionSteeringMetaKey = schema.SessionSteeringMetaKey

	SessionSteeringInjected       = schema.SessionSteeringInjected
	SessionSteeringPromptRequired = schema.SessionSteeringPromptRequired
	SessionSteeringFailed         = schema.SessionSteeringFailed

	SessionSteeringIdlePromptRequired = schema.SessionSteeringIdlePromptRequired
)

func DecodeSessionSteeringOptions(meta map[string]json.RawMessage) (SessionSteeringOptions, error) {
	return schema.DecodeSessionSteeringOptions(meta)
}
