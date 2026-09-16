package httpclient

import (
	"context"
	"net/http"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
)

func (c *Client) ListBots(ctx context.Context) ([]bot.Bot, error) {
	return doFocusedJSON[[]bot.Bot](ctx, c, http.MethodGet, "/bots", nil)
}

func (c *Client) GetBot(ctx context.Context, botID string) (bot.Bot, error) {
	id, err := remotePathID("bot", botID)
	if err != nil {
		return bot.Bot{}, err
	}
	return doFocusedJSON[bot.Bot](ctx, c, http.MethodGet, "/bots/"+id, nil)
}

func (c *Client) CreateBot(ctx context.Context, req appserver.CreateBotRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/bots/create", req.WriteBase, req)
}

func (c *Client) UpdateBot(ctx context.Context, req appserver.UpdateBotRequest) (appserver.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/bots/update")
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}

var _ appserver.BotClient = (*Client)(nil)
