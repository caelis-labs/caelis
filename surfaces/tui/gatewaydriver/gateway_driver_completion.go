package gatewaydriver

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/session"
	tuicommands "github.com/OnslaughtSnail/caelis/surfaces/tui/commands"
)

func (d *GatewayDriver) CompleteMention(ctx context.Context, query string, limit int) ([]CompletionCandidate, error) {
	limit = normalizeCompletionLimit(limit)
	activeSession, ok := d.currentSession()
	if !ok {
		return []CompletionCandidate{}, nil
	}
	gw, err := d.gateway()
	if err != nil {
		return nil, err
	}
	state, err := gw.ControlPlaneState(ctx, kernel.ControlPlaneStateRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(query), "@"))
	out := make([]CompletionCandidate, 0, min(limit, len(state.Participants)))
	for _, participant := range state.Participants {
		if !isUserSideParticipant(participant) {
			continue
		}
		handle := strings.TrimPrefix(strings.TrimSpace(participant.Label), "@")
		if handle == "" {
			continue
		}
		agent := strings.TrimSpace(participant.AgentName)
		if agent == "" {
			agent = strings.TrimSpace(participant.ID)
		}
		if query != "" && !hasSlashArgPrefix(query, handle, agent, participant.SessionID, participant.DelegationID) {
			continue
		}
		display := handle
		if agent != "" {
			display = handle + "(" + agent + ")"
		}
		out = append(out, CompletionCandidate{
			Value:   handle,
			Display: display,
			Detail:  strings.Join(compactNonEmpty([]string{string(participant.Role), participant.SessionID}), " · "),
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func isUserSideParticipant(participant kernel.ParticipantState) bool {
	if participant.Role != session.ParticipantRoleSidecar {
		return false
	}
	return participant.Kind == session.ParticipantKindACP
}

func (d *GatewayDriver) CompleteFile(ctx context.Context, query string, limit int) ([]CompletionCandidate, error) {
	return completeWorkspaceFiles(ctx, d.WorkspaceDir(), query, limit)
}

func (d *GatewayDriver) CompleteSkill(ctx context.Context, query string, limit int) ([]CompletionCandidate, error) {
	limit = normalizeCompletionLimit(limit)

	skills, err := d.stack.DiscoverSkills(ctx, d.WorkspaceDir())
	if err != nil {
		return nil, err
	}
	workspace := d.WorkspaceDir()
	scored := make([]scoredCompletion, 0, len(skills))
	for _, skill := range skills {
		score, ok := scoreSkillMeta(query, skill, workspace)
		if !ok {
			continue
		}
		skillPath := firstNonEmpty(skill.Paths...)
		pathHint := displayPathHint(workspace, skillPath)
		detail := strings.Join(compactNonEmpty([]string{strings.TrimSpace(skill.Description), pathHint}), " · ")
		scored = append(scored, scoredCompletion{
			candidate: CompletionCandidate{
				Value:   strings.TrimSpace(skill.Name),
				Display: strings.TrimSpace(skill.Name),
				Detail:  strings.TrimSpace(detail),
				Path:    strings.TrimSpace(skillPath),
			},
			score: score,
		})
	}
	return sortAndTrimCandidates(scored, limit), nil
}

func (d *GatewayDriver) CompleteResume(ctx context.Context, query string, limit int) ([]ResumeCandidate, error) {
	limit = normalizeCompletionLimit(limit)
	all, err := d.ListSessions(ctx, limit)
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return all, nil
	}
	out := make([]ResumeCandidate, 0, min(limit, len(all)))
	for _, item := range all {
		if _, ok := scoreResumeCandidate(query, item); !ok {
			continue
		}
		out = append(out, item)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (d *GatewayDriver) CompleteSlashArg(ctx context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
	if limit <= 0 {
		limit = 8
	}
	query = strings.TrimSpace(strings.ToLower(query))
	if acpStatus, activeACP, err := d.activeACPControllerStatus(ctx); err != nil {
		return nil, err
	} else if activeACP {
		if candidates, handled := d.completeACPControllerSlashArg(acpStatus, command, query, limit); handled {
			return candidates, nil
		}
	}
	switch strings.TrimSpace(strings.ToLower(command)) {
	case "agent add":
		return d.completeBuiltInAgentCatalog(query, limit), nil
	case "agent install", "agent update":
		return d.completeInstallableBuiltInAgentCatalog(query, limit), nil
	case "agent add --install":
		return d.completeInstallableBuiltInAgentCatalog(query, limit), nil
	case "agent use":
		return d.completeAgentHandoffTargets(ctx, query, limit)
	case "agent remove":
		return d.completeRemovableAgentCatalog(query, limit), nil
	case "model use", "model del":
		return d.completeModelAliases(ctx, query, limit)
	case "connect":
		return completeConnectArgs(ctx, d, "connect", query, limit)
	}
	if strings.HasPrefix(strings.TrimSpace(strings.ToLower(command)), "connect-") {
		return completeConnectArgs(ctx, d, strings.TrimSpace(strings.ToLower(command)), query, limit)
	}
	if alias, ok := strings.CutPrefix(strings.TrimSpace(strings.ToLower(command)), "model use "); ok {
		return d.completeModelReasoningLevels(ctx, alias, query, limit)
	}
	candidates := tuicommands.RootArgCandidates(command)
	out := make([]SlashArgCandidate, 0, min(limit, len(candidates)))
	for _, candidate := range candidates {
		if query != "" && !hasSlashArgPrefix(query, candidate.Value, candidate.Display, candidate.Detail) {
			continue
		}
		out = append(out, candidate)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (d *GatewayDriver) completeACPControllerSlashArg(status controller.ControllerStatus, command string, query string, limit int) ([]SlashArgCandidate, bool) {
	normalized := strings.TrimSpace(strings.ToLower(command))
	switch normalized {
	case "model":
		candidate := SlashArgCandidate{
			Value:   "use",
			Display: "use",
			Detail:  "switch remote ACP model",
		}
		if query != "" && !hasSlashArgPrefix(query, candidate.Value, candidate.Display, candidate.Detail) {
			return nil, true
		}
		return []SlashArgCandidate{candidate}, true
	case "model use":
		return controllerChoicesToSlashCandidates(status.ModelOptions, "remote ACP model", query, limit), true
	case "model del", "model delete", "model rm":
		return nil, true
	}
	if alias, ok := strings.CutPrefix(normalized, "model use "); ok && strings.TrimSpace(alias) != "" {
		efforts := acpControllerEffortsForModel(status, alias)
		return controllerChoicesToSlashCandidates(efforts, "remote ACP reasoning effort", query, limit), true
	}
	return nil, false
}

func (d *GatewayDriver) completeModelReasoningLevels(ctx context.Context, aliasQuery string, query string, limit int) ([]SlashArgCandidate, error) {
	alias, err := d.resolveStoredModelAlias(ctx, aliasQuery)
	if err != nil {
		return nil, nil
	}
	cfg, ok := d.stack.ModelConfig(alias)
	if !ok {
		return nil, nil
	}
	levels := d.configuredModelReasoningLevels(cfg)
	out := make([]SlashArgCandidate, 0, min(limit, len(levels)))
	for _, level := range levels {
		if query != "" && !hasSlashArgPrefix(query, level) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   level,
			Display: level,
			Detail:  modelReasoningLevelDetail(level),
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (d *GatewayDriver) modelAliasSupportsReasoningLevel(alias string, level string) bool {
	cfg, ok := d.stack.ModelConfig(alias)
	if !ok {
		return false
	}
	for _, one := range d.configuredModelReasoningLevels(cfg) {
		if strings.EqualFold(strings.TrimSpace(one), strings.TrimSpace(level)) {
			return true
		}
	}
	return false
}

func (d *GatewayDriver) configuredModelReasoningLevels(cfg ModelConfig) []string {
	levels := normalizeReasoningLevels(cfg.ReasoningLevels)
	for _, level := range normalizeReasoningLevels(reasoningLevelsForModel(stackForDriver(d), cfg.Provider, cfg.Model)) {
		seen := false
		for _, existing := range levels {
			if strings.EqualFold(existing, level) {
				seen = true
				break
			}
		}
		if !seen {
			levels = append(levels, level)
		}
	}
	return levels
}

func modelReasoningLevelDetail(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "none":
		return "reasoning disabled"
	case "high", "medium", "low", "minimal", "xhigh":
		return "reasoning level"
	default:
		return "reasoning option"
	}
}

func controllerCommandNames(commands []controller.ControllerCommand) []string {
	if len(commands) == 0 {
		return nil
	}
	out := make([]string, 0, len(commands))
	seen := map[string]struct{}{}
	for _, command := range commands {
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(command.Name, "/")))
		if name == "" {
			continue
		}
		if fields := strings.Fields(name); len(fields) > 0 {
			name = fields[0]
		}
		if _, exists := seen[name]; exists {
			continue
		}
		out = append(out, name)
		seen[name] = struct{}{}
	}
	return out
}

func acpControllerModelText(status controller.ControllerStatus, activeSession session.Session) string {
	return firstNonEmpty(
		strings.TrimSpace(status.Model),
		strings.TrimSpace(status.Agent),
		strings.TrimSpace(activeSession.Controller.AgentName),
		strings.TrimSpace(activeSession.Controller.Label),
		strings.TrimSpace(activeSession.Controller.ControllerID),
	)
}

func acpControllerModeDisplay(status controller.ControllerStatus) string {
	current := strings.TrimSpace(status.Mode)
	if current == "" {
		return ""
	}
	if mode, ok := matchACPControllerMode(status.ModeOptions, current); ok {
		return acpControllerModeLabel(mode)
	}
	return current
}

func nextACPControllerMode(status controller.ControllerStatus) (controller.ControllerMode, error) {
	modes := compactACPControllerModes(status.ModeOptions)
	if len(modes) == 0 {
		return controller.ControllerMode{}, fmt.Errorf("surfaces/tui/gatewaydriver: remote ACP controller did not declare session modes")
	}
	current := strings.TrimSpace(status.Mode)
	if current == "" {
		return modes[0], nil
	}
	for i, mode := range modes {
		if strings.EqualFold(strings.TrimSpace(mode.ID), current) || strings.EqualFold(strings.TrimSpace(mode.Name), current) {
			return modes[(i+1)%len(modes)], nil
		}
	}
	return modes[0], nil
}

func compactACPControllerModes(modes []controller.ControllerMode) []controller.ControllerMode {
	if len(modes) == 0 {
		return nil
	}
	out := make([]controller.ControllerMode, 0, len(modes))
	seen := map[string]struct{}{}
	for _, mode := range modes {
		id := strings.TrimSpace(mode.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, controller.ControllerMode{
			ID:          id,
			Name:        strings.TrimSpace(mode.Name),
			Description: strings.TrimSpace(mode.Description),
		})
	}
	return out
}

func matchACPControllerMode(modes []controller.ControllerMode, requested string) (controller.ControllerMode, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return controller.ControllerMode{}, false
	}
	for _, mode := range modes {
		id := strings.TrimSpace(mode.ID)
		if id == "" {
			continue
		}
		if strings.EqualFold(id, requested) || strings.EqualFold(strings.TrimSpace(mode.Name), requested) {
			return mode, true
		}
	}
	return controller.ControllerMode{}, false
}

func acpControllerModeLabel(mode controller.ControllerMode) string {
	return firstNonEmpty(strings.TrimSpace(mode.Name), strings.TrimSpace(mode.ID))
}

func acpControllerEffortsForModel(status controller.ControllerStatus, model string) []controller.ControllerConfigChoice {
	model = strings.ToLower(strings.TrimSpace(model))
	if model != "" {
		for key, efforts := range status.EffortOptionsByModel {
			if strings.EqualFold(strings.TrimSpace(key), model) {
				return efforts
			}
		}
	}
	return status.EffortOptions
}

func controllerChoicesToSlashCandidates(choices []controller.ControllerConfigChoice, detail string, query string, limit int) []SlashArgCandidate {
	if len(choices) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = len(choices)
	}
	out := make([]SlashArgCandidate, 0, min(limit, len(choices)))
	for _, choice := range choices {
		value := strings.TrimSpace(choice.Value)
		if value == "" {
			continue
		}
		display := firstNonEmpty(strings.TrimSpace(choice.Name), value)
		candidateDetail := firstNonEmpty(strings.TrimSpace(choice.Description), detail)
		if query != "" && !hasSlashArgPrefix(query, value, display, candidateDetail) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   value,
			Display: display,
			Detail:  candidateDetail,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (d *GatewayDriver) completeModelAliases(ctx context.Context, query string, limit int) ([]SlashArgCandidate, error) {
	ref := session.SessionRef{}
	if activeSession, ok := d.currentSession(); ok {
		ref = activeSession.SessionRef
	}
	choices, err := d.stack.ListModelChoices(ctx, ref)
	if err != nil {
		return nil, err
	}
	out := make([]SlashArgCandidate, 0, min(limit, len(choices)))
	for _, choice := range choices {
		value := strings.TrimSpace(firstNonEmpty(choice.ID, choice.Alias))
		display := strings.TrimSpace(firstNonEmpty(choice.Alias, choice.ID))
		if display == "" {
			continue
		}
		if query != "" && !hasSlashArgPrefix(query, display) && !hasSlashArgPrefix(query, value) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   value,
			Display: display,
			Detail:  firstNonEmpty(strings.TrimSpace(choice.Detail), "configured model alias"),
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (d *GatewayDriver) completeAgentCatalog(query string, limit int) []SlashArgCandidate {
	agents := d.agentCatalog(limit)
	if len(agents) == 0 {
		return nil
	}
	out := make([]SlashArgCandidate, 0, min(limit, len(agents)))
	for _, agent := range agents {
		if query != "" && !hasSlashArgPrefix(query, agent.Name, agent.Description) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   agent.Name,
			Display: agent.Name,
			Detail:  firstNonEmpty(agent.Description, "configured ACP agent"),
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (d *GatewayDriver) completeBuiltInAgentCatalog(query string, limit int) []SlashArgCandidate {
	options := d.stack.ListBuiltinACPAgentAddOptions()
	if len(options) == 0 {
		return nil
	}
	out := make([]SlashArgCandidate, 0, min(limit, len(options)))
	for _, option := range options {
		if query != "" && !hasSlashArgPrefix(query, option.Value, option.Display, option.Detail) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   option.Value,
			Display: option.Display,
			Detail:  firstNonEmpty(option.Detail, "built-in ACP agent"),
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (d *GatewayDriver) completeRemovableAgentCatalog(query string, limit int) []SlashArgCandidate {
	agents := d.completeAgentCatalog(query, limit)
	if len(agents) == 0 {
		return nil
	}
	out := make([]SlashArgCandidate, 0, len(agents))
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.Value), "self") || strings.EqualFold(strings.TrimSpace(agent.Display), "self") {
			continue
		}
		out = append(out, agent)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (d *GatewayDriver) completeInstallableBuiltInAgentCatalog(query string, limit int) []SlashArgCandidate {
	options := d.stack.ListInstallableACPAgentOptions()
	if len(options) == 0 {
		return nil
	}
	out := make([]SlashArgCandidate, 0, min(limit, len(options)))
	for _, option := range options {
		if query != "" && !hasSlashArgPrefix(query, option.Value, option.Display, option.Detail) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   option.Value,
			Display: option.Display,
			Detail:  firstNonEmpty(option.Detail, "install ACP agent adapter"),
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (d *GatewayDriver) completeAgentParticipants(ctx context.Context, query string, limit int) ([]SlashArgCandidate, error) {
	activeSession, ok := d.currentSession()
	if !ok {
		return nil, nil
	}
	gw, err := d.gateway()
	if err != nil {
		return nil, err
	}
	state, err := gw.ControlPlaneState(ctx, kernel.ControlPlaneStateRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		return nil, err
	}
	out := make([]SlashArgCandidate, 0, min(limit, len(state.Participants)))
	for _, participant := range state.Participants {
		id := strings.TrimSpace(participant.ID)
		label := strings.TrimSpace(firstNonEmpty(participant.Label, participant.ID))
		if id == "" {
			continue
		}
		if query != "" && !hasSlashArgPrefix(query, id, label, participant.SessionID, string(participant.Role)) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   id,
			Display: label,
			Detail:  strings.Join(compactNonEmpty([]string{string(participant.Role), strings.TrimSpace(participant.SessionID)}), " · "),
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (d *GatewayDriver) completeAgentHandoffTargets(ctx context.Context, query string, limit int) ([]SlashArgCandidate, error) {
	out := []SlashArgCandidate{{
		Value:   "local",
		Display: "local",
		Detail:  "return to local kernel",
	}}
	if query != "" && !hasSlashArgPrefix(query, "local", "kernel") {
		out = nil
	}
	for _, agent := range d.completeAgentCatalog(query, limit) {
		out = append(out, SlashArgCandidate{
			Value:   agent.Value,
			Display: agent.Display,
			Detail:  agent.Detail,
		})
		if len(out) >= limit {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (d *GatewayDriver) agentCatalog(limit int) []AgentCandidate {
	available := d.stack.ListACPAgents()
	if len(available) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = len(available)
	}
	out := make([]AgentCandidate, 0, min(limit, len(available)))
	for _, agent := range available {
		out = append(out, AgentCandidate{
			Name:        strings.TrimSpace(agent.Name),
			Description: strings.TrimSpace(agent.Description),
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (d *GatewayDriver) resolveHandoffAgentName(ctx context.Context, ref session.SessionRef, input string) (string, error) {
	if agent, err := d.resolveAgentName(input); err == nil {
		return agent, nil
	}
	participantID, err := d.resolveParticipantID(ctx, ref, input)
	if err != nil {
		return "", err
	}
	gw, err := d.gateway()
	if err != nil {
		return "", err
	}
	state, err := gw.ControlPlaneState(ctx, kernel.ControlPlaneStateRequest{SessionRef: ref})
	if err != nil {
		return "", err
	}
	for _, participant := range state.Participants {
		if strings.EqualFold(strings.TrimSpace(participant.ID), participantID) {
			return strings.TrimSpace(firstNonEmpty(participant.Label, participant.ID)), nil
		}
	}
	return "", fmt.Errorf("surfaces/tui/gatewaydriver: participant %q is not attached", input)
}

func (d *GatewayDriver) resolveAgentName(input string) (string, error) {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: agent name is required")
	}
	var exact string
	prefixMatches := make([]string, 0, 2)
	for _, agent := range d.agentCatalog(0) {
		name := strings.TrimSpace(agent.Name)
		normalized := strings.ToLower(name)
		if normalized == "" {
			continue
		}
		if normalized == input {
			exact = name
			break
		}
		if strings.HasPrefix(normalized, input) {
			prefixMatches = append(prefixMatches, name)
		}
	}
	if exact != "" {
		return exact, nil
	}
	switch len(prefixMatches) {
	case 1:
		return prefixMatches[0], nil
	case 0:
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: agent %q is not configured", input)
	default:
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: agent %q is ambiguous", input)
	}
}

func (d *GatewayDriver) resolveParticipantID(ctx context.Context, ref session.SessionRef, input string) (string, error) {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: participant id is required")
	}
	gw, err := d.gateway()
	if err != nil {
		return "", err
	}
	state, err := gw.ControlPlaneState(ctx, kernel.ControlPlaneStateRequest{SessionRef: ref})
	if err != nil {
		return "", err
	}
	var exact string
	prefixMatches := make([]string, 0, 2)
	for _, participant := range state.Participants {
		if participant.Kind != session.ParticipantKindACP {
			continue
		}
		id := strings.TrimSpace(participant.ID)
		label := strings.TrimSpace(participant.Label)
		handle := strings.TrimPrefix(label, "@")
		sessionID := strings.TrimSpace(participant.SessionID)
		if id == "" {
			continue
		}
		if strings.EqualFold(id, input) || strings.EqualFold(label, input) || strings.EqualFold(handle, input) || strings.EqualFold(sessionID, input) {
			return id, nil
		}
		for _, candidate := range []string{id, label, handle, sessionID} {
			candidate = strings.ToLower(strings.TrimSpace(candidate))
			if candidate != "" && strings.HasPrefix(candidate, input) {
				exact = id
				prefixMatches = append(prefixMatches, exact)
				break
			}
		}
	}
	switch len(dedupeNonEmptyStrings(prefixMatches)) {
	case 1:
		return dedupeNonEmptyStrings(prefixMatches)[0], nil
	case 0:
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: participant %q is not attached", input)
	default:
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: participant %q is ambiguous", input)
	}
}

func (d *GatewayDriver) resolveStoredModelAlias(ctx context.Context, input string) (string, error) {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: model alias is required")
	}
	ref := session.SessionRef{}
	if activeSession, ok := d.currentSession(); ok {
		ref = activeSession.SessionRef
	}
	choices, err := d.stack.ListModelChoices(ctx, ref)
	if err != nil {
		return "", err
	}
	var exact string
	exactAliasMatches := make([]string, 0, 2)
	prefixMatches := make([]string, 0, 2)
	for _, choice := range choices {
		id := strings.TrimSpace(firstNonEmpty(choice.ID, choice.Alias))
		alias := strings.TrimSpace(choice.Alias)
		normalizedID := strings.ToLower(id)
		normalizedAlias := strings.ToLower(alias)
		if normalizedID == "" && normalizedAlias == "" {
			continue
		}
		if normalizedID == input {
			exact = id
			break
		}
		if normalizedAlias == input {
			exactAliasMatches = append(exactAliasMatches, id)
			continue
		}
		if strings.HasPrefix(normalizedID, input) || strings.HasPrefix(normalizedAlias, input) {
			prefixMatches = append(prefixMatches, id)
		}
	}
	if exact != "" {
		return exact, nil
	}
	switch len(dedupeNonEmptyStrings(exactAliasMatches)) {
	case 1:
		return dedupeNonEmptyStrings(exactAliasMatches)[0], nil
	case 0:
	default:
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: ambiguous model alias %q", input)
	}
	prefixMatches = dedupeNonEmptyStrings(prefixMatches)
	switch len(prefixMatches) {
	case 1:
		return prefixMatches[0], nil
	case 0:
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: unknown model alias %q", input)
	default:
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: ambiguous model alias %q", input)
	}
}

func hasSlashArgPrefix(query string, values ...string) bool {
	if query == "" {
		return true
	}
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if strings.HasPrefix(normalized, query) {
			return true
		}
	}
	return false
}
