package tuiapp

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
)

func slashPluginOpenCodeWithContext(ctx context.Context, service control.Service, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	action, rest := splitFirst(strings.TrimSpace(args))
	switch action {
	case "discover":
		workspace := openCodeWorkspaceArg(service, rest)
		if workspace == "" {
			sendNotice(send, "usage: /plugin opencode discover [workspace]")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		discovery, err := service.DiscoverOpenCode(ctx, workspace)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("plugin opencode discover", err)}
		}
		sendNotice(send, formatOpenCodeDiscovery(workspace, discovery))
		return TaskResultMsg{SuppressTurnDivider: true}
	case "import":
		workspace := openCodeWorkspaceArg(service, rest)
		if workspace == "" {
			sendNotice(send, "usage: /plugin opencode import [workspace]")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		plugins, err := service.ImportOpenCode(ctx, workspace)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("plugin opencode import", err)}
		}
		sendNotice(send, formatOpenCodeImport(plugins))
		return TaskResultMsg{SuppressTurnDivider: true}
	default:
		sendNotice(send, "usage: /plugin opencode discover [workspace] | import [workspace]")
		return TaskResultMsg{SuppressTurnDivider: true}
	}
}

func openCodeWorkspaceArg(service control.Service, args string) string {
	if workspace := strings.TrimSpace(args); workspace != "" {
		return workspace
	}
	if service == nil {
		return ""
	}
	return strings.TrimSpace(service.WorkspaceDir())
}

func formatOpenCodeDiscovery(workspace string, discovery control.OpenCodeDiscoverySnapshot) string {
	lines := []string{fmt.Sprintf("opencode discovery: %s", workspace)}
	for _, warning := range discovery.Warnings {
		if text := strings.TrimSpace(warning); text != "" {
			lines = append(lines, "  Warning: "+text)
		}
	}
	lines = append(lines, fmt.Sprintf("  Local plugins: %d", len(discovery.LocalPlugins)))
	for _, source := range discovery.LocalPlugins {
		name := firstNonEmpty(strings.TrimSpace(source.Name), "-")
		path := firstNonEmpty(strings.TrimSpace(source.Path), "-")
		lines = append(lines, fmt.Sprintf("    %s  %s", name, path))
	}
	lines = append(lines, fmt.Sprintf("  npm packages:  %d", len(discovery.NPMPackages)))
	for _, pkg := range discovery.NPMPackages {
		name := firstNonEmpty(strings.TrimSpace(pkg.Package), "-")
		source := firstNonEmpty(strings.TrimSpace(pkg.Source), "-")
		lines = append(lines, fmt.Sprintf("    %s  %s", name, source))
	}
	return strings.Join(lines, "\n")
}

func formatOpenCodeImport(plugins []control.PluginSnapshot) string {
	if len(plugins) == 0 {
		return "no OpenCode plugins imported"
	}
	lines := []string{fmt.Sprintf("imported OpenCode entries: %d", len(plugins))}
	for _, plugin := range plugins {
		name := firstNonEmpty(strings.TrimSpace(plugin.ID), strings.TrimSpace(plugin.Name), "-")
		status := firstNonEmpty(strings.TrimSpace(plugin.Status), "unsupported")
		line := fmt.Sprintf("  %s  %s", name, status)
		if warning := strings.TrimSpace(plugin.Warning); warning != "" {
			line += " - " + warning
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
