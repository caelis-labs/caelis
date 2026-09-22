package httpclient

import (
	"context"
	"errors"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"io"
	"net/http"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
)

func (c *Client) ListBotWork(ctx context.Context, id string) ([]bot.Work, error) {
	id, err := remotePathID("bot", id)
	if err != nil {
		return nil, err
	}
	return doFocusedJSON[[]bot.Work](ctx, c, http.MethodGet, "/bots/"+id+"/work", nil)
}
func (c *Client) GetBotWork(ctx context.Context, id, workID string) (bot.Work, error) {
	id, err := remotePathID("bot", id)
	if err != nil {
		return bot.Work{}, err
	}
	workID, err = remotePathID("work", workID)
	if err != nil {
		return bot.Work{}, err
	}
	return doFocusedJSON[bot.Work](ctx, c, http.MethodGet, "/bots/"+id+"/work/"+workID, nil)
}
func (c *Client) GetBotRequest(ctx context.Context, id, operationID string) (bot.RequestSource, error) {
	id, err := remotePathID("bot", id)
	if err != nil {
		return bot.RequestSource{}, err
	}
	operationID, err = remotePathID("operation", operationID)
	if err != nil {
		return bot.RequestSource{}, err
	}
	response, err := c.do(ctx, http.MethodGet, "/bots/"+id+"/requests/"+operationID, nil, nil, nil)
	if err != nil {
		return bot.RequestSource{}, err
	}
	defer response.Body.Close()
	// Sources can contain the same bounded image parts as an accepted prompt.
	const limit = 64 << 20
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return bot.RequestSource{}, err
	}
	if len(raw) > limit {
		return bot.RequestSource{}, errors.New("control http client: Bot source exceeds prompt envelope limit")
	}
	var out bot.RequestSource
	err = wirev1.Unmarshal(raw, &out)
	return out, err
}
func (c *Client) botWorkCommand(ctx context.Context, action string, r appserver.BotWorkRequest) (appserver.CommandResult, error) {
	id, err := remotePathID("bot", r.BotID)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, "/bots/"+id+"/work/"+action, r.WriteBase, r)
}
func (c *Client) CreateBotWork(ctx context.Context, r appserver.BotWorkRequest) (appserver.CommandResult, error) {
	return c.botWorkCommand(ctx, "create", r)
}
func (c *Client) ContinueBotWork(ctx context.Context, r appserver.BotWorkRequest) (appserver.CommandResult, error) {
	return c.botWorkCommand(ctx, "continue", r)
}
func (c *Client) SteerBotWork(ctx context.Context, r appserver.BotWorkRequest) (appserver.CommandResult, error) {
	return c.botWorkCommand(ctx, "steer", r)
}
func (c *Client) CancelBotWork(ctx context.Context, r appserver.BotWorkRequest) (appserver.CommandResult, error) {
	return c.botWorkCommand(ctx, "cancel", r)
}

var _ appserver.BotWorkClient = (*Client)(nil)

func (c *Client) GetBotWorkOperation(ctx context.Context, id, operation string) (bot.WorkOperation, error) {
	id, err := remotePathID("bot", id)
	if err != nil {
		return bot.WorkOperation{}, err
	}
	operation, err = remotePathID("operation", operation)
	if err != nil {
		return bot.WorkOperation{}, err
	}
	return doFocusedJSON[bot.WorkOperation](ctx, c, http.MethodGet, "/bots/"+id+"/work-operations/"+operation, nil)
}
func (c *Client) ListBotCompletions(ctx context.Context, id string) ([]bot.Completion, error) {
	id, err := remotePathID("bot", id)
	if err != nil {
		return nil, err
	}
	return doFocusedJSON[[]bot.Completion](ctx, c, http.MethodGet, "/bots/"+id+"/completions", nil)
}
func (c *Client) AcknowledgeBotCompletion(ctx context.Context, r appserver.BotWorkRequest) (appserver.CommandResult, error) {
	return c.botWorkCommand(ctx, "acknowledge", r)
}
