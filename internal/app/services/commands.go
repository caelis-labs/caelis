package services

import (
	"context"

	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

type CommandCatalogRequest struct{}

func (s CommandService) Available(context.Context, CommandCatalogRequest) (appviewmodel.CommandCatalogView, error) {
	return appviewmodel.CommandCatalogView{
		Commands: []appviewmodel.CommandView{
			{Name: "agent", Description: "Manage ACP agents", InputHint: "use|add|install|list|remove"},
			{Name: "connect", Description: "Configure a model provider", InputHint: "provider model [base-url] [timeout] [token] [context] [max-output] [reasoning-levels]"},
			{Name: "model", Description: "Switch or inspect models", InputHint: "use <alias> [reasoning]"},
			{Name: "approval", Description: "Switch approval mode", InputHint: "auto-review|manual"},
			{Name: "status", Description: "Show current runtime status"},
			{Name: "resume", Description: "Resume a previous session", InputHint: "session id"},
			{Name: "compact", Description: "Compact the current conversation"},
		},
	}, nil
}
