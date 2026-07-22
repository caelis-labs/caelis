package tuiapp

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/control"
)

func slashPluginWithContext(ctx context.Context, service control.PluginService, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	sub, rest, _ := controlprompt.ParseFirst(strings.TrimSpace(args))
	switch sub {
	case "":
		sendNotice(send, pluginUsageText())
		return TaskResultMsg{SuppressTurnDivider: true}
	case "manage":
		plugins, err := service.ListPlugins(ctx)
		if err != nil {
			return TaskResultMsg{Err: controlprompt.FriendlyCommandError("plugin manage", err)}
		}
		if len(plugins) == 0 {
			sendNotice(send, "no installed plugins\nnext: /plugin install <plugin@marketplace|path>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		sendPluginManagerPrompt(ctx, service, send, plugins)
		return TaskResultMsg{SuppressTurnDivider: true}
	case "install":
		target := strings.TrimSpace(rest)
		if target == "" {
			sendNotice(send, "usage: /plugin install <plugin@marketplace|directory-path>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		p, err := service.InstallPlugin(ctx, target)
		if err != nil {
			return TaskResultMsg{Err: controlprompt.FriendlyCommandError("plugin install", err)}
		}
		sendNotice(send, fmt.Sprintf("installed plugin %s successfully\n\n%s", p.ID, formatPluginDetail(p)))
		return TaskResultMsg{SuppressTurnDivider: true}
	case "marketplace":
		return slashPluginMarketplaceWithContext(ctx, service, send, rest)
	case "rm":
		target := strings.TrimSpace(rest)
		if target == "" {
			sendNotice(send, "usage: /plugin rm <plugin-id>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		err := service.RemovePlugin(ctx, target)
		if err != nil {
			return TaskResultMsg{Err: controlprompt.FriendlyCommandError("plugin rm", err)}
		}
		sendNotice(send, fmt.Sprintf("removed plugin %s successfully", target))
		return TaskResultMsg{SuppressTurnDivider: true}
	default:
		sendNotice(send, pluginUsageText())
		return TaskResultMsg{SuppressTurnDivider: true}
	}
}

func sendPluginManagerPrompt(ctx context.Context, service control.PluginService, send func(tea.Msg), plugins []control.PluginSnapshot) {
	if send == nil || service == nil || len(plugins) == 0 {
		return
	}
	choices := make([]PromptChoice, 0, len(plugins))
	selected := make([]string, 0, len(plugins))
	for _, p := range plugins {
		id := strings.TrimSpace(p.ID)
		if id == "" {
			continue
		}
		status := p.Status
		if !p.Enabled {
			status = "disabled"
		}
		detail := strings.TrimSpace(strings.Join([]string{
			strings.TrimSpace(status),
			strings.TrimSpace(p.Name),
			strings.TrimSpace(p.Version),
		}, " "))
		choices = append(choices, PromptChoice{
			Label:  id,
			Value:  id,
			Detail: detail,
		})
		if p.Enabled {
			selected = append(selected, id)
		}
	}
	if len(choices) == 0 {
		return
	}
	responses := make(chan PromptResponse, 1)
	send(PromptRequestMsg{
		Title:               "Manage plugins",
		Prompt:              "Select enabled plugins",
		Choices:             choices,
		SelectedChoices:     selected,
		Filterable:          true,
		MultiSelect:         true,
		AllowEmptySelection: true,
		Response:            responses,
	})
	go awaitPluginManagerSelection(context.WithoutCancel(ctx), service, send, plugins, responses)
}

func awaitPluginManagerSelection(ctx context.Context, service control.PluginService, send func(tea.Msg), plugins []control.PluginSnapshot, responses <-chan PromptResponse) {
	ctx = contextOrBackground(ctx)
	var response PromptResponse
	select {
	case <-ctx.Done():
		return
	case next, ok := <-responses:
		if !ok {
			return
		}
		response = next
	}
	if response.Err != nil {
		return
	}
	selected := map[string]struct{}{}
	for _, id := range strings.Split(response.Line, ",") {
		id = strings.TrimSpace(id)
		if id != "" {
			selected[id] = struct{}{}
		}
	}
	var enabled, disabled []string
	for _, p := range plugins {
		id := strings.TrimSpace(p.ID)
		if id == "" {
			continue
		}
		_, wantEnabled := selected[id]
		switch {
		case wantEnabled && !p.Enabled:
			if _, err := service.EnablePlugin(ctx, id); err != nil {
				sendNotice(send, fmt.Sprintf("plugin manager failed enabling %s: %v", id, err))
				return
			}
			enabled = append(enabled, id)
		case !wantEnabled && p.Enabled:
			if _, err := service.DisablePlugin(ctx, id); err != nil {
				sendNotice(send, fmt.Sprintf("plugin manager failed disabling %s: %v", id, err))
				return
			}
			disabled = append(disabled, id)
		}
	}
	if len(enabled) == 0 && len(disabled) == 0 {
		sendNotice(send, "plugin selection unchanged")
		return
	}
	parts := make([]string, 0, 2)
	if len(enabled) > 0 {
		parts = append(parts, "enabled "+strings.Join(enabled, ", "))
	}
	if len(disabled) > 0 {
		parts = append(parts, "disabled "+strings.Join(disabled, ", "))
	}
	sendNotice(send, "plugin selection updated: "+strings.Join(parts, "; "))
}

func pluginUsageText() string {
	return "usage: /plugin install <plugin@marketplace|path> | marketplace add|list|update|rm | manage | rm <id>"
}

func formatPluginDetail(p control.PluginSnapshot) string {
	lines := []string{fmt.Sprintf("plugin info: %s", p.ID)}
	statusStr := p.Status
	if !p.Enabled {
		statusStr = "disabled"
	}
	lines = append(lines, fmt.Sprintf("  Name:        %s", p.Name))
	lines = append(lines, fmt.Sprintf("  Version:     %s", p.Version))
	lines = append(lines, fmt.Sprintf("  Status:      %s", statusStr))
	lines = append(lines, fmt.Sprintf("  Root Path:   %s", p.Root))
	if p.Description != "" {
		lines = append(lines, fmt.Sprintf("  Description: %s", p.Description))
	}
	if len(p.Skills) > 0 {
		lines = append(lines, fmt.Sprintf("  Skills:      %s", strings.Join(p.Skills, ", ")))
	}
	if len(p.Hooks) > 0 {
		lines = append(lines, fmt.Sprintf("  Hooks:       %s", strings.Join(p.Hooks, ", ")))
	}
	if len(p.Agents) > 0 {
		lines = append(lines, fmt.Sprintf("  Agents:      %s", strings.Join(p.Agents, ", ")))
	}
	if len(p.MCPServers) > 0 {
		lines = append(lines, "  MCP Servers:")
		for _, m := range p.MCPServers {
			mcpLine := fmt.Sprintf("    - %s (%s)", m.Name, m.Status)
			if len(m.Tools) > 0 {
				mcpLine += fmt.Sprintf(" [tools: %s]", strings.Join(m.Tools, ", "))
			}
			if m.Warning != "" {
				mcpLine += fmt.Sprintf(" (warning: %s)", m.Warning)
			}
			lines = append(lines, mcpLine)
		}
	}
	if p.Warning != "" {
		lines = append(lines, fmt.Sprintf("  Warning:     %s", p.Warning))
	}
	return strings.Join(lines, "\n")
}
