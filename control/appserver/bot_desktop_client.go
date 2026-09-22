package appserver

import (
	"context"
	"github.com/caelis-labs/caelis/control/bot"
)

// BotDesktopClient is the native connection boundary. Its credentials and
// dispatch claims must never be handed to an Agent or managed work Session.
// WatchBotDesktop blocks until cancellation, EOF, or a callback error. Callers
// reconnect with the last applied cursor and reconcile before claiming actions.
type BotDesktopClient interface {
	RegisterBotClient(context.Context, RegisterBotClientRequest) (bot.ClientRegistration, error)
	GetBotClient(context.Context, string) (bot.Client, error)
	ActivateBotClient(context.Context, string) (bot.Client, error)
	RenewBotClient(context.Context, string) (bot.Client, error)
	ExitBotClient(context.Context, BotClientExitRequest) (CommandResult, error)
	BotDesktopSnapshot(context.Context, string) (bot.DesktopSnapshot, error)
	GetBotDesktopCall(context.Context, string, string) (bot.DesktopCall, error)
	ClaimBotDesktopCall(context.Context, string, string) (bot.DesktopClaim, error)
	CompleteBotDesktopCall(context.Context, string, string, bot.DesktopReceipt) (bot.DesktopCall, error)
	WatchBotDesktop(context.Context, string, string, func(bot.DesktopSnapshot) error) error
	ListBotReminders(context.Context, string) ([]bot.ReminderGrant, error)
	ListBotReminderOccurrences(context.Context, string) ([]bot.ReminderFire, error)
	FireBotReminder(context.Context, BotReminderRequest) (CommandResult, error)
}
