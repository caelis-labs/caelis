package server

import "github.com/OnslaughtSnail/caelis/protocol/acp"

type Agent = acp.Agent
type PromptCallbacks = acp.PromptCallbacks
type SessionLoader = acp.SessionLoader
type PromptCapabilitiesProvider = acp.PromptCapabilitiesProvider
type CommandProvider = acp.CommandProvider

var ServeStdio = acp.ServeStdio
