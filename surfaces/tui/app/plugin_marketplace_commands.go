package tuiapp

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/control"
)

func slashPluginMarketplaceWithContext(ctx context.Context, service control.PluginService, send func(tea.Msg), args string) TaskResultMsg {
	action, rest, _ := controlprompt.ParseFirst(strings.TrimSpace(args))
	switch action {
	case "add":
		source := strings.TrimSpace(rest)
		if source == "" {
			sendNotice(send, "usage: /plugin marketplace add <source>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		marketplace, err := service.AddMarketplace(ctx, source)
		if err != nil {
			return TaskResultMsg{Err: controlprompt.FriendlyCommandError("plugin marketplace add", err)}
		}
		sendNotice(send, fmt.Sprintf("added marketplace %s successfully\n\n%s", marketplace.Name, formatMarketplaceDetail(marketplace)))
		return TaskResultMsg{SuppressTurnDivider: true}
	case "list":
		marketplaces, err := service.ListMarketplaces(ctx)
		if err != nil {
			return TaskResultMsg{Err: controlprompt.FriendlyCommandError("plugin marketplace list", err)}
		}
		if len(marketplaces) == 0 {
			sendNotice(send, "no plugin marketplaces\nnext: /plugin marketplace add <source>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		lines := make([]string, 0, len(marketplaces))
		for _, marketplace := range marketplaces {
			lines = append(lines, formatMarketplaceSummary(marketplace))
		}
		sendNotice(send, strings.Join(lines, "\n"))
		return TaskResultMsg{SuppressTurnDivider: true}
	case "update":
		name := strings.TrimSpace(rest)
		if name == "" {
			sendNotice(send, "usage: /plugin marketplace update <name>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		marketplace, err := service.UpdateMarketplace(ctx, name)
		if err != nil {
			return TaskResultMsg{Err: controlprompt.FriendlyCommandError("plugin marketplace update", err)}
		}
		sendNotice(send, fmt.Sprintf("updated marketplace %s successfully\n\n%s", marketplace.Name, formatMarketplaceDetail(marketplace)))
		return TaskResultMsg{SuppressTurnDivider: true}
	case "rm", "remove":
		name := strings.TrimSpace(rest)
		if name == "" {
			sendNotice(send, "usage: /plugin marketplace rm <name>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		if err := service.RemoveMarketplace(ctx, name); err != nil {
			return TaskResultMsg{Err: controlprompt.FriendlyCommandError("plugin marketplace rm", err)}
		}
		sendNotice(send, fmt.Sprintf("removed marketplace %s successfully", name))
		return TaskResultMsg{SuppressTurnDivider: true}
	default:
		sendNotice(send, "usage: /plugin marketplace add <source> | list | update <name> | rm <name>")
		return TaskResultMsg{SuppressTurnDivider: true}
	}
}

func formatMarketplaceSummary(m control.MarketplaceSnapshot) string {
	count := "0 plugins"
	if m.PluginCount == 1 {
		count = "1 plugin"
	} else if m.PluginCount > 1 {
		count = fmt.Sprintf("%d plugins", m.PluginCount)
	}
	if strings.TrimSpace(m.Description) != "" {
		return fmt.Sprintf("%s (%s) - %s", m.Name, count, m.Description)
	}
	return fmt.Sprintf("%s (%s)", m.Name, count)
}

func formatMarketplaceDetail(m control.MarketplaceSnapshot) string {
	lines := []string{fmt.Sprintf("marketplace info: %s", m.Name)}
	lines = append(lines, fmt.Sprintf("  Source:      %s", m.Source))
	lines = append(lines, fmt.Sprintf("  Root Path:   %s", m.Root))
	if m.Owner != "" {
		lines = append(lines, fmt.Sprintf("  Owner:       %s", m.Owner))
	}
	if m.Version != "" {
		lines = append(lines, fmt.Sprintf("  Version:     %s", m.Version))
	}
	if m.Description != "" {
		lines = append(lines, fmt.Sprintf("  Description: %s", m.Description))
	}
	if m.PluginRoot != "" {
		lines = append(lines, fmt.Sprintf("  Plugin Root: %s", m.PluginRoot))
	}
	if len(m.AllowCrossMarketplaceDependencies) > 0 {
		lines = append(lines, fmt.Sprintf("  Allows:      %s", strings.Join(m.AllowCrossMarketplaceDependencies, ", ")))
	}
	lines = append(lines, fmt.Sprintf("  Plugins:     %d", m.PluginCount))
	return strings.Join(lines, "\n")
}
