package acp

import (
	"context"
	"io"
	"strings"

	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
	"github.com/caelis-labs/caelis/surfaces/internal/promptview"
)

// DraftProductAgent is the experimental connection contract. Its semantic
// implementation receives only the same focused Control clients as v1.
type DraftProductAgent = runtimeacp.DraftProductAgent

// NewDraftFromClients constructs the pinned asynchronous product ACP facade.
func NewDraftFromClients(ctx context.Context, cfg ClientsConfig) (DraftProductAgent, error) {
	return runtimeacp.NewGatewayDraftAgent(ctx, runtimeacp.GatewayAgentConfig{
		Clients: cfg.Clients, SystemSessionClient: cfg.SystemSessionClient,
		AppName: cfg.AppName, UserID: cfg.UserID,
		WorkspaceKey: strings.TrimSpace(cfg.WorkspaceKey), WorkspaceCWD: strings.TrimSpace(cfg.WorkspaceCWD),
		SlashResultFormatter: promptview.FormatSlashResult,
	})
}

// ServeDraftProductStdio serves the complete asynchronous draft facade. Wire
// decoding and directions are owned by the v2 SDK, never the stable dispatcher.
func ServeDraftProductStdio(ctx context.Context, agent DraftProductAgent, in io.Reader, out io.Writer) error {
	return runtimeacp.ServeDraftProductStreams(ctx, agent, in, out)
}
