package services

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

type CommandCatalogRequest struct{}

type CommandExecutionRequest struct {
	SessionRef   session.Ref         `json:"session_ref,omitempty"`
	Input        string              `json:"input,omitempty"`
	ContentParts []model.ContentPart `json:"content_parts,omitempty"`
}

func (s CommandService) Available(ctx context.Context, _ CommandCatalogRequest) (appviewmodel.CommandCatalogView, error) {
	commands := []appviewmodel.CommandView{
		{Name: "agent", Description: "Manage ACP agents", InputHint: "list|use|add|install|update|remove", ArgCandidates: commandAgentArgCandidates()},
		{Name: "connect", Description: "Configure a model provider", InputHint: "provider model [base-url] [timeout] [token] [context] [max-output] [reasoning-levels]"},
		{Name: "controller", Description: "Show or configure the active ACP controller", InputHint: "[set <option-id> <value>]"},
		{Name: "model", Description: "Switch or inspect models", InputHint: "use <alias> [reasoning]|del <alias>", ArgCandidates: commandModelArgCandidates()},
		{Name: "approval", Description: "Inspect or switch approval mode", InputHint: "[auto-review|manual|toggle]", ArgCandidates: commandApprovalArgCandidates()},
		{Name: "status", Description: "Show current runtime status"},
		{Name: "settings", Description: "Show shared settings and diagnostics panel", InputHint: "[set <field-id> <value>|run <action-id> [confirm]]", ArgCandidates: commandSettingsArgCandidates()},
		{Name: "doctor", Description: "Diagnose model, session store, resources, and sandbox readiness", InputHint: "[fix]", ArgCandidates: commandDoctorArgCandidates()},
		{Name: "task", Description: "Inspect and control live or durable tasks", InputHint: "list|tail|wait|write|cancel|release|start", ArgCandidates: commandTaskArgCandidates()},
		{Name: "new", Description: "Start a fresh session"},
		{Name: "resume", Description: "Resume a previous session", InputHint: "[session id]"},
		{Name: "compact", Description: "Compact the current conversation"},
	}
	seen := map[string]struct{}{}
	for _, command := range commands {
		if name := strings.ToLower(strings.TrimSpace(command.Name)); name != "" {
			seen[name] = struct{}{}
		}
	}
	agents, err := s.services.Agents().List(ctx)
	if err != nil {
		return appviewmodel.CommandCatalogView{}, err
	}
	for _, agent := range agents {
		agent = normalizeAgentDescriptor(agent)
		if agent.Kind != AgentKindExternalACP {
			continue
		}
		name := strings.ToLower(firstNonEmpty(agent.Name, agent.ID))
		if name == "" || reservedSlashCommandName(name) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		description := firstNonEmpty(agent.Description, "Invoke ACP agent "+name)
		commands = append(commands, appviewmodel.CommandView{
			Name:        name,
			Description: description,
			InputHint:   "prompt",
		})
	}
	return appviewmodel.CommandCatalogView{Commands: commands}, nil
}

func commandAgentArgCandidates() []appviewmodel.CommandArgCandidate {
	return []appviewmodel.CommandArgCandidate{
		{Value: "use", Display: "use", Detail: "Switch the main controller"},
		{Value: "add", Display: "add", Detail: "Register a built-in ACP agent"},
		{Value: "install", Display: "install", Detail: "Install and register an external ACP adapter"},
		{Value: "update", Display: "update", Detail: "Update and register an external ACP adapter"},
		{Value: "list", Display: "list", Detail: "List registered ACP agents"},
		{Value: "remove", Display: "remove", Detail: "Unregister an ACP agent"},
	}
}

func commandModelArgCandidates() []appviewmodel.CommandArgCandidate {
	return []appviewmodel.CommandArgCandidate{
		{Value: "use", Display: "use", Detail: "Switch current model alias"},
		{Value: "del", Display: "del", Detail: "Delete stored model alias"},
	}
}

func commandApprovalArgCandidates() []appviewmodel.CommandArgCandidate {
	return []appviewmodel.CommandArgCandidate{
		{Value: "auto-review", Display: "auto-review", Detail: "Use automatic AI approval review"},
		{Value: "manual", Display: "manual", Detail: "Prompt before sensitive requests"},
	}
}

func commandSettingsArgCandidates() []appviewmodel.CommandArgCandidate {
	return []appviewmodel.CommandArgCandidate{
		{Value: "set", Display: "set", Detail: "Edit a settings field"},
		{Value: "run", Display: "run", Detail: "Run a settings panel action"},
	}
}

func commandTaskArgCandidates() []appviewmodel.CommandArgCandidate {
	return []appviewmodel.CommandArgCandidate{
		{Value: "list", Display: "list", Detail: "List live and durable tasks"},
		{Value: "tail", Display: "tail", Detail: "Read task output"},
		{Value: "wait", Display: "wait", Detail: "Wait briefly and read output"},
		{Value: "write", Display: "write", Detail: "Send input to a task"},
		{Value: "cancel", Display: "cancel", Detail: "Cancel a running task"},
		{Value: "release", Display: "release", Detail: "Close a completed task handle"},
		{Value: "start", Display: "start", Detail: "Start a sandbox task"},
	}
}

func commandDoctorArgCandidates() []appviewmodel.CommandArgCandidate {
	return []appviewmodel.CommandArgCandidate{
		{Value: "fix", Display: "fix", Detail: "Repair Windows sandbox ACLs"},
	}
}

func (s CommandService) Execute(ctx context.Context, req CommandExecutionRequest) (appviewmodel.CommandExecutionView, error) {
	command, args, ok := parseSlashCommand(req.Input)
	if !ok {
		return appviewmodel.CommandExecutionView{}, nil
	}
	switch command {
	case "agent":
		return s.executeAgent(ctx, req.SessionRef, args)
	case "approval":
		return s.executeApproval(ctx, req.SessionRef, args)
	case "connect":
		return s.executeConnect(ctx, req.SessionRef, args)
	case "controller":
		return s.executeController(ctx, req.SessionRef, args)
	case "doctor":
		return s.executeDoctor(ctx, req.SessionRef, args)
	case "model":
		return s.executeModel(ctx, req.SessionRef, args)
	case "new":
		return s.executeNew(ctx, args)
	case "resume":
		return s.executeResume(ctx, args)
	case "settings":
		return s.executeSettings(ctx, req.SessionRef, args)
	case "task":
		return s.executeTask(ctx, req.SessionRef, args)
	case "status":
		if strings.TrimSpace(args) != "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /status")
		}
		status, err := s.services.Status().View(ctx, StatusRequest{SessionRef: req.SessionRef})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return appviewmodel.CommandExecutionView{
			Handled: true,
			Command: command,
			Output:  formatCommandStatus(status),
			Status:  &status,
		}, nil
	case "compact":
		if strings.TrimSpace(args) != "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /compact")
		}
		if _, err := s.services.Compaction().Compact(ctx, CompactSessionRequest{
			SessionRef: req.SessionRef,
			Trigger:    "manual",
		}); err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return appviewmodel.CommandExecutionView{
			Handled: true,
			Command: command,
			Output:  "compaction completed",
		}, nil
	default:
		return s.executeAgentPrompt(ctx, req, command, args)
	}
}

func (s CommandService) executeNew(ctx context.Context, args string) (appviewmodel.CommandExecutionView, error) {
	if strings.TrimSpace(args) != "" {
		return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /new")
	}
	runtimeCfg := s.services.Runtime()
	active, err := s.services.Sessions().Start(ctx, StartSessionRequest{
		Workspace: session.Workspace{
			Key: runtimeCfg.WorkspaceKey,
			CWD: runtimeCfg.WorkspaceCWD,
		},
	})
	if err != nil {
		return appviewmodel.CommandExecutionView{}, err
	}
	ref := active.Ref
	return appviewmodel.CommandExecutionView{
		Handled:    true,
		Command:    "new",
		Output:     "new session: " + strings.TrimSpace(ref.SessionID),
		SessionRef: &ref,
	}, nil
}

func (s CommandService) executeAgentPrompt(ctx context.Context, req CommandExecutionRequest, command string, args string) (appviewmodel.CommandExecutionView, error) {
	agent, ok, err := s.lookupCommandAgent(ctx, command)
	if err != nil || !ok {
		return appviewmodel.CommandExecutionView{}, err
	}
	prompt := strings.TrimSpace(args)
	parts := commandAgentContentParts(prompt, req.ContentParts)
	if prompt == "" && len(parts) == 0 {
		return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /%s <prompt>", command)
	}
	snapshot, err := s.services.Sessions().Load(ctx, req.SessionRef)
	if err != nil {
		return appviewmodel.CommandExecutionView{}, err
	}
	participant := commandAgentParticipant(snapshot, agent, command)
	result, err := s.services.Agents().Invoke(ctx, AgentInvokeRequest{
		AgentID:      agent.ID,
		SessionRef:   snapshot.Session.Ref,
		Participant:  participant,
		Input:        prompt,
		ContentParts: parts,
		DeferRecord:  true,
	})
	if err != nil {
		return appviewmodel.CommandExecutionView{}, err
	}
	preface := []session.Event{commandParticipantEvent(snapshot.Session.Ref.SessionID, participant, "attached", "slash_"+command)}
	if event := commandAgentUserEvent(snapshot.Session.Ref.SessionID, participant, prompt, parts, "slash_"+command); event.Type != "" {
		preface = append(preface, event)
	}
	events := make([]session.Event, 0, len(preface)+len(result.Events))
	events = append(events, preface...)
	events = append(events, result.Events...)
	if len(events) > 0 {
		if _, err := s.services.Engine().RecordEvents(ctx, snapshot.Session.Ref, events); err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
	}
	return appviewmodel.CommandExecutionView{
		Handled: true,
		Command: command,
		Events:  events,
	}, nil
}

func (s CommandService) lookupCommandAgent(ctx context.Context, command string) (AgentDescriptor, bool, error) {
	command = strings.ToLower(strings.TrimSpace(command))
	if command == "" || reservedSlashCommandName(command) {
		return AgentDescriptor{}, false, nil
	}
	agents, err := s.services.Agents().List(ctx)
	if err != nil {
		return AgentDescriptor{}, false, err
	}
	for _, agent := range agents {
		agent = normalizeAgentDescriptor(agent)
		if agent.Kind != AgentKindExternalACP {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(agent.ID), command) || strings.EqualFold(strings.TrimSpace(agent.Name), command) {
			if strings.TrimSpace(agent.ID) == "" {
				agent.ID = firstNonEmpty(agent.Name, agent.Command)
			}
			return agent, strings.TrimSpace(agent.ID) != "", nil
		}
	}
	return AgentDescriptor{}, false, nil
}

func (s CommandService) executeAgent(ctx context.Context, ref session.Ref, args string) (appviewmodel.CommandExecutionView, error) {
	sub, rest, hasSub := splitCommandArg(args)
	if !hasSub || strings.EqualFold(sub, "list") || strings.EqualFold(sub, "ls") || strings.EqualFold(sub, "status") {
		if strings.TrimSpace(rest) != "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /agent [list]")
		}
		view, err := s.services.Agents().Management(ctx)
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return appviewmodel.CommandExecutionView{
			Handled:         true,
			Command:         "agent",
			Output:          s.formatCommandAgents(ctx, ref, view),
			AgentManagement: &view,
		}, nil
	}
	switch strings.ToLower(sub) {
	case "use":
		target := strings.TrimSpace(rest)
		if target == "" || strings.ContainsAny(target, " \t\n") {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /agent use <agent|local>")
		}
		result, err := s.services.Controllers().Handoff(ctx, ControllerHandoffRequest{
			SessionRef: ref,
			Target:     target,
			Source:     "app_command_agent",
			Reason:     "slash command handoff",
		})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		output := "agent controller: local"
		if result.ActiveACP {
			output = "agent controller: " + firstNonEmpty(result.Status.Agent, result.Controller.AgentName, result.Controller.ID)
		}
		event := controllerHandoffEvent(result.Controller, "app_command_agent", "slash command handoff")
		event.SessionID = strings.TrimSpace(ref.SessionID)
		return s.agentCommandView(ctx, ref, output, []session.Event{event})
	case "add", "register":
		agent, err := s.executeAgentAdd(ctx, rest)
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return s.agentCommandView(ctx, ref, "agent registered: "+firstNonEmpty(agent.Name, agent.ID), nil)
	case "install", "update":
		target := strings.TrimSpace(rest)
		if target == "" || strings.ContainsAny(target, " \t\n") {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /agent %s <builtin>", strings.ToLower(sub))
		}
		agent, err := s.services.Agents().RegisterBuiltinWithOptions(ctx, target, RegisterBuiltinAgentOptions{Install: true})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		action := "installed"
		if strings.EqualFold(sub, "update") {
			action = "updated"
		}
		return s.agentCommandView(ctx, ref, "agent "+action+": "+firstNonEmpty(agent.Name, agent.ID), nil)
	case "remove", "rm", "delete", "del":
		target := strings.TrimSpace(rest)
		if target == "" || strings.ContainsAny(target, " \t\n") {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /agent remove <agent>")
		}
		if status, ok, err := s.services.Controllers().Status(ctx, ref); err != nil {
			return appviewmodel.CommandExecutionView{}, err
		} else if ok && strings.EqualFold(strings.TrimSpace(status.Agent), target) {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: agent %q is the active controller; run /agent use local before removing it", target)
		}
		if err := s.services.Agents().Remove(ctx, target); err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return s.agentCommandView(ctx, ref, "agent removed: "+target, nil)
	default:
		return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /agent list|use <agent|local>|add <builtin>|add custom <name> -- <command> [args...]|install <builtin>|update <builtin>|remove <agent>")
	}
}

func (s CommandService) agentCommandView(ctx context.Context, ref session.Ref, output string, events []session.Event) (appviewmodel.CommandExecutionView, error) {
	view, err := s.services.Agents().Management(ctx)
	if err != nil {
		return appviewmodel.CommandExecutionView{}, err
	}
	return appviewmodel.CommandExecutionView{
		Handled:         true,
		Command:         "agent",
		Output:          output,
		Events:          events,
		AgentManagement: &view,
	}, nil
}

func (s CommandService) executeAgentAdd(ctx context.Context, args string) (AgentDescriptor, error) {
	sub, rest, hasSub := splitCommandArg(args)
	if !hasSub {
		return AgentDescriptor{}, fmt.Errorf("app/services: usage: /agent add <builtin>|custom <name> -- <command> [args...]")
	}
	if strings.EqualFold(sub, "custom") {
		agent, err := parseCommandCustomAgent(rest)
		if err != nil {
			return AgentDescriptor{}, err
		}
		return s.services.Agents().RegisterCustom(ctx, agent)
	}
	if strings.TrimSpace(rest) != "" {
		return AgentDescriptor{}, fmt.Errorf("app/services: usage: /agent add <builtin>")
	}
	return s.services.Agents().RegisterBuiltin(ctx, sub)
}

func parseCommandCustomAgent(args string) (AgentDescriptor, error) {
	fields := strings.Fields(args)
	if len(fields) < 3 {
		return AgentDescriptor{}, fmt.Errorf("app/services: usage: /agent add custom <name> -- <command> [args...]")
	}
	sep := -1
	for i, field := range fields {
		if field == "--" {
			sep = i
			break
		}
	}
	if sep != 1 || sep+1 >= len(fields) {
		return AgentDescriptor{}, fmt.Errorf("app/services: usage: /agent add custom <name> -- <command> [args...]")
	}
	return AgentDescriptor{
		Name:    strings.TrimSpace(fields[0]),
		Command: strings.TrimSpace(fields[sep+1]),
		Args:    append([]string(nil), fields[sep+2:]...),
	}, nil
}

func (s CommandService) executeConnect(ctx context.Context, ref session.Ref, args string) (appviewmodel.CommandExecutionView, error) {
	if strings.TrimSpace(args) == "" {
		panel, err := s.services.Models().ConnectPanel(ctx, ModelConnectRequest{SessionRef: ref})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return appviewmodel.CommandExecutionView{
			Handled:           true,
			Command:           "connect",
			Output:            formatCommandConnectPanel(panel),
			ModelConnectPanel: &panel,
		}, nil
	}
	cfg, err := s.commandConnectConfig(args)
	if err != nil {
		return appviewmodel.CommandExecutionView{}, err
	}
	prepared, err := s.services.Models().PrepareConnectConfig(ctx, cfg)
	if err != nil {
		return appviewmodel.CommandExecutionView{}, err
	}
	connected, err := s.services.Models().Connect(ctx, prepared)
	if err != nil {
		return appviewmodel.CommandExecutionView{}, err
	}
	if strings.TrimSpace(ref.SessionID) != "" {
		if _, err := s.services.Models().Use(ctx, ref, connected.ID, connected.DefaultReasoningEffort); err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
	}
	output := "connected: " + formatModelConfigName(connected)
	if connected.BaseURL != "" {
		output += "\n  base_url: " + connected.BaseURL
	}
	if connected.ContextWindowTokens > 0 {
		output += fmt.Sprintf("\n  context_window_tokens: %d", connected.ContextWindowTokens)
	}
	if connected.MaxOutputTokens > 0 {
		output += fmt.Sprintf("\n  max_output_tokens: %d", connected.MaxOutputTokens)
	}
	if len(connected.ReasoningLevels) > 0 {
		output += "\n  reasoning_levels: " + strings.Join(connected.ReasoningLevels, ",")
	}
	return appviewmodel.CommandExecutionView{
		Handled: true,
		Command: "connect",
		Output:  output,
	}, nil
}

func (s CommandService) commandConnectConfig(args string) (appsettings.ModelConfig, error) {
	fields := strings.Fields(args)
	if len(fields) < 2 {
		return appsettings.ModelConfig{}, fmt.Errorf("app/services: usage: /connect provider model [base-url] [timeout] [token] [context] [max-output] [reasoning-levels]")
	}
	if len(fields) > 8 {
		return appsettings.ModelConfig{}, fmt.Errorf("app/services: usage: /connect provider model [base-url] [timeout] [token] [context] [max-output] [reasoning-levels]")
	}
	cfg := appsettings.ModelConfig{
		Provider: strings.TrimSpace(fields[0]),
		Model:    strings.TrimSpace(fields[1]),
	}
	if len(fields) >= 3 {
		cfg.BaseURL = dashAsEmpty(fields[2])
	}
	timeoutSeconds := 0
	if len(fields) >= 4 {
		raw := dashAsEmpty(fields[3])
		if raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value <= 0 {
				if len(fields) == 4 {
					applyCommandConnectToken(&cfg, raw)
					raw = ""
				} else {
					return appsettings.ModelConfig{}, fmt.Errorf("app/services: connect timeout must be a positive integer")
				}
			}
			if raw != "" {
				timeoutSeconds = value
			}
		}
	}
	if len(fields) >= 5 {
		applyCommandConnectToken(&cfg, dashAsEmpty(fields[4]))
	}
	if len(fields) >= 6 {
		value, err := parseCommandPositiveInt(fields[5], "context window")
		if err != nil {
			return appsettings.ModelConfig{}, err
		}
		cfg.ContextWindowTokens = value
	}
	if len(fields) >= 7 {
		value, err := parseCommandPositiveInt(fields[6], "max output")
		if err != nil {
			return appsettings.ModelConfig{}, err
		}
		cfg.MaxOutputTokens = value
	}
	if len(fields) >= 8 {
		cfg.ReasoningLevels = parseCommandReasoningLevels(fields[7])
	}
	if timeoutSeconds > 0 {
		cfg.Timeout = time.Duration(timeoutSeconds) * time.Second
	}
	return s.commandConnectConfigWithDefaults(cfg), nil
}

func (s CommandService) commandConnectConfigWithDefaults(cfg appsettings.ModelConfig) appsettings.ModelConfig {
	caps, ok := s.services.Models().LookupCapabilities(cfg.Provider, cfg.Model)
	if !ok {
		caps = s.services.Models().DefaultCapabilities()
	}
	if cfg.ContextWindowTokens <= 0 {
		cfg.ContextWindowTokens = caps.ContextWindowTokens
	}
	if cfg.MaxOutputTokens <= 0 {
		cfg.MaxOutputTokens = caps.DefaultMaxOutputTokens
		if cfg.MaxOutputTokens <= 0 {
			cfg.MaxOutputTokens = caps.MaxOutputTokens
		}
	}
	if cfg.DefaultReasoningEffort == "" {
		cfg.DefaultReasoningEffort = strings.ToLower(strings.TrimSpace(caps.DefaultReasoningEffort))
	}
	if cfg.ReasoningMode == "" {
		cfg.ReasoningMode = strings.ToLower(strings.TrimSpace(caps.ReasoningMode))
	}
	if len(cfg.ReasoningLevels) == 0 {
		cfg.ReasoningLevels = reasoningLevelsFromCapabilities(caps)
	}
	return cfg
}

func applyCommandConnectToken(cfg *appsettings.ModelConfig, raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	switch {
	case strings.HasPrefix(strings.ToLower(raw), "env:"):
		cfg.TokenEnv = strings.TrimSpace(raw[len("env:"):])
	case strings.HasPrefix(raw, "$"):
		cfg.TokenEnv = strings.TrimSpace(strings.TrimPrefix(raw, "$"))
	default:
		cfg.Token = raw
		cfg.PersistToken = true
	}
}

func (s CommandService) executeApproval(ctx context.Context, ref session.Ref, args string) (appviewmodel.CommandExecutionView, error) {
	mode := strings.TrimSpace(args)
	if status, active, err := s.services.Controllers().Status(ctx, ref); err != nil {
		return appviewmodel.CommandExecutionView{}, err
	} else if active {
		if mode == "" {
			return s.approvalCommandView(ctx, ref, "approval mode: "+firstNonEmpty(status.Mode, "auto-review"))
		}
		fields := strings.Fields(mode)
		if len(fields) != 1 {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /approval [mode]")
		}
		if isApprovalToggleArg(fields[0]) {
			next, err := s.services.Controllers().CycleMode(ctx, ref)
			if err != nil {
				return appviewmodel.CommandExecutionView{}, err
			}
			return s.approvalCommandView(ctx, ref, "approval mode: "+firstNonEmpty(next.Mode, fields[0]))
		}
		next, err := s.services.Controllers().SetMode(ctx, ref, fields[0])
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return s.approvalCommandView(ctx, ref, "approval mode: "+firstNonEmpty(next.Mode, fields[0]))
	}
	if mode == "" {
		current, err := s.services.Modes().Current(ctx, ref)
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return s.approvalCommandView(ctx, ref, "approval mode: "+firstNonEmpty(current.ID, "auto-review"))
	}
	fields := strings.Fields(mode)
	if len(fields) != 1 {
		return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /approval [auto-review|manual|toggle]")
	}
	if isApprovalToggleArg(fields[0]) {
		next, err := s.services.Modes().Toggle(ctx, ref)
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return s.approvalCommandView(ctx, ref, "approval mode: "+next.ID)
	}
	next, err := s.services.Modes().Set(ctx, ref, fields[0])
	if err != nil {
		return appviewmodel.CommandExecutionView{}, err
	}
	return s.approvalCommandView(ctx, ref, "approval mode: "+next.ID)
}

func (s CommandService) approvalCommandView(ctx context.Context, ref session.Ref, output string) (appviewmodel.CommandExecutionView, error) {
	panel, err := s.services.Approvals().Panel(ctx, ApprovalPanelRequest{SessionRef: ref})
	if err != nil {
		return appviewmodel.CommandExecutionView{}, err
	}
	return appviewmodel.CommandExecutionView{
		Handled:       true,
		Command:       "approval",
		Output:        output,
		ApprovalPanel: &panel,
	}, nil
}

func isApprovalToggleArg(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "toggle", "cycle", "next":
		return true
	default:
		return false
	}
}

func (s CommandService) executeModel(ctx context.Context, ref session.Ref, args string) (appviewmodel.CommandExecutionView, error) {
	sub, rest, hasSub := splitCommandArg(args)
	if status, active, err := s.services.Controllers().Status(ctx, ref); err != nil {
		return appviewmodel.CommandExecutionView{}, err
	} else if active {
		return s.executeControllerModel(ctx, ref, status, sub, rest, hasSub)
	}
	if !hasSub || strings.EqualFold(sub, "list") || strings.EqualFold(sub, "ls") {
		if strings.TrimSpace(rest) != "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /model [list]")
		}
		choices, err := s.services.Models().List(ctx)
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		currentID := ""
		if len(choices) > 0 {
			if current, ok, err := s.services.Models().Current(ctx, ref); err != nil {
				return appviewmodel.CommandExecutionView{}, err
			} else if ok {
				currentID = current.ID
			}
		}
		return s.modelCommandView(ctx, ref, formatCommandModels(choices, currentID))
	}
	switch strings.ToLower(sub) {
	case "use":
		modelRef, reasoning := parseCommandModelUse(rest)
		if modelRef == "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /model use <alias> [reasoning]")
		}
		cfg, err := s.services.Models().Use(ctx, ref, modelRef, reasoning)
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		output := "model switched to: " + formatModelConfigName(cfg)
		if reasoning != "" {
			output += " (reasoning: " + reasoning + ")"
		}
		return s.modelCommandView(ctx, ref, output)
	case "del", "delete", "rm":
		modelRef := strings.TrimSpace(rest)
		if modelRef == "" || strings.ContainsAny(modelRef, " \t\n") {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /model del <alias>")
		}
		deleted, resolveErr := s.services.Models().Resolve(ctx, modelRef)
		if err := s.services.Models().Delete(ctx, modelRef); err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		if resolveErr == nil {
			if err := s.clearDeletedSessionModel(ctx, ref, deleted); err != nil {
				return appviewmodel.CommandExecutionView{}, err
			}
		}
		return s.modelCommandView(ctx, ref, "model deleted: "+modelRef)
	default:
		return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /model [list|use <alias> [reasoning]|del <alias>]")
	}
}

func (s CommandService) modelCommandView(ctx context.Context, ref session.Ref, output string) (appviewmodel.CommandExecutionView, error) {
	panel, err := s.services.Models().Selection(ctx, ModelSelectionRequest{SessionRef: ref})
	if err != nil {
		return appviewmodel.CommandExecutionView{}, err
	}
	return appviewmodel.CommandExecutionView{
		Handled:        true,
		Command:        "model",
		Output:         output,
		ModelSelection: &panel,
	}, nil
}

func (s CommandService) executeControllerModel(ctx context.Context, ref session.Ref, status ControllerStatus, sub string, rest string, hasSub bool) (appviewmodel.CommandExecutionView, error) {
	if !hasSub || strings.EqualFold(sub, "list") || strings.EqualFold(sub, "ls") {
		if strings.TrimSpace(rest) != "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /model [list]")
		}
		return appviewmodel.CommandExecutionView{
			Handled: true,
			Command: "model",
			Output:  formatCommandControllerModels(status),
		}, nil
	}
	switch strings.ToLower(sub) {
	case "use":
		modelRef, reasoning := parseCommandModelUse(rest)
		if modelRef == "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /model use <model> [reasoning]")
		}
		next, err := s.services.Controllers().SetModel(ctx, ref, modelRef, reasoning)
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		output := "model switched to: " + firstNonEmpty(next.Model, modelRef)
		if effort := firstNonEmpty(next.ReasoningEffort, reasoning); effort != "" {
			output += " (reasoning: " + effort + ")"
		}
		return appviewmodel.CommandExecutionView{
			Handled: true,
			Command: "model",
			Output:  output,
		}, nil
	case "del", "delete", "rm":
		return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /model use <model> [reasoning]")
	default:
		return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /model [list|use <model> [reasoning]]")
	}
}

func (s CommandService) clearDeletedSessionModel(ctx context.Context, ref session.Ref, deleted appsettings.ModelConfig) error {
	if s.services.engine == nil || strings.TrimSpace(ref.SessionID) == "" || strings.TrimSpace(deleted.ID) == "" {
		return nil
	}
	ref = defaultSessionRef(s.services.Runtime(), ref)
	snapshot, err := s.services.engine.LoadSession(ctx, ref)
	if err != nil {
		return nil
	}
	currentID := strings.TrimSpace(stateString(snapshot.State, StateCurrentModelID))
	if currentID == "" || !strings.EqualFold(currentID, strings.TrimSpace(deleted.ID)) {
		return nil
	}
	return s.services.Models().ClearSession(ctx, ref)
}

func (s CommandService) executeResume(ctx context.Context, args string) (appviewmodel.CommandExecutionView, error) {
	sessionID := strings.TrimSpace(args)
	if sessionID == "" {
		req := ListSessionsRequest{Limit: 10}
		page, err := s.services.Sessions().List(ctx, req)
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		panel := resumePanelFromPage(page, s.services.Sessions().workspaceWithDefaults(req.Workspace), req.Search)
		return appviewmodel.CommandExecutionView{
			Handled:     true,
			Command:     "resume",
			Output:      formatCommandSessions(page),
			ResumePanel: &panel,
		}, nil
	}
	if strings.ContainsAny(sessionID, " \t\n") {
		return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /resume [session id]")
	}
	snapshot, err := s.services.Sessions().Load(ctx, session.Ref{SessionID: sessionID})
	if err != nil {
		return appviewmodel.CommandExecutionView{}, err
	}
	ref := snapshot.Session.Ref
	return appviewmodel.CommandExecutionView{
		Handled:    true,
		Command:    "resume",
		Output:     formatCommandResume(snapshot),
		SessionRef: &ref,
	}, nil
}

func resumePanelFromPage(page session.SessionPage, workspace session.Workspace, search string) appviewmodel.ResumePanelView {
	panel := appviewmodel.ResumePanelView{
		Workspace:  workspace,
		Search:     strings.TrimSpace(search),
		Count:      len(page.Sessions),
		NextCursor: page.NextCursor,
		Sessions:   make([]appviewmodel.ResumeSessionItem, 0, len(page.Sessions)),
	}
	for _, summary := range page.Sessions {
		item := resumeSessionItem(summary)
		if strings.TrimSpace(item.SessionID) == "" {
			continue
		}
		panel.Sessions = append(panel.Sessions, item)
	}
	panel.Count = len(panel.Sessions)
	return panel
}

func resumeSessionItem(summary session.SessionSummary) appviewmodel.ResumeSessionItem {
	active := session.CloneSession(summary.Session)
	sessionID := strings.TrimSpace(active.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(active.Ref.SessionID)
	}
	updatedAt := active.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = summary.LastEventAt
	}
	workspace := strings.TrimSpace(active.Workspace.CWD)
	if workspace == "" {
		workspace = strings.TrimSpace(active.Workspace.Key)
	}
	command := "/resume " + sessionID
	return appviewmodel.ResumeSessionItem{
		Ref:         session.NormalizeRef(active.Ref),
		SessionID:   sessionID,
		Title:       strings.TrimSpace(active.Title),
		Workspace:   workspace,
		EventCount:  summary.EventCount,
		UpdatedAt:   updatedAt,
		LastEventAt: summary.LastEventAt,
		Command:     command,
		Actions: []appviewmodel.ResumeSessionAction{{
			ID:        "resume.open:" + sessionID,
			Kind:      "open",
			Label:     "Resume",
			Command:   command,
			SessionID: sessionID,
			Enabled:   true,
		}},
	}
}

func parseSlashCommand(input string) (string, string, bool) {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "//") {
		return "", "", false
	}
	trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "/"))
	if trimmed == "" {
		return "", "", false
	}
	command, args, _ := strings.Cut(trimmed, " ")
	command = strings.ToLower(strings.TrimSpace(command))
	if command == "" {
		return "", "", false
	}
	return command, strings.TrimSpace(args), true
}

func commandAgentContentParts(prompt string, in []model.ContentPart) []model.ContentPart {
	prompt = strings.TrimSpace(prompt)
	out := make([]model.ContentPart, 0, len(in)+1)
	if prompt != "" {
		out = append(out, model.ContentPart{Type: model.ContentPartText, Text: prompt})
	}
	for _, part := range model.CloneContentParts(in) {
		if part.Type == model.ContentPartText {
			continue
		}
		out = append(out, part)
	}
	return out
}

func commandAgentParticipant(snapshot session.Snapshot, agent AgentDescriptor, command string) session.ParticipantBinding {
	agent = normalizeAgentDescriptor(agent)
	base := firstNonEmpty(agent.ID, agent.Name, command)
	label := "@" + base
	return session.ParticipantBinding{
		ID:         allocateCommandParticipantID(snapshot, base),
		Kind:       session.ParticipantACP,
		Role:       session.ParticipantSidecar,
		AgentName:  base,
		Label:      label,
		Source:     "app_command_agent",
		AttachedAt: time.Now().UTC(),
	}
}

func allocateCommandParticipantID(snapshot session.Snapshot, base string) string {
	base = strings.ToLower(firstNonEmpty(base, "agent"))
	used := map[string]struct{}{}
	add := func(id string) {
		id = strings.ToLower(strings.TrimSpace(id))
		if id != "" {
			used[id] = struct{}{}
		}
	}
	for _, participant := range snapshot.Session.Participants {
		add(participant.ID)
	}
	for _, event := range snapshot.Events {
		if event.Scope != nil {
			add(event.Scope.Participant.ID)
		}
	}
	if _, ok := used[base]; !ok {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if _, ok := used[candidate]; !ok {
			return candidate
		}
	}
}

func commandParticipantEvent(sessionID string, participant session.ParticipantBinding, action string, source string) session.Event {
	return session.Event{
		Type:       session.EventParticipant,
		SessionID:  strings.TrimSpace(sessionID),
		Visibility: session.VisibilityCanonical,
		Time:       time.Now().UTC(),
		Actor:      session.ActorRef{Kind: session.ActorSystem, ID: "caelis", Name: "caelis"},
		Scope: &session.EventScope{
			Source:      firstNonEmpty(source, "app_command_agent"),
			Participant: participant,
		},
		Meta: map[string]any{"action": strings.TrimSpace(action)},
	}
}

func commandAgentUserEvent(sessionID string, participant session.ParticipantBinding, prompt string, contentParts []model.ContentPart, source string) session.Event {
	parts := commandMessageParts(prompt, contentParts)
	if len(parts) == 0 {
		return session.Event{}
	}
	return session.Event{
		Type:       session.EventUser,
		SessionID:  strings.TrimSpace(sessionID),
		Visibility: session.VisibilityCanonical,
		Time:       time.Now().UTC(),
		Actor:      session.ActorRef{Kind: session.ActorUser, ID: "user", Name: "user"},
		Scope: &session.EventScope{
			Source:      firstNonEmpty(source, "app_command_agent"),
			Participant: participant,
		},
		Message: &model.Message{
			Role:  model.RoleUser,
			Parts: parts,
		},
		Meta: map[string]any{
			"agent":  strings.TrimSpace(participant.AgentName),
			"handle": strings.TrimSpace(participant.Label),
		},
	}
}

func commandMessageParts(prompt string, contentParts []model.ContentPart) []model.Part {
	out := make([]model.Part, 0, len(contentParts)+1)
	for _, part := range contentParts {
		switch part.Type {
		case model.ContentPartText:
			if text := strings.TrimSpace(part.Text); text != "" {
				out = append(out, model.NewTextPart(text))
			}
		case model.ContentPartImage:
			source := model.MediaSource{Kind: model.MediaInline, Data: part.Data}
			if strings.TrimSpace(part.URI) != "" {
				source = model.MediaSource{Kind: model.MediaURL, URI: strings.TrimSpace(part.URI)}
			} else if strings.TrimSpace(part.Data) == "" && strings.TrimSpace(part.FileName) != "" {
				source = model.MediaSource{Kind: model.MediaLocalRef, LocalRef: strings.TrimSpace(part.FileName)}
			}
			if source.Data == "" && source.URI == "" && source.LocalRef == "" {
				continue
			}
			out = append(out, model.Part{Kind: model.PartMedia, Media: &model.MediaPart{
				Modality: model.MediaImage,
				Source:   source,
				MimeType: strings.TrimSpace(part.MimeType),
				Name:     strings.TrimSpace(part.FileName),
			}})
		case model.ContentPartFile:
			ref := model.FileRefPart{
				Name:     strings.TrimSpace(part.FileName),
				MimeType: strings.TrimSpace(part.MimeType),
				URI:      strings.TrimSpace(part.URI),
			}
			if ref.URI == "" {
				ref.LocalRef = strings.TrimSpace(part.FileName)
			}
			if ref.Name == "" && ref.URI == "" && ref.LocalRef == "" {
				continue
			}
			out = append(out, model.Part{Kind: model.PartFileRef, FileRef: &ref})
		}
	}
	if len(out) == 0 {
		if prompt = strings.TrimSpace(prompt); prompt != "" {
			out = append(out, model.NewTextPart(prompt))
		}
	}
	return out
}

func splitCommandArg(input string) (string, string, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", "", false
	}
	head, tail, ok := strings.Cut(trimmed, " ")
	if !ok {
		return strings.TrimSpace(head), "", true
	}
	return strings.TrimSpace(head), strings.TrimSpace(tail), true
}

func parseCommandModelUse(args string) (string, string) {
	modelRef, reasoning, _ := strings.Cut(strings.TrimSpace(args), " ")
	return strings.TrimSpace(modelRef), strings.ToLower(strings.TrimSpace(reasoning))
}

func parseCommandPositiveInt(raw string, label string) (int, error) {
	raw = dashAsEmpty(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("app/services: connect %s must be a positive integer", label)
	}
	return value, nil
}

func parseCommandReasoningLevels(raw string) []string {
	raw = dashAsEmpty(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	return appsettings.DedupeStrings(parts)
}

func dashAsEmpty(value string) string {
	value = strings.TrimSpace(value)
	if value == "-" {
		return ""
	}
	return value
}

func (s CommandService) formatCommandAgents(ctx context.Context, ref session.Ref, view appviewmodel.AgentManagementView) string {
	lines := []string{"agents:"}
	if status, ok, err := s.services.Controllers().Status(ctx, ref); err == nil && ok {
		lines = append(lines, "  controller: "+firstNonEmpty(status.Agent, "acp"))
	} else {
		lines = append(lines, "  controller: local")
	}
	if len(view.Registered) == 0 {
		lines = append(lines, "  registered: none")
	} else {
		lines = append(lines, "  registered:")
		for _, item := range view.Registered {
			lines = append(lines, "    "+formatCommandAgentItem(item.Agent))
		}
	}
	if len(view.Builtins) > 0 {
		lines = append(lines, "  builtins:")
		for _, item := range view.Builtins {
			line := "    " + formatCommandAgentItem(item.Agent)
			if item.Registered {
				line += " (registered)"
			}
			if item.Installable {
				line += " (installable)"
			}
			lines = append(lines, line)
		}
	}
	if len(view.Installable) > 0 {
		lines = append(lines, "  installable:")
		for _, item := range view.Installable {
			line := "    " + firstNonEmpty(item.Name, item.ID)
			if detail := strings.TrimSpace(item.Detail); detail != "" {
				line += "  " + detail
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func formatCommandAgentItem(agent appviewmodel.AgentItem) string {
	name := firstNonEmpty(agent.Name, agent.ID, agent.Command)
	details := []string{}
	if agent.Kind != "" {
		details = append(details, agent.Kind)
	}
	if agent.Command != "" {
		command := agent.Command
		if len(agent.Args) > 0 {
			command += " " + strings.Join(agent.Args, " ")
		}
		details = append(details, command)
	}
	if len(details) > 0 {
		name += "  " + strings.Join(details, " · ")
	}
	return name
}

func formatCommandStatus(status appviewmodel.StatusView) string {
	lines := []string{"status:"}
	if status.Session != nil {
		if sessionID := strings.TrimSpace(status.Session.Ref.SessionID); sessionID != "" {
			lines = append(lines, "  session: "+sessionID)
		}
		if title := strings.TrimSpace(status.Session.Title); title != "" {
			lines = append(lines, "  title: "+title)
		}
	}
	if status.Model.Current != nil {
		modelText := firstNonEmpty(status.Model.Current.Alias, status.Model.Current.Model, status.Model.Current.ID)
		detail := firstNonEmpty(status.Model.Current.Provider, status.Model.Current.Detail)
		if detail != "" {
			modelText += " (" + detail + ")"
		}
		lines = append(lines, "  model: "+modelText)
	} else if status.Model.Configured {
		lines = append(lines, fmt.Sprintf("  model: %d configured", status.Model.Count))
	} else {
		lines = append(lines, "  model: not configured")
	}
	if mode := strings.TrimSpace(status.Mode.Current.ID); mode != "" {
		lines = append(lines, "  mode: "+mode)
	}
	if status.Controller != nil {
		controller := firstNonEmpty(status.Controller.Agent, "acp")
		if status.Controller.RemoteSessionID != "" {
			controller += " remote=" + status.Controller.RemoteSessionID
		}
		if status.Controller.Lifecycle != nil && status.Controller.Lifecycle.Phase != "" {
			controller += " phase=" + status.Controller.Lifecycle.Phase
			if status.Controller.Lifecycle.Recovering {
				controller += " recovering"
			}
		}
		lines = append(lines, "  controller: "+controller)
		for _, diagnostic := range status.Controller.Diagnostics {
			if diagnostic.Message == "" {
				continue
			}
			lines = append(lines, "  controller_"+firstNonEmpty(diagnostic.Severity, "info")+": "+diagnostic.Message)
		}
	}
	if status.Runtime.StoreBackend != "" || status.Runtime.StoreURI != "" {
		store := firstNonEmpty(status.Runtime.StoreBackend, "store")
		if status.Runtime.StoreURI != "" {
			store += " " + status.Runtime.StoreURI
		}
		lines = append(lines, "  store: "+store)
	}
	if sandbox := strings.TrimSpace(status.Runtime.SandboxBackend); sandbox != "" {
		lines = append(lines, "  sandbox: "+sandbox)
	}
	if status.Agents.Count > 0 {
		lines = append(lines, fmt.Sprintf("  agents: %d", status.Agents.Count))
	}
	if status.Resources.ErrorCount > 0 || status.Resources.WarningCount > 0 {
		lines = append(lines, fmt.Sprintf("  resources: %d warnings, %d errors", status.Resources.WarningCount, status.Resources.ErrorCount))
	}
	if status.Usage.Total.TotalTokens > 0 {
		lines = append(lines, fmt.Sprintf("  tokens: %d", status.Usage.Total.TotalTokens))
	}
	return strings.Join(lines, "\n")
}

func formatCommandModels(choices []appsettings.ModelChoice, currentID string) string {
	lines := []string{"models:"}
	if len(choices) == 0 {
		lines = append(lines, "  none configured")
		return strings.Join(lines, "\n")
	}
	currentID = strings.TrimSpace(currentID)
	for _, choice := range choices {
		name := formatModelChoiceName(choice)
		markers := []string{}
		if choice.Default {
			markers = append(markers, "default")
		}
		if currentID != "" && strings.EqualFold(choice.ID, currentID) {
			markers = append(markers, "current")
		}
		if len(markers) > 0 {
			name += " (" + strings.Join(markers, ", ") + ")"
		}
		lines = append(lines, "  "+name)
	}
	return strings.Join(lines, "\n")
}

func formatCommandControllerModels(status ControllerStatus) string {
	lines := []string{"models:"}
	current := strings.TrimSpace(status.Model)
	if len(status.ModelOptions) == 0 {
		if current == "" {
			lines = append(lines, "  none declared")
		} else {
			lines = append(lines, "  "+current+" (current)")
		}
		return strings.Join(lines, "\n")
	}
	for _, option := range status.ModelOptions {
		name := firstNonEmpty(strings.TrimSpace(option.Name), strings.TrimSpace(option.Value))
		if name == "" {
			continue
		}
		if current != "" && (strings.EqualFold(current, strings.TrimSpace(option.Value)) || strings.EqualFold(current, strings.TrimSpace(option.Name))) {
			name += " (current)"
		}
		if detail := strings.TrimSpace(option.Description); detail != "" {
			name += "  " + detail
		}
		lines = append(lines, "  "+name)
	}
	if len(lines) == 1 {
		lines = append(lines, "  none declared")
	}
	return strings.Join(lines, "\n")
}

func formatModelChoiceName(choice appsettings.ModelChoice) string {
	label := firstNonEmpty(choice.Alias, choice.ID, choice.Model)
	providerModel := strings.Trim(strings.TrimSpace(choice.Provider)+"/"+strings.TrimSpace(choice.Model), "/")
	if label == "" {
		label = providerModel
	} else if providerModel != "" && !strings.EqualFold(label, providerModel) {
		label += "  " + providerModel
	}
	return label
}

func formatModelConfigName(cfg appsettings.ModelConfig) string {
	label := firstNonEmpty(cfg.Alias, cfg.ID, cfg.Model)
	providerModel := strings.Trim(strings.TrimSpace(cfg.Provider)+"/"+strings.TrimSpace(cfg.Model), "/")
	if label == "" {
		label = providerModel
	} else if providerModel != "" && !strings.EqualFold(label, providerModel) {
		label += "  " + providerModel
	}
	return label
}

func formatCommandSessions(page session.SessionPage) string {
	lines := []string{"available sessions:"}
	if len(page.Sessions) == 0 {
		lines = append(lines, "  none")
		return strings.Join(lines, "\n")
	}
	for _, item := range page.Sessions {
		sessionID := strings.TrimSpace(item.Session.SessionID)
		if sessionID == "" {
			continue
		}
		line := "  " + sessionID
		if title := strings.TrimSpace(item.Session.Title); title != "" {
			line += "  " + title
		}
		if !item.Session.UpdatedAt.IsZero() {
			line += "  (" + item.Session.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z") + ")"
		} else if !item.LastEventAt.IsZero() {
			line += "  (" + item.LastEventAt.UTC().Format("2006-01-02T15:04:05Z") + ")"
		}
		lines = append(lines, line)
	}
	if len(lines) == 1 {
		lines = append(lines, "  none")
	}
	return strings.Join(lines, "\n")
}

func formatCommandResume(snapshot session.Snapshot) string {
	sessionID := strings.TrimSpace(snapshot.Session.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(snapshot.Session.Ref.SessionID)
	}
	lines := []string{"resume session: " + sessionID}
	if title := strings.TrimSpace(snapshot.Session.Title); title != "" {
		lines = append(lines, "  title: "+title)
	}
	if cwd := strings.TrimSpace(snapshot.Session.Workspace.CWD); cwd != "" {
		lines = append(lines, "  cwd: "+cwd)
	}
	lines = append(lines, fmt.Sprintf("  events: %d", len(snapshot.Events)))
	return strings.Join(lines, "\n")
}
