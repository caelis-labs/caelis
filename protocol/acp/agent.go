package acp

import (
	"context"
	"errors"

	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
	"github.com/OnslaughtSnail/caelis/session"
)

const (
	JSONRPCVersion         = schema.JSONRPCVersion
	CurrentProtocolVersion = schema.CurrentProtocolVersion

	MethodInitialize       = schema.MethodInitialize
	MethodAuthenticate     = schema.MethodAuthenticate
	MethodSessionNew       = schema.MethodSessionNew
	MethodSessionLoad      = schema.MethodSessionLoad
	MethodSessionResume    = schema.MethodSessionResume
	MethodSessionClose     = schema.MethodSessionClose
	MethodSessionSetMode   = schema.MethodSessionSetMode
	MethodSessionSetConfig = schema.MethodSessionSetConfig
	MethodSessionSetModel  = schema.MethodSessionSetModel
	MethodSessionPrompt    = schema.MethodSessionPrompt
	MethodSessionCancel    = schema.MethodSessionCancel

	StopReasonEndTurn   = schema.StopReasonEndTurn
	StopReasonCancelled = schema.StopReasonCancelled
)

var ErrCapabilityUnsupported = errors.New("acp: capability unsupported")

type Agent interface {
	Initialize(context.Context, InitializeRequest) (InitializeResponse, error)
	Authenticate(context.Context, AuthenticateRequest) (AuthenticateResponse, error)
	NewSession(context.Context, NewSessionRequest) (NewSessionResponse, error)
	Prompt(context.Context, PromptRequest, PromptCallbacks) (PromptResponse, error)
	Cancel(context.Context, CancelNotification) error
}

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

type ModelProvider interface {
	SessionModels(context.Context, session.Session) (*SessionModelState, error)
	SetSessionModel(context.Context, SetSessionModelRequest) (SetSessionModelResponse, error)
}

type PromptCapabilitiesProvider interface {
	PromptCapabilities(context.Context) (PromptCapabilities, error)
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
type ModelInfo = schema.ModelInfo
type SessionModelState = schema.SessionModelState
type SetSessionModelRequest = schema.SetSessionModelRequest
type SetSessionModelResponse = schema.SetSessionModelResponse
type SessionConfigSelectOption = schema.SessionConfigSelectOption
type SessionConfigOption = schema.SessionConfigOption
type SetSessionConfigOptionRequest = schema.SetSessionConfigOptionRequest
type SetSessionConfigOptionResponse = schema.SetSessionConfigOptionResponse
type PromptRequest = schema.PromptRequest
type PromptResponse = schema.PromptResponse
type CancelNotification = schema.CancelNotification
type AvailableCommandInput = schema.AvailableCommandInput
type AvailableCommand = schema.AvailableCommand
type AvailableCommandsUpdate = schema.AvailableCommandsUpdate
