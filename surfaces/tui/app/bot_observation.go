package tuiapp

import "github.com/caelis-labs/caelis/control/bot"

// botConfigurationReader captures the selected identity on the update loop.
// The shared background status refresh owns scheduling and view-generation
// fencing, including recovery and model-only edits that append no user message.
func (m *Model) botConfigurationReader() func() *bot.Bot {
	active, ok := m.activeBot()
	if !ok || m.bot.client == nil {
		return nil
	}
	client := m.bot.client
	ctx := m.botFlowContext()
	return func() *bot.Bot {
		value, err := client.GetBot(ctx, active.ID)
		if err != nil || value.ID != active.ID || value.SessionID != active.SessionID {
			// A failed read leaves the last confirmed configuration in place;
			// a later ordinary status refresh can observe the Host again.
			return nil
		}
		return &value
	}
}

// applyBotConfiguration updates presentation only. An older read cannot roll
// back a local save or change which conversation the window presents.
func (m *Model) applyBotConfiguration(value bot.Bot) bool {
	active, ok := m.activeBot()
	if !ok || value.ID != active.ID || value.SessionID != active.SessionID || value.Revision < active.Revision {
		return false
	}
	m.bot.active = value
	m.bot.bots = upsertBot(m.bot.bots, value)
	return true
}
