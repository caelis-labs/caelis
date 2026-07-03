package controlcommand

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/control"
)

type AgentAddArgs struct {
	Target  string
	Install bool
	Custom  *control.CustomAgentConfig
}

func ParseAgentAddArgs(args string) (AgentAddArgs, bool) {
	fields := strings.Fields(args)
	var out AgentAddArgs
	if len(fields) > 0 && strings.EqualFold(fields[0], "custom") {
		if len(fields) < 4 {
			return AgentAddArgs{}, false
		}
		name := strings.TrimSpace(fields[1])
		if name == "" || strings.HasPrefix(name, "-") {
			return AgentAddArgs{}, false
		}
		delim := -1
		for i := 2; i < len(fields); i++ {
			if fields[i] == "--" {
				delim = i
				break
			}
		}
		if delim < 0 || delim+1 >= len(fields) {
			return AgentAddArgs{}, false
		}
		command := strings.TrimSpace(fields[delim+1])
		if command == "" {
			return AgentAddArgs{}, false
		}
		return AgentAddArgs{
			Target: name,
			Custom: &control.CustomAgentConfig{
				Name:    name,
				Command: command,
				Args:    append([]string(nil), fields[delim+2:]...),
			},
		}, true
	}
	for _, field := range fields {
		switch strings.ToLower(strings.TrimSpace(field)) {
		case "--install", "-i":
			out.Install = true
		default:
			if strings.HasPrefix(field, "-") {
				return AgentAddArgs{}, false
			}
			if out.Target != "" {
				return AgentAddArgs{}, false
			}
			out.Target = strings.TrimSpace(field)
		}
	}
	return out, true
}

func ParseConnectArgs(args string) control.ConnectConfig {
	parts := strings.Fields(args)
	cfg := control.ConnectConfig{}
	if len(parts) >= 1 {
		cfg.Provider = parts[0]
	}
	if len(parts) >= 2 {
		cfg.Model = parts[1]
	}
	if len(parts) >= 3 {
		cfg.BaseURL = dashAsEmpty(parts[2])
	}
	if len(parts) >= 4 {
		if timeout, err := strconv.Atoi(dashAsEmpty(parts[3])); err == nil {
			cfg.TimeoutSeconds = timeout
		}
	}
	if len(parts) >= 5 {
		secret := dashAsEmpty(parts[4])
		if strings.HasPrefix(strings.ToLower(secret), "env:") {
			cfg.TokenEnv = strings.TrimSpace(secret[len("env:"):])
		} else if strings.HasPrefix(secret, "$") {
			cfg.TokenEnv = strings.TrimSpace(strings.TrimPrefix(secret, "$"))
		} else {
			cfg.APIKey = secret
		}
	}
	if len(parts) >= 6 {
		if contextWindow, err := strconv.Atoi(dashAsEmpty(parts[5])); err == nil {
			cfg.ContextWindowTokens = contextWindow
		}
	}
	if len(parts) >= 7 {
		if maxOutput, err := strconv.Atoi(dashAsEmpty(parts[6])); err == nil {
			cfg.MaxOutputTokens = maxOutput
		}
	}
	if len(parts) >= 8 {
		cfg.ReasoningLevels = parseReasoningLevels(parts[7])
	}
	if len(parts) >= 9 {
		if timeout, err := strconv.Atoi(dashAsEmpty(parts[8])); err == nil {
			cfg.StreamFirstEventTimeoutSeconds = timeout
		}
	}
	if len(parts) == 4 && cfg.TimeoutSeconds == 0 && cfg.APIKey == "" && cfg.TokenEnv == "" {
		cfg.TokenEnv = dashAsEmpty(parts[3])
	}
	return cfg
}

func AgentHelpText() string {
	lines := []string{
		"/agent commands:",
		"  /agent list          list registered ACP agents and current controller",
		"  /agent add NAME      register a built-in ACP agent",
		"  /agent add custom NAME -- COMMAND [ARGS...]",
		"  /agent use NAME      switch the main controller to a registered ACP agent",
		"  /agent use local     return the main controller to the local kernel",
		"  /agent remove NAME   unregister an ACP agent",
	}
	return strings.Join(lines, "\n")
}

func FormatAgentCatalog(agents []control.AgentCandidate) string {
	if len(agents) == 0 {
		return "no ACP agents are registered\nnext: run /agent add <builtin>"
	}
	lines := []string{"registered ACP agents:"}
	for _, agent := range agents {
		line := "  " + strings.TrimSpace(agent.Name)
		if desc := strings.TrimSpace(agent.Description); desc != "" {
			line += "  " + desc
		}
		lines = append(lines, line)
	}
	lines = append(lines, "next: use /<agent> <prompt> for a child subagent, or /agent use <agent> to switch the main controller")
	return strings.Join(lines, "\n")
}

func FormatAgentList(agents []control.AgentCandidate, status control.AgentStatusSnapshot) string {
	lines := []string{"agent registry:"}
	lines = append(lines, fmt.Sprintf("  controller: %s", firstNonEmpty(strings.TrimSpace(status.ControllerLabel), strings.TrimSpace(status.ControllerKind), "local")))
	if len(agents) == 0 {
		lines = append(lines, "  registered: none")
		lines = append(lines, "next: run /agent add <builtin>")
		return strings.Join(lines, "\n")
	}
	lines = append(lines, "  registered:")
	for _, agent := range agents {
		line := "    " + strings.TrimSpace(agent.Name)
		if desc := strings.TrimSpace(agent.Description); desc != "" {
			line += "  " + desc
		}
		lines = append(lines, line)
	}
	lines = append(lines, "next: /<agent> <prompt> starts a child; /agent use <agent> switches the main controller")
	return strings.Join(lines, "\n")
}

func FormatAgentReadyNotice(agent string) string {
	agent = strings.TrimSpace(agent)
	command := agentSlashCommand(agent)
	return fmt.Sprintf("%s is ready\nNext: %s <prompt> starts a side ACP task. /agent use %s makes it the main controller.", agent, command, agent)
}

func FormatAgentRemovedNotice(agent string) string {
	return fmt.Sprintf("%s removed\nNext: /agent list shows remaining agents.", strings.TrimSpace(agent))
}

func FormatAgentUseNotice(target string, status control.AgentStatusSnapshot) string {
	target = strings.TrimSpace(target)
	if IsLocalAgentTarget(target) {
		return "local control restored\nNext: /agent use <agent> switches the main controller."
	}
	agent := firstNonEmpty(strings.TrimSpace(status.ControllerLabel), target)
	return fmt.Sprintf("%s is now the main controller\nNext: type a prompt normally. /agent use local returns to local control.", agent)
}

func FormatAgentStatusSnapshot(status control.AgentStatusSnapshot) string {
	controller := firstNonEmpty(strings.TrimSpace(status.ControllerLabel), strings.TrimSpace(status.ControllerKind), "local kernel")
	kind := firstNonEmpty(strings.TrimSpace(status.ControllerKind), "kernel")
	state := "idle"
	if status.HasActiveTurn {
		state = "running"
	}
	lines := []string{"Agent Controller"}
	lines = append(lines, fmt.Sprintf("  Active    %s", controller))
	lines = append(lines, fmt.Sprintf("  Kind      %s", kind))
	lines = append(lines, fmt.Sprintf("  Session   %s", firstNonEmpty(strings.TrimSpace(status.SessionID), "-")))
	lines = append(lines, fmt.Sprintf("  State     %s", state))
	if model := FormatAgentControllerModel(status); model != "" {
		lines = append(lines, fmt.Sprintf("  Model     %s", model))
	}
	participants := displayableAgentParticipants(status.Participants)
	if len(participants) == 0 {
		lines = append(lines, "  Side agents  none")
	} else {
		lines = append(lines, "  Side agents")
		for _, participant := range participants {
			lines = append(lines, fmt.Sprintf("    %s  %s  %s", firstNonEmpty(strings.TrimSpace(participant.ID), "-"), agentParticipantDisplayLabel(participant), strings.TrimSpace(participant.Role)))
		}
	}
	delegated := displayableAgentParticipants(status.DelegatedParticipants)
	if len(delegated) == 0 {
		lines = append(lines, "  Delegated tasks  none")
	} else {
		lines = append(lines, "  Delegated tasks")
		for _, participant := range delegated {
			lines = append(lines, fmt.Sprintf("    %s  %s  %s", firstNonEmpty(strings.TrimSpace(participant.ID), "-"), agentParticipantDisplayLabel(participant), strings.TrimSpace(participant.AgentName)))
		}
	}
	if len(status.AvailableAgents) == 0 {
		lines = append(lines, "note: No ACP agents are configured")
	} else if len(participants) == 0 && strings.TrimSpace(status.ControllerKind) == "" {
		lines = append(lines, "note: Run /agent add <builtin> to register an ACP agent")
	} else if len(participants) == 0 {
		lines = append(lines, fmt.Sprintf("note: Run %s <prompt> to start a child subagent", agentPromptCommand(status)))
	}
	return strings.Join(lines, "\n")
}

func FormatAgentControllerModel(status control.AgentStatusSnapshot) string {
	model := strings.TrimSpace(status.ControllerModel)
	effort := strings.TrimSpace(status.ControllerReasoningEffort)
	if model == "" {
		return ""
	}
	if effort == "" || strings.EqualFold(effort, "none") || strings.Contains(model, "[") {
		return model
	}
	return model + " [" + effort + "]"
}

func ModeToggleHint(status control.StatusSnapshot) string {
	label := firstNonEmpty(strings.TrimSpace(status.Session.ModeLabel), strings.TrimSpace(status.Session.SessionMode), "auto-review")
	switch strings.ToLower(strings.TrimSpace(status.Session.SessionMode)) {
	case "manual":
		return "manual approval mode enabled"
	case "auto-review":
		return "auto-review approval mode enabled"
	default:
		return label + " mode enabled"
	}
}

func FriendlyCommandError(action string, err error) error {
	if err == nil {
		return nil
	}
	raw := strings.TrimSpace(err.Error())
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "api key is missing"):
		return fmt.Errorf("%s: API key is missing. Use /connect and paste a key, or enter env:YOUR_API_KEY", action)
	case strings.Contains(lower, "base url is invalid"):
		return fmt.Errorf("%s: base URL is invalid. Use a full URL such as https://api.openai.com/v1", action)
	case strings.Contains(lower, "provider is not supported"), strings.Contains(lower, "unknown provider"):
		return fmt.Errorf("%s: provider is not supported. Run /connect and choose one of the listed providers", action)
	case strings.Contains(lower, "provider and model are required"), strings.Contains(lower, "model is required"):
		return fmt.Errorf("%s: provider or model is not configured. Run /connect to add one", action)
	case strings.Contains(lower, "unknown model alias"):
		return fmt.Errorf("%s: model alias was not found. Run /model and choose a configured alias, or use /connect first", action)
	case strings.Contains(lower, "ambiguous model alias"):
		return fmt.Errorf("%s: model alias is ambiguous. Type more of the alias or pick from /model", action)
	case strings.Contains(lower, "agent name is required"), strings.Contains(lower, "agent ") && (strings.Contains(lower, " is not configured") || strings.Contains(lower, " not found")):
		return fmt.Errorf("%s: agent was not found. Run /agent add <builtin> first, then /agent list to inspect registered agents", action)
	case strings.Contains(lower, "agent ") && strings.Contains(lower, " is ambiguous"):
		return fmt.Errorf("%s: agent name is ambiguous. Type more of the agent name or run /agent list", action)
	case strings.Contains(lower, "subagent handle") && strings.Contains(lower, "not found"):
		return fmt.Errorf("%s: handle was not found. Use @handle only for side subagents created by /<agent>", action)
	case strings.Contains(lower, "participant id is required"), strings.Contains(lower, "participant ") && strings.Contains(lower, " is not attached"):
		return fmt.Errorf("%s: participant was not found", action)
	case strings.Contains(lower, "participant ") && strings.Contains(lower, " is ambiguous"):
		return fmt.Errorf("%s: participant target is ambiguous", action)
	case strings.Contains(lower, "control plane is not available"), strings.Contains(lower, "acp controller backend is not configured"):
		return fmt.Errorf("%s: ACP control plane is not configured for this stack. Check app assembly agent config before using /agent", action)
	case strings.Contains(lower, "unknown sandbox backend"), strings.Contains(lower, "unsupported by"):
		return fmt.Errorf("%s: sandbox backend is unavailable on this machine. Run /status to inspect sandbox readiness", action)
	case strings.Contains(lower, "uac prompt was cancelled"):
		return fmt.Errorf("%s: Windows sandbox repair was cancelled", action)
	case strings.Contains(lower, "acl: write") && strings.Contains(lower, "dacl"):
		return fmt.Errorf("%s: Windows sandbox cannot repair workspace ACLs with the current token. Run /doctor", action)
	case strings.Contains(lower, "windows sandbox setup is required"):
		return fmt.Errorf("%s: Windows sandbox repair is pending. Retry the command to let the sandbox repair workspace ACL state lazily", action)
	case strings.Contains(lower, "session not found"):
		return fmt.Errorf("%s: session could not be loaded. Run /resume to inspect available sessions", action)
	case strings.Contains(lower, "active turn"):
		return fmt.Errorf("%s: another turn is still running. Wait for it to finish or interrupt it before reconfiguring", action)
	default:
		return fmt.Errorf("%s: %w", action, err)
	}
}

func IsLocalAgentTarget(target string) bool {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "local", "kernel", "main":
		return true
	default:
		return false
	}
}

func dashAsEmpty(value string) string {
	value = strings.TrimSpace(value)
	if value == "-" {
		return ""
	}
	return value
}

func parseReasoningLevels(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "-" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.ToLower(strings.TrimSpace(part)); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func agentSlashCommand(agent string) string {
	agent = strings.TrimSpace(strings.TrimPrefix(agent, "/"))
	if agent == "" || strings.ContainsAny(agent, " \t") {
		return "/<agent>"
	}
	return "/" + agent
}

func agentPromptCommand(status control.AgentStatusSnapshot) string {
	controller := strings.TrimSpace(firstNonEmpty(status.ControllerLabel, status.ControllerKind))
	controller = strings.TrimPrefix(controller, "/")
	if controller == "" || strings.EqualFold(controller, "kernel") || strings.EqualFold(controller, "local kernel") {
		return "/<agent>"
	}
	if strings.ContainsAny(controller, " \t") {
		return "/<agent>"
	}
	return "/" + controller
}

func agentParticipantDisplayLabel(participant control.AgentParticipantSnapshot) string {
	label := strings.TrimSpace(participant.Label)
	agent := strings.TrimSpace(participant.AgentName)
	if label == "" {
		if agent != "" {
			return agent
		}
		return "-"
	}
	if agent == "" {
		return label
	}
	return label + "(" + agent + ")"
}

func displayableAgentParticipants(participants []control.AgentParticipantSnapshot) []control.AgentParticipantSnapshot {
	if len(participants) == 0 {
		return nil
	}
	return append([]control.AgentParticipantSnapshot(nil), participants...)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
