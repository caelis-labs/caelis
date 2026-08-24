package controladapter

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/control/modelconfig"
	controller "github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/kernel"
)

const resumeCompletionPageLimit = 200

func (d *assembler) CompleteFile(ctx context.Context, query string, limit int) ([]controlprompt.CompletionCandidate, error) {
	return completeWorkspaceFiles(ctx, d.WorkspaceDir(), query, limit)
}

func (d *assembler) CompleteSkill(ctx context.Context, query string, limit int) ([]controlprompt.CompletionCandidate, error) {
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

func (d *assembler) ResolveSkill(ctx context.Context, name string) (controlprompt.SkillResolveResult, error) {
	catalog, err := d.skillCompletionCatalog(ctx)
	if err != nil {
		return controlprompt.SkillResolveResult{}, err
	}
	return resolveSkillCatalog(catalog, name), nil
}

func (d *assembler) skillCompletionCatalog(ctx context.Context) (skill.Catalog, error) {
	if d == nil || d.deps == nil {
		return skill.Catalog{}, missingRuntimeDependency("skill discovery")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return skill.Catalog{}, err
		}
	}
	return d.deps.Skill.Snapshot(), nil
}

func (d *assembler) skillCompletionMetas(ctx context.Context) ([]skill.Meta, error) {
	catalog, err := d.skillCompletionCatalog(ctx)
	if err != nil {
		return nil, err
	}
	return catalog.Metas(), nil
}

func resolveSkillCatalog(catalog skill.Catalog, name string) controlprompt.SkillResolveResult {
	name = strings.TrimSpace(name)
	if name == "" {
		return controlprompt.SkillResolveResult{}
	}
	meta, status := catalog.Resolve(name)
	switch status {
	case skill.ResolveMatched:
		return controlprompt.SkillResolveResult{Canonical: strings.TrimSpace(meta.Name)}
	case skill.ResolveAmbiguous:
		matches := make([]string, 0, len(catalog.MatchingMetas(name)))
		for _, match := range catalog.MatchingMetas(name) {
			if canonical := strings.TrimSpace(match.Name); canonical != "" {
				matches = append(matches, canonical)
			}
		}
		sort.Strings(matches)
		return controlprompt.SkillResolveResult{Matches: matches}
	default:
		return controlprompt.SkillResolveResult{}
	}
}

func (d *assembler) CompleteResume(ctx context.Context, query string, limit int) ([]controlprompt.ResumeCandidate, error) {
	limit = normalizeCompletionLimit(limit)
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return d.listResumeCandidates(ctx, limit)
	}
	ctx, cancel := completionContext(ctx, resumeCompletionTimeout)
	defer cancel()
	matched := make([]controlprompt.ResumeCandidate, 0, limit)
	cursor := ""
	for {
		result, err := d.listSessions(ctx, kernel.ListSessionsRequest{
			AppName: d.deps.Session.AppName, UserID: d.deps.Session.UserID,
			CWD:    d.deps.Session.Workspace.CWD,
			Cursor: cursor, Limit: resumeCompletionPageLimit,
		})
		if err != nil {
			return nil, err
		}
		for _, summary := range result.Sessions {
			candidate := enrichResumeCandidate(summary)
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

func (d *assembler) CompleteSlashArg(ctx context.Context, command string, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
	if limit <= 0 {
		limit = 8
	}
	query = strings.TrimSpace(strings.ToLower(query))
	command = strings.TrimSpace(command)
	normalizedCommand := strings.ToLower(command)
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
	if strings.HasPrefix(normalizedCommand, "connect-acp-model:") {
		return nil, errors.New("app/gatewayapp/controladapter: ACP onboarding completion requires the principal-bound Agent preparation client")
	}
	if strings.HasPrefix(normalizedCommand, "connect-") {
		return completeConnectArgs(ctx, d, command, query, limit)
	}
	if alias, ok := strings.CutPrefix(normalizedCommand, "model use "); ok {
		return d.completeModelReasoningLevels(ctx, alias, query, limit)
	}
	candidates := controlprompt.RootArgCandidates(command)
	out := make([]controlprompt.SlashArgCandidate, 0, min(limit, len(candidates)))
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

func filterSlashCandidates(candidates []controlprompt.SlashArgCandidate, query string, limit int) []controlprompt.SlashArgCandidate {
	out := make([]controlprompt.SlashArgCandidate, 0, min(limit, len(candidates)))
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

func (d *assembler) completeModelReasoningLevels(ctx context.Context, aliasQuery string, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
	alias, err := d.resolveStoredModelAlias(ctx, aliasQuery)
	if err != nil {
		//nolint:nilerr // Completion is best-effort; an unresolved alias yields no candidates.
		return nil, nil
	}
	if d.deps.Model.ConfigFn == nil {
		return nil, nil
	}
	cfg, ok := d.deps.Model.ConfigFn(alias)
	if !ok {
		return nil, nil
	}
	levels := d.configuredModelReasoningLevels(cfg)
	out := make([]controlprompt.SlashArgCandidate, 0, min(limit, len(levels)))
	for _, level := range levels {
		if query != "" && !hasSlashArgPrefix(query, level) {
			continue
		}
		out = append(out, controlprompt.SlashArgCandidate{
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

func (d *assembler) modelAliasSupportsReasoningLevel(alias string, level string) bool {
	if d.deps.Model.ConfigFn == nil {
		return false
	}
	cfg, ok := d.deps.Model.ConfigFn(alias)
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

func (d *assembler) configuredModelReasoningLevels(cfg ModelConfig) []string {
	levels := modelconfig.NormalizeReasoningLevels(cfg.ReasoningLevels)
	for _, level := range modelconfig.ReasoningLevelsForConfig(cfg) {
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

func controllerChoicesToSlashCandidates(choices []controller.ControllerConfigChoice, detail string, query string, limit int) []controlprompt.SlashArgCandidate {
	if len(choices) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = len(choices)
	}
	out := make([]controlprompt.SlashArgCandidate, 0, min(limit, len(choices)))
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
		out = append(out, controlprompt.SlashArgCandidate{
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

func (d *assembler) completeModelAliases(ctx context.Context, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
	ref := session.SessionRef{}
	if activeSession, ok := d.currentSession(); ok {
		ref = activeSession.SessionRef
	}
	choices, err := listModelChoices(ctx, d.deps.Model, ref)
	if err != nil {
		return nil, err
	}
	out := make([]controlprompt.SlashArgCandidate, 0, min(limit, len(choices)))
	for _, choice := range choices {
		value := strings.TrimSpace(firstNonEmpty(choice.ID, choice.Alias))
		display := strings.TrimSpace(firstNonEmpty(choice.Alias, choice.ID))
		if display == "" {
			continue
		}
		if query != "" && !hasSlashArgPrefix(query, display) && !hasSlashArgPrefix(query, value) {
			continue
		}
		out = append(out, controlprompt.SlashArgCandidate{
			Value:   value,
			Display: display,
			Detail:  strings.TrimSpace(choice.Detail),
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (d *assembler) agentCatalog(limit int) []controlprompt.AgentCandidate {
	if d.deps.Agent.ListFn == nil {
		return nil
	}
	available := d.deps.Agent.ListFn()
	if len(available) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = len(available)
	}
	out := make([]controlprompt.AgentCandidate, 0, min(limit, len(available)))
	for _, agent := range available {
		out = append(out, controlprompt.AgentCandidate{
			Name:        strings.TrimSpace(agent.Name),
			Description: strings.TrimSpace(agent.Description),
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (d *assembler) resolveStoredModelAlias(ctx context.Context, input string) (string, error) {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return "", fmt.Errorf("app/gatewayapp/controladapter: model alias is required")
	}
	ref := session.SessionRef{}
	if activeSession, ok := d.currentSession(); ok {
		ref = activeSession.SessionRef
	}
	choices, err := listModelChoices(ctx, d.deps.Model, ref)
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

func (d *assembler) completePluginIDs(ctx context.Context, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
	if d.deps.Plugin.ListPluginsFn == nil {
		return nil, missingRuntimeDependency("list plugins")
	}
	plugins, err := d.deps.Plugin.ListPluginsFn(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]controlprompt.SlashArgCandidate, 0, min(limit, len(plugins)))
	for _, p := range plugins {
		if query != "" && !hasSlashArgPrefix(query, p.ID, p.Name, p.Description) {
			continue
		}
		out = append(out, controlprompt.SlashArgCandidate{
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

func pluginMarketplaceActionCandidates() []controlprompt.SlashArgCandidate {
	return []controlprompt.SlashArgCandidate{
		{Value: "add", Display: "add", Detail: "Add a plugin marketplace"},
		{Value: "list", Display: "list", Detail: "List plugin marketplaces"},
		{Value: "update", Display: "update", Detail: "Refresh a plugin marketplace"},
		{Value: "rm", Display: "rm", Detail: "Remove a plugin marketplace"},
	}
}

func (d *assembler) completeMarketplaceNames(ctx context.Context, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
	if d.deps.Plugin.ListMarketplacesFn == nil {
		return nil, missingRuntimeDependency("list marketplaces")
	}
	marketplaces, err := d.deps.Plugin.ListMarketplacesFn(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]controlprompt.SlashArgCandidate, 0, min(limit, len(marketplaces)))
	for _, marketplace := range marketplaces {
		name := strings.TrimSpace(marketplace.Name)
		if name == "" {
			continue
		}
		if query != "" && !hasSlashArgPrefix(query, name, marketplace.Description, marketplace.Source) {
			continue
		}
		out = append(out, controlprompt.SlashArgCandidate{
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

func marketplaceCompletionDetail(marketplace controlprompt.MarketplaceSnapshot) string {
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
