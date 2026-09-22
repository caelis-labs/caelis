package httpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
	"mime"
	"net/http"
)

func botClientPath(id, suffix string) (string, error) {
	id, err := remotePathID("bot", id)
	if err != nil {
		return "", err
	}
	return "/bots/" + id + suffix, nil
}
func (c *Client) RegisterBotClient(ctx context.Context, r appserver.RegisterBotClientRequest) (bot.ClientRegistration, error) {
	path, err := botClientPath(r.BotID, "/clients/register")
	if err != nil {
		return bot.ClientRegistration{}, err
	}
	response, err := c.do(ctx, http.MethodPost, path, nil, r, http.Header{"Idempotency-Key": {r.OperationID}})
	if err != nil {
		return bot.ClientRegistration{}, err
	}
	defer response.Body.Close()
	var out bot.ClientRegistration
	raw, err := readRemoteResponse(response)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}
func (c *Client) GetBotClient(ctx context.Context, id string) (bot.Client, error) {
	return botConnectionJSON[bot.Client](ctx, c, http.MethodGet, id, "/client", nil)
}
func (c *Client) ActivateBotClient(ctx context.Context, id string) (bot.Client, error) {
	return botConnectionJSON[bot.Client](ctx, c, http.MethodPost, id, "/client/activate", struct{}{})
}
func (c *Client) RenewBotClient(ctx context.Context, id string) (bot.Client, error) {
	return botConnectionJSON[bot.Client](ctx, c, http.MethodPost, id, "/client/renew", struct{}{})
}
func (c *Client) ExitBotClient(ctx context.Context, r appserver.BotClientExitRequest) (appserver.CommandResult, error) {
	path, err := botClientPath(r.BotID, "/client/exit")
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, r.WriteBase, r)
}
func (c *Client) FireBotReminder(ctx context.Context, r appserver.BotReminderRequest) (appserver.CommandResult, error) {
	path, err := botClientPath(r.BotID, "/client/reminders/fire")
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, r.WriteBase, r)
}
func (c *Client) BotDesktopSnapshot(ctx context.Context, id string) (bot.DesktopSnapshot, error) {
	return botConnectionJSON[bot.DesktopSnapshot](ctx, c, http.MethodGet, id, "/client/actions", nil)
}
func (c *Client) ListBotReminders(ctx context.Context, id string) ([]bot.ReminderGrant, error) {
	return botConnectionJSON[[]bot.ReminderGrant](ctx, c, http.MethodGet, id, "/client/reminders", nil)
}
func (c *Client) ListBotReminderOccurrences(ctx context.Context, id string) ([]bot.ReminderFire, error) {
	return botConnectionJSON[[]bot.ReminderFire](ctx, c, http.MethodGet, id, "/client/reminder-occurrences", nil)
}
func botActionJSON[T any](ctx context.Context, c *Client, method, id, action, suffix string, body any) (T, error) {
	var zero T
	action, err := remotePathID("action", action)
	if err != nil {
		return zero, err
	}
	return botConnectionJSON[T](ctx, c, method, id, "/client/actions/"+action+suffix, body)
}
func botConnectionJSON[T any](ctx context.Context, c *Client, method, id, suffix string, body any) (T, error) {
	var zero T
	path, err := botClientPath(id, suffix)
	if err != nil {
		return zero, err
	}
	return doFocusedJSON[T](ctx, c, method, path, body)
}
func (c *Client) GetBotDesktopCall(ctx context.Context, id, action string) (bot.DesktopCall, error) {
	return botActionJSON[bot.DesktopCall](ctx, c, http.MethodGet, id, action, "", nil)
}
func (c *Client) ClaimBotDesktopCall(ctx context.Context, id, action string) (bot.DesktopClaim, error) {
	return botActionJSON[bot.DesktopClaim](ctx, c, http.MethodPost, id, action, "/claim", struct{}{})
}
func (c *Client) CompleteBotDesktopCall(ctx context.Context, id, action string, r bot.DesktopReceipt) (bot.DesktopCall, error) {
	return botActionJSON[bot.DesktopCall](ctx, c, http.MethodPost, id, action, "/result", r)
}
func (c *Client) WatchBotDesktop(ctx context.Context, id, cursor string, apply func(bot.DesktopSnapshot) error) error {
	if apply == nil {
		return errors.New("control http client: desktop snapshot consumer required")
	}
	path, err := botClientPath(id, "/client/actions/events")
	if err != nil {
		return err
	}
	response, err := c.do(ctx, http.MethodGet, path, nil, nil, http.Header{"Last-Event-ID": {cursor}})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "text/event-stream" {
		return errors.New("control http client: desktop observation requires SSE")
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), c.maxEventBytes)
	for {
		frame, err := readRemoteSSEFrame(scanner)
		if err != nil {
			return err
		}
		if frame.event != "bot.desktop.snapshot" {
			continue
		}
		var out bot.DesktopSnapshot
		if err := json.Unmarshal(frame.data, &out); err != nil {
			return err
		}
		if out.Cursor == "" || out.Cursor != frame.id {
			return errors.New("control http client: desktop cursor mismatch")
		}
		if err := apply(out); err != nil {
			return err
		}
	}
}

var _ appserver.BotDesktopClient = (*Client)(nil)
