package server

import "github.com/OnslaughtSnail/caelis/protocol/acp"

type Agent = acp.Agent
type PromptCallbacks = acp.PromptCallbacks
type SessionLoader = acp.SessionLoader
type ModeProvider = acp.ModeProvider
type ConfigProvider = acp.ConfigProvider
type ModelProvider = acp.ModelProvider
type PromptCapabilitiesProvider = acp.PromptCapabilitiesProvider
type CommandProvider = acp.CommandProvider

var ServeStdio = acp.ServeStdio
