package controladapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelcatalog"
	"github.com/caelis-labs/caelis/control/modelconfig"
	controller "github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/kernel"
)

const resumeCompletionPageLimit = 200

func (d *Adapter) CompleteFile(ctx context.Context, query string, limit int) ([]CompletionCandidate, error) {
	return completeWorkspaceFiles(ctx, d.WorkspaceDir(), query, limit)
}

func (d *Adapter) CompleteSkill(ctx context.Context, query string, limit int) ([]CompletionCandidate, error) {
	limit = normalizeCompletionLimit(limit)

	skills, err := d.skillCompletionMetas(ctx)
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
		scored = append(scored, scoredCompletion{
			candidate: skillCompletionCandidate(skill),
			score:     score,
		})
	}
	return sortAndTrimCandidates(scored, limit), nil
}

func (d *Adapter) skillCompletionMetas(ctx context.Context) ([]skill.Meta, error) {
	if d == nil || d.stack == nil {
		return nil, missingRuntimeDependency("skill discovery")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return d.stack.Skill.Snapshot().Metas(), nil
}

func (d *Adapter) CompleteResume(ctx context.Context, query string, limit int) ([]ResumeCandidate, error) {
	limit = normalizeCompletionLimit(limit)
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return d.ListSessions(ctx, limit)
	}
	ctx, cancel := completionContext(ctx, resumeCompletionTimeout)
	defer cancel()
	gw, err := d.gatewaySessions()
	if err != nil {
		return nil, err
	}
	matched := make([]ResumeCandidate, 0, limit)
	cursor := ""
	for {
		result, err := gw.ListSessions(ctx, kernel.ListSessionsRequest{
			AppName: d.stack.Session.AppName, UserID: d.stack.Session.UserID,
			WorkspaceKey: d.stack.Session.Workspace.Key,
			Cursor:       cursor, Limit: resumeCompletionPageLimit,
		})
		if err != nil {
			return nil, err
		}
		for _, summary := range result.Sessions {
			candidate := enrichResumeCandidate(ctx, d.stack.Session.Store, summary)
			if strings.TrimSpace(candidate.Prompt) == "" && strings.TrimSpace(candidate.Title) == "" {
				continue
			}
			if _, ok := scoreResumeCandidate(query, candidate); !ok {
				continue
			}
			matched = append(matched, candidate)
			if len(matched) >= limit {
				return matched, nil
			}
		}
		next := strings.TrimSpace(result.NextCursor)
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	return matched, nil
}

func (d *Adapter) CompleteSlashArg(ctx context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
	if limit <= 0 {
		limit = 8
	}
	query = strings.TrimSpace(strings.ToLower(query))
	command = strings.TrimSpace(command)
	normalizedCommand := strings.ToLower(command)
	if acpStatus, activeACP, err := d.activeACPControllerStatus(ctx); err != nil {
		return nil, err
	} else if activeACP {
		if candidates, handled := d.completeACPControllerSlashArg(acpStatus, command, query, limit); handled {
			return candidates, nil
		}
	}
	switch normalizedCommand {
	case "model use", "model del":
		return d.completeModelAliases(ctx, query, limit)
	case "plugin rm":
		return d.completePluginIDs(ctx, query, limit)
	case "plugin marketplace":
		return filterSlashCandidates(pluginMarketplaceActionCandidates(), query, limit), nil
	case "plugin marketplace update", "plugin marketplace rm":
		return d.completeMarketplaceNames(ctx, query, limit)
	case "connect":
		return completeConnectArgs(ctx, d, "connect", query, limit)
	}
	if strings.HasPrefix(normalizedCommand, "connect-") {
		return completeConnectArgs(ctx, d, command, query, limit)
	}
	if alias, ok := strings.CutPrefix(normalizedCommand, "model use "); ok {
		return d.completeModelReasoningLevels(ctx, alias, query, limit)
	}
	candidates := controlprompt.RootArgCandidates(command)
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

func filterSlashCandidates(candidates []SlashArgCandidate, query string, limit int) []SlashArgCandidate {
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
	return out
}

func (d *Adapter) completeACPControllerSlashArg(status controller.ControllerStatus, command string, query string, limit int) ([]SlashArgCandidate, bool) {
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

func (d *Adapter) completeModelReasoningLevels(ctx context.Context, aliasQuery string, query string, limit int) ([]SlashArgCandidate, error) {
	alias, err := d.resolveStoredModelAlias(ctx, aliasQuery)
	if err != nil {
		return nil, nil
	}
	if d.stack.Model.ConfigFn == nil {
		return nil, nil
	}
	cfg, ok := d.stack.Model.ConfigFn(alias)
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

func (d *Adapter) modelAliasSupportsReasoningLevel(alias string, level string) bool {
	if d.stack.Model.ConfigFn == nil {
		return false
	}
	cfg, ok := d.stack.Model.ConfigFn(alias)
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

func (d *Adapter) configuredModelReasoningLevels(cfg ModelConfig) []string {
	levels := modelconfig.NormalizeReasoningLevels(cfg.ReasoningLevels)
	for _, level := range modelconfig.NormalizeReasoningLevels(modelcatalog.ReasoningLevelsForModel(cfg.Provider, cfg.Model)) {
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
		return controller.ControllerMode{}, fmt.Errorf("app/gatewayapp/controladapter: remote ACP controller did not declare session modes")
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

func (d *Adapter) completeModelAliases(ctx context.Context, query string, limit int) ([]SlashArgCandidate, error) {
	ref := session.SessionRef{}
	if activeSession, ok := d.currentSession(); ok {
		ref = activeSession.SessionRef
	}
	choices, err := listModelChoices(ctx, d.stack.Model, ref)
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

func (d *Adapter) completeAgentCatalog(query string, limit int) []SlashArgCandidate {
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

func (d *Adapter) completeAgentParticipants(ctx context.Context, query string, limit int) ([]SlashArgCandidate, error) {
	activeSession, ok := d.currentSession()
	if !ok {
		return nil, nil
	}
	gw, err := d.gatewayControlPlane()
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

func (d *Adapter) agentCatalog(limit int) []AgentCandidate {
	if d.stack.Agent.ListFn == nil {
		return nil
	}
	available := d.stack.Agent.ListFn()
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

func (d *Adapter) resolveAgentName(input string) (string, error) {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return "", fmt.Errorf("app/gatewayapp/controladapter: agent name is required")
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
		return "", fmt.Errorf("app/gatewayapp/controladapter: agent %q is not configured", input)
	default:
		return "", fmt.Errorf("app/gatewayapp/controladapter: agent %q is ambiguous", input)
	}
}

func (d *Adapter) resolveParticipantID(ctx context.Context, ref session.SessionRef, input string) (string, error) {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return "", fmt.Errorf("app/gatewayapp/controladapter: participant id is required")
	}
	gw, err := d.gatewayControlPlane()
	if err != nil {
		return "", err
	}
	state, err := gw.ControlPlaneState(ctx, kernel.ControlPlaneStateRequest{SessionRef: ref})
	if err != nil {
		return "", err
	}
	runAgent, runHandle, directRun := controlagents.ParseRunName(input)
	var exact string
	prefixMatches := make([]string, 0, 2)
	for _, participant := range state.Participants {
		if participant.Kind != session.ParticipantKindACP || participant.Role != session.ParticipantRoleSidecar {
			continue
		}
		id := strings.TrimSpace(participant.ID)
		label := strings.TrimSpace(participant.Label)
		handle := strings.TrimPrefix(label, "@")
		sessionID := strings.TrimSpace(participant.SessionID)
		if id == "" {
			continue
		}
		if directRun {
			handle, profileRun := controlagents.DirectRunHandleFromSource(participant.Source)
			if profileRun && strings.EqualFold(string(handle), runAgent) &&
				strings.EqualFold(taskapi.NormalizeHandle(label), runHandle) {
				return id, nil
			}
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
		return "", fmt.Errorf("app/gatewayapp/controladapter: participant %q is not attached", input)
	default:
		return "", fmt.Errorf("app/gatewayapp/controladapter: participant %q is ambiguous", input)
	}
}

func (d *Adapter) resolveStoredModelAlias(ctx context.Context, input string) (string, error) {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return "", fmt.Errorf("app/gatewayapp/controladapter: model alias is required")
	}
	ref := session.SessionRef{}
	if activeSession, ok := d.currentSession(); ok {
		ref = activeSession.SessionRef
	}
	choices, err := listModelChoices(ctx, d.stack.Model, ref)
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
		return "", fmt.Errorf("app/gatewayapp/controladapter: ambiguous model alias %q", input)
	}
	prefixMatches = dedupeNonEmptyStrings(prefixMatches)
	switch len(prefixMatches) {
	case 1:
		return prefixMatches[0], nil
	case 0:
		return "", fmt.Errorf("app/gatewayapp/controladapter: unknown model alias %q", input)
	default:
		return "", fmt.Errorf("app/gatewayapp/controladapter: ambiguous model alias %q", input)
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

func (d *Adapter) completePluginIDs(ctx context.Context, query string, limit int) ([]SlashArgCandidate, error) {
	if d.stack.Plugin.ListPluginsFn == nil {
		return nil, missingRuntimeDependency("list plugins")
	}
	plugins, err := d.stack.Plugin.ListPluginsFn(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SlashArgCandidate, 0, min(limit, len(plugins)))
	for _, p := range plugins {
		if query != "" && !hasSlashArgPrefix(query, p.ID, p.Name, p.Description) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   p.ID,
			Display: p.ID,
			Detail:  p.Name,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func pluginMarketplaceActionCandidates() []SlashArgCandidate {
	return []SlashArgCandidate{
		{Value: "add", Display: "add", Detail: "Add a plugin marketplace"},
		{Value: "list", Display: "list", Detail: "List plugin marketplaces"},
		{Value: "update", Display: "update", Detail: "Refresh a plugin marketplace"},
		{Value: "rm", Display: "rm", Detail: "Remove a plugin marketplace"},
	}
}

func (d *Adapter) completeMarketplaceNames(ctx context.Context, query string, limit int) ([]SlashArgCandidate, error) {
	if d.stack.Plugin.ListMarketplacesFn == nil {
		return nil, missingRuntimeDependency("list marketplaces")
	}
	marketplaces, err := d.stack.Plugin.ListMarketplacesFn(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SlashArgCandidate, 0, min(limit, len(marketplaces)))
	for _, marketplace := range marketplaces {
		name := strings.TrimSpace(marketplace.Name)
		if name == "" {
			continue
		}
		if query != "" && !hasSlashArgPrefix(query, name, marketplace.Description, marketplace.Source) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   name,
			Display: name,
			Detail:  marketplaceCompletionDetail(marketplace),
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func marketplaceCompletionDetail(marketplace MarketplaceSnapshot) string {
	parts := compactNonEmpty([]string{
		strings.TrimSpace(marketplace.Description),
		marketplacePluginCountDetail(marketplace.PluginCount),
		strings.TrimSpace(marketplace.Source),
	})
	return strings.Join(parts, " · ")
}

func marketplacePluginCountDetail(count int) string {
	switch {
	case count == 1:
		return "1 plugin"
	case count > 1:
		return fmt.Sprintf("%d plugins", count)
	default:
		return ""
	}
}
