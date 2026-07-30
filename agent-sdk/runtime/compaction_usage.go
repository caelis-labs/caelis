package runtime

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/prefixusage"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func snapshotUsageWithResolvedWindow(promptEvents []*session.Event, window int, cfg CompactionConfig) compact.UsageSnapshot {
	return snapshotUsageWithResolvedWindowUsing(promptEvents, window, cfg, nil)
}

func snapshotUsageWithResolvedWindowUsing(
	promptEvents []*session.Event,
	window int,
	cfg CompactionConfig,
	useProviderSnapshot func(providerTokenSnapshot) bool,
) compact.UsageSnapshot {
	usage, _, _ := snapshotUsageWithResolvedWindowDetails(promptEvents, window, cfg, useProviderSnapshot)
	return usage
}

func snapshotUsageWithResolvedWindowDetails(
	promptEvents []*session.Event,
	window int,
	cfg CompactionConfig,
	useProviderSnapshot func(providerTokenSnapshot) bool,
) (compact.UsageSnapshot, providerTokenSnapshot, bool) {
	cfg = normalizeCompactionConfig(cfg)
	if window <= 0 {
		window = cfg.DefaultContextWindowTokens
	}
	reserve := resolveReserveOutputTokens(window, cfg.ReserveOutputTokens)
	safety := resolveSafetyMarginTokens(window, cfg.SafetyMarginTokens)
	effective := resolveEffectiveInputBudget(window, reserve, safety)

	total := 0
	delta := 0
	prefix := 0
	asOfEventID := ""
	source := compact.UsageSourceEstimated
	providerSnapshot, hasProviderSnapshot := latestProviderTokenSnapshotUsing(promptEvents, useProviderSnapshot)
	if hasProviderSnapshot {
		total = providerSnapshot.BaselineTokens
		delta = estimateTokensFromIndex(promptEvents, providerSnapshot.DeltaStartIndex)
		total += delta
		asOfEventID = providerSnapshot.EventID
		source = compact.UsageSourceProvider
	} else {
		prefix = cfg.EstimatedPromptPrefixTokens
		total = estimatePromptEventsTokens(promptEvents) + prefix
	}
	return compact.UsageSnapshot{
		TotalTokens:           total,
		ContextWindowTokens:   window,
		EffectiveInputBudget:  effective,
		EstimatedDeltaTokens:  delta,
		EstimatedPrefixTokens: prefix,
		Source:                source,
		AsOfEventID:           asOfEventID,
	}, providerSnapshot, hasProviderSnapshot
}

func splitEventsByTokenBudget(events []*session.Event, budget int) [][]*session.Event {
	if budget <= 0 {
		budget = 24000
	}
	chunks := make([][]*session.Event, 0, 4)
	current := make([]*session.Event, 0, 8)
	currentTokens := 0
	for _, ev := range events {
		if ev == nil {
			continue
		}
		tokens := estimatePromptEventTokens(ev)
		if len(current) > 0 && currentTokens+tokens > budget {
			chunks = append(chunks, current)
			current = make([]*session.Event, 0, 8)
			currentTokens = 0
		}
		current = append(current, session.CloneEvent(ev))
		currentTokens += tokens
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

type providerTokenSnapshot struct {
	BaselineTokens          int
	DeltaStartIndex         int
	EventID                 string
	Provider                string
	Model                   string
	PromptPrefixFingerprint string
	PromptPrefixTokens      int
}

func latestProviderTokenSnapshot(events []*session.Event) (providerTokenSnapshot, bool) {
	return latestProviderTokenSnapshotUsing(events, nil)
}

func latestProviderTokenSnapshotUsing(
	events []*session.Event,
	useProviderSnapshot func(providerTokenSnapshot) bool,
) (providerTokenSnapshot, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		meta := eventUsageMetadata(event)
		if event == nil || len(meta) == 0 {
			continue
		}
		baseline, includeSnapshotGroup, ok := providerPromptBaselineTokens(meta)
		if !ok || baseline <= 0 {
			continue
		}
		start := providerSnapshotGroupStart(events, i)
		deltaStart := start
		if !includeSnapshotGroup {
			deltaStart = i + 1
		}
		provider, modelName := providerSnapshotIdentity(event, meta)
		snapshot := providerTokenSnapshot{
			BaselineTokens:          baseline,
			DeltaStartIndex:         deltaStart,
			Provider:                provider,
			Model:                   modelName,
			PromptPrefixFingerprint: providerPromptPrefixFingerprint(event),
			PromptPrefixTokens:      providerPromptPrefixTokens(event),
		}
		if id := strings.TrimSpace(events[start].ID); id != "" {
			snapshot.EventID = id
		} else if id := strings.TrimSpace(event.ID); id != "" {
			snapshot.EventID = id
		} else {
			continue
		}
		if useProviderSnapshot != nil && !useProviderSnapshot(snapshot) {
			continue
		}
		return snapshot, true
	}
	return providerTokenSnapshot{}, false
}

func providerPromptPrefixFingerprint(event *session.Event) string {
	if event == nil || event.Invocation == nil {
		return ""
	}
	return strings.TrimSpace(event.Invocation.PromptPrefixFingerprint)
}

func providerPromptPrefixTokens(event *session.Event) int {
	if event == nil || event.Invocation == nil {
		return 0
	}
	return max(event.Invocation.PromptPrefixTokens, 0)
}

func providerSnapshotCompatibleWithLLM(snapshot providerTokenSnapshot, llm model.LLM) bool {
	if llm == nil ||
		strings.TrimSpace(snapshot.Provider) == "" ||
		strings.TrimSpace(snapshot.Model) == "" {
		return false
	}
	currentProvider := ""
	if provider, ok := llm.(interface{ ProviderName() string }); ok {
		currentProvider = strings.TrimSpace(provider.ProviderName())
	}
	return providerSnapshotCompatibleWithIdentity(snapshot, currentProvider, llm.Name())
}

func providerSnapshotCompatibleWithIdentity(snapshot providerTokenSnapshot, provider string, modelName string) bool {
	provider, modelName = session.StableInvocationIdentity(provider, modelName)
	snapshotProvider, snapshotModel := session.StableInvocationIdentity(snapshot.Provider, snapshot.Model)
	if provider == "" || modelName == "" || snapshotProvider == "" || snapshotModel == "" {
		return false
	}
	return strings.EqualFold(snapshotProvider, provider) &&
		strings.EqualFold(snapshotModel, modelName)
}

func providerSnapshotIdentity(event *session.Event, meta map[string]any) (string, string) {
	provider := ""
	modelName := ""
	if event != nil && event.Invocation != nil {
		provider = strings.TrimSpace(event.Invocation.Provider)
		modelName = strings.TrimSpace(event.Invocation.Model)
	}
	provider = firstNonEmpty(provider, strings.TrimSpace(stringifyAny(meta["provider"])))
	modelName = firstNonEmpty(modelName, strings.TrimSpace(stringifyAny(meta["model"])))
	if sdkMeta := nestedMap(meta, "caelis", "sdk"); len(sdkMeta) > 0 {
		provider = firstNonEmpty(provider, strings.TrimSpace(stringifyAny(sdkMeta["provider"])))
		modelName = firstNonEmpty(modelName, strings.TrimSpace(stringifyAny(sdkMeta["model"])))
	}
	return provider, modelName
}

func providerPromptBaselineTokens(meta map[string]any) (int, bool, bool) {
	if len(meta) == 0 {
		return 0, false, false
	}
	if usage := nestedMap(meta, "caelis", "sdk", "usage"); len(usage) > 0 {
		if value, ok := intFromAny(usage["prompt_tokens"]); ok && value > 0 {
			return providerPromptBaselineWithCachedInput(meta, usage, value), true, true
		}
		total, totalOK := intFromAny(usage["total_tokens"])
		completion, completionOK := intFromAny(usage["completion_tokens"])
		if totalOK && completionOK && total > 0 {
			return providerPromptBaselineWithCachedInput(meta, usage, max(total-completion, 0)), true, true
		}
		if totalOK && total > 0 {
			return total, false, true
		}
	}
	if value, ok := intFromAny(meta["prompt_tokens"]); ok && value > 0 {
		return providerPromptBaselineWithCachedInput(meta, meta, value), true, true
	}
	total, totalOK := intFromAny(meta["total_tokens"])
	completion, completionOK := intFromAny(meta["completion_tokens"])
	if totalOK && completionOK && total > 0 {
		return providerPromptBaselineWithCachedInput(meta, meta, max(total-completion, 0)), true, true
	}
	if totalOK && total > 0 {
		return total, false, true
	}
	return 0, false, false
}

func providerPromptBaselineWithCachedInput(meta map[string]any, usage map[string]any, baseline int) int {
	if baseline <= 0 || !providerUsageSeparatesCachedInputMeta(meta, usage) {
		return baseline
	}
	cached := firstPositiveIntFromAny(
		usage["cached_input_tokens"],
		usage["cache_read_input_tokens"],
		usage["cached_prompt_tokens"],
		usage["prompt_cache_hit_tokens"],
	)
	return baseline + cached
}

func providerUsageSeparatesCachedInputMeta(meta map[string]any, usage map[string]any) bool {
	provider := strings.TrimSpace(stringifyAny(meta["provider"]))
	if sdkMeta := nestedMap(meta, "caelis", "sdk"); len(sdkMeta) > 0 {
		provider = firstNonEmpty(provider, strings.TrimSpace(stringifyAny(sdkMeta["provider"])))
	}
	return model.ProviderSeparatesCachedInputForUsage(provider, usage)
}

func firstPositiveIntFromAny(values ...any) int {
	for _, value := range values {
		if n, ok := intFromAny(value); ok && n > 0 {
			return n
		}
	}
	return 0
}

func providerSnapshotGroupStart(events []*session.Event, end int) int {
	if end < 0 || end >= len(events) {
		return end
	}
	target := providerSnapshotSignature(events[end])
	if target == "" {
		return end
	}
	start := end
	for start > 0 {
		prev := events[start-1]
		if prev == nil || providerSnapshotSignature(prev) != target {
			break
		}
		start--
	}
	return start
}

func providerSnapshotSignature(event *session.Event) string {
	meta := eventUsageMetadata(event)
	if event == nil || len(meta) == 0 {
		return ""
	}
	prompt, _ := intFromAny(meta["prompt_tokens"])
	cached, _ := intFromAny(meta["cached_input_tokens"])
	completion, _ := intFromAny(meta["completion_tokens"])
	total, _ := intFromAny(meta["total_tokens"])
	provider, model := providerSnapshotIdentity(event, meta)
	if sdkMeta := nestedMap(meta, "caelis", "sdk"); len(sdkMeta) > 0 {
		if usage := nestedMap(meta, "caelis", "sdk", "usage"); len(usage) > 0 {
			if value, ok := intFromAny(usage["prompt_tokens"]); ok {
				prompt = value
			}
			if value, ok := intFromAny(usage["cached_input_tokens"]); ok {
				cached = value
			}
			if value, ok := intFromAny(usage["cache_read_input_tokens"]); ok {
				cached = value
			}
			if value, ok := intFromAny(usage["completion_tokens"]); ok {
				completion = value
			}
			if value, ok := intFromAny(usage["total_tokens"]); ok {
				total = value
			}
		}
	}
	if prompt <= 0 && cached <= 0 && completion <= 0 && total <= 0 && provider == "" && model == "" {
		return ""
	}
	return fmt.Sprintf("%s|%s|%d|%d|%d|%d", provider, model, prompt, cached, completion, total)
}

func eventUsageMetadata(event *session.Event) map[string]any {
	if event == nil {
		return nil
	}
	return event.Meta
}

func nestedMap(values map[string]any, path ...string) map[string]any {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = mapped[key]
	}
	out, _ := current.(map[string]any)
	return out
}

func estimateTokensFromIndex(events []*session.Event, index int) int {
	if index <= 0 {
		return estimatePromptEventsTokens(events)
	}
	total := 0
	for _, event := range events[index:] {
		total += estimatePromptEventTokens(event)
	}
	return total
}

func estimatePromptEventsTokens(events []*session.Event) int {
	total := 0
	for _, event := range events {
		total += estimatePromptEventTokens(event)
	}
	return total
}

func estimatePromptEventTokens(event *session.Event) int {
	if event == nil {
		return 0
	}
	if event.Message != nil {
		return estimateMessageTokens(*event.Message)
	}
	if text := strings.TrimSpace(session.EventText(event)); text != "" {
		return estimateTextTokens(text)
	}
	return 0
}

func estimateMessageTokens(message model.Message) int {
	structural := 0
	for _, part := range message.Parts {
		structural += estimatePartTokens(part)
	}
	legacy := estimateLegacyMessageTokens(message)
	return max(max(structural, legacy), 1)
}

func estimateLegacyMessageTokens(message model.Message) int {
	total := 0
	if text := strings.TrimSpace(message.TextContent()); text != "" {
		total += estimateTextTokens(text)
	}
	for _, call := range message.ToolCalls() {
		total += estimateTextTokens(call.Name) + estimateTextTokens(call.Args)
	}
	for _, result := range message.ToolResults() {
		total += estimateTextTokens(result.Name)
		response := model.Message{
			Role: model.RoleTool,
			Parts: []model.Part{{
				Kind:       model.PartKindToolResult,
				ToolResult: &result,
			}},
		}.ToolResponse()
		if response != nil {
			payload := stringifyAny(response.Result)
			estimated := estimateTextTokens(payload)
			total += max(estimated+32, int(float64(estimated)*1.25))
		}
	}
	return total
}

func resolveContextWindowTokens(llm model.LLM, fallback int) int {
	if provider, ok := llm.(compact.ContextWindowProvider); ok {
		if tokens := provider.ContextWindowTokens(); tokens > 0 {
			return tokens
		}
	}
	return fallback
}

func resolveReserveOutputTokens(window int, configured int) int {
	if configured <= 0 {
		configured = defaultReserveOutputTokens(window)
	}
	if window <= 0 {
		return configured
	}
	maxReserve := max(window/4, 256)
	if configured > maxReserve {
		return maxReserve
	}
	return configured
}

func defaultReserveOutputTokens(window int) int {
	return policyForContextWindow(window).reserveOutputTokens
}

func resolveSafetyMarginTokens(window int, configured int) int {
	if configured <= 0 {
		configured = defaultSafetyMarginTokens(window)
	}
	if window <= 0 {
		return configured
	}
	maxSafety := max(window/8, 256)
	if configured > maxSafety {
		return maxSafety
	}
	return configured
}

func defaultSafetyMarginTokens(window int) int {
	return policyForContextWindow(window).safetyMarginTokens
}

func resolveEffectiveInputBudget(window, reserve, safety int) int {
	if window <= 0 {
		return 1
	}
	effective := window - reserve - safety
	if effective <= 0 {
		effective = window - reserve
	}
	if effective <= 0 {
		effective = window / 2
	}
	return max(min(effective, window), 1)
}

func dynamicWatermarks(window int, configuredSoft, configuredForce float64) (float64, float64) {
	if configuredSoft > 0 && configuredForce > 0 {
		if configuredForce < configuredSoft {
			configuredForce = configuredSoft
		}
		return configuredSoft, configuredForce
	}
	policy := policyForContextWindow(window)
	return policy.softWatermark, policy.forceWatermark
}

func dynamicEmergencyWatermark(window int, configuredForce float64) float64 {
	base := policyForContextWindow(window).emergencyWatermark
	if configuredForce > base {
		return min(configuredForce, 0.99)
	}
	return base
}

type contextWindowPolicy struct {
	reserveOutputTokens int
	safetyMarginTokens  int
	softWatermark       float64
	forceWatermark      float64
	emergencyWatermark  float64
}

func policyForContextWindow(window int) contextWindowPolicy {
	switch {
	case window >= 1_000_000:
		return contextWindowPolicy{
			reserveOutputTokens: min(max(window/32, 32000), 64000),
			safetyMarginTokens:  min(max(window/128, 8000), 16000),
			softWatermark:       0.99,
			forceWatermark:      0.995,
			emergencyWatermark:  0.998,
		}
	case window >= 200000:
		return contextWindowPolicy{
			reserveOutputTokens: min(max(window/32, 8000), 24000),
			safetyMarginTokens:  min(max(window/256, 2048), 8000),
			softWatermark:       0.95,
			forceWatermark:      0.98,
			emergencyWatermark:  0.99,
		}
	case window >= 96000:
		// Keep 96k-199k models around an 85% raw-window soft trigger after
		// output reserve and safety margin are accounted for.
		return contextWindowPolicy{
			reserveOutputTokens: 4096,
			safetyMarginTokens:  1536,
			softWatermark:       0.90,
			forceWatermark:      0.94,
			emergencyWatermark:  0.97,
		}
	case window >= 64000:
		return contextWindowPolicy{
			reserveOutputTokens: 4096,
			safetyMarginTokens:  1536,
			softWatermark:       0.88,
			forceWatermark:      0.93,
			emergencyWatermark:  0.96,
		}
	case window >= 32000:
		return contextWindowPolicy{
			reserveOutputTokens: 2048,
			safetyMarginTokens:  1024,
			softWatermark:       0.82,
			forceWatermark:      0.90,
			emergencyWatermark:  0.94,
		}
	default:
		return contextWindowPolicy{
			reserveOutputTokens: 2048,
			safetyMarginTokens:  1024,
			softWatermark:       0.78,
			forceWatermark:      0.88,
			emergencyWatermark:  0.92,
		}
	}
}

func evaluateWatermark(usage compact.UsageSnapshot, cfg CompactionConfig) compact.TriggerDecision {
	if usage.EffectiveInputBudget <= 0 {
		return compact.TriggerDecision{}
	}
	softRatio, forceRatio := dynamicWatermarks(usage.ContextWindowTokens, cfg.WatermarkRatio, cfg.ForceWatermarkRatio)
	ratio := usageRatio(usage)
	switch {
	case ratio >= forceRatio:
		return compact.TriggerDecision{ShouldCompact: true, Reason: "context_limit"}
	case ratio >= softRatio:
		return compact.TriggerDecision{ShouldCompact: true, Reason: "context_watermark"}
	default:
		return compact.TriggerDecision{}
	}
}

func evaluateEmergencyWatermark(usage compact.UsageSnapshot, cfg CompactionConfig) bool {
	if usage.EffectiveInputBudget <= 0 {
		return false
	}
	return usageRatio(usage) >= dynamicEmergencyWatermark(usage.ContextWindowTokens, cfg.ForceWatermarkRatio)
}

func usageRatio(usage compact.UsageSnapshot) float64 {
	if usage.EffectiveInputBudget <= 0 {
		return 0
	}
	return float64(usage.TotalTokens) / float64(usage.EffectiveInputBudget)
}

func usageForModelRequest(events []*session.Event, llm model.LLM, req *model.Request, cfg CompactionConfig) (compact.UsageSnapshot, int) {
	usage, requestTokens, _ := usageForModelRequestDetails(events, llm, req, cfg)
	return usage, requestTokens
}

func usageForModelRequestDetails(
	events []*session.Event,
	llm model.LLM,
	req *model.Request,
	cfg CompactionConfig,
) (compact.UsageSnapshot, int, bool) {
	window := resolveContextWindowTokens(llm, cfg.DefaultContextWindowTokens)
	promptEvents := compact.PromptEventsFromLatestCompact(events)
	usage, providerSnapshot, hasProviderSnapshot := snapshotUsageWithResolvedWindowDetails(promptEvents, window, cfg, func(snapshot providerTokenSnapshot) bool {
		return providerSnapshotCompatibleWithLLM(snapshot, llm)
	})
	return usageWithModelRequestEstimateDetails(usage, providerSnapshot, hasProviderSnapshot, req)
}

func usageWithModelRequestEstimate(usage compact.UsageSnapshot, req *model.Request) (compact.UsageSnapshot, int) {
	usage, requestTokens, _ := usageWithModelRequestEstimateDetails(usage, providerTokenSnapshot{}, false, req)
	return usage, requestTokens
}

func usageWithModelRequestEstimateDetails(
	usage compact.UsageSnapshot,
	providerSnapshot providerTokenSnapshot,
	hasProviderSnapshot bool,
	req *model.Request,
) (compact.UsageSnapshot, int, bool) {
	requestTokens := estimateModelRequestTokens(req)
	// Provider prompt usage is authoritative for the request shape already
	// accepted by that provider. When runtime-controlled prefix assembly changes,
	// reconcile only that local delta; messages and attachments remain covered
	// by provider usage plus post-snapshot event estimates.
	if usage.Source == compact.UsageSourceProvider {
		currentPrefix := prefixusage.ForRequest(req)
		previousFingerprint := strings.TrimSpace(providerSnapshot.PromptPrefixFingerprint)
		prefixChanged := hasProviderSnapshot &&
			previousFingerprint != "" &&
			currentPrefix.Fingerprint != "" &&
			previousFingerprint != currentPrefix.Fingerprint
		if prefixChanged {
			usage.TotalTokens = max(
				usage.TotalTokens+currentPrefix.Tokens-providerSnapshot.PromptPrefixTokens,
				0,
			)
		}
		return usage, requestTokens, prefixChanged
	}
	if requestTokens > usage.TotalTokens {
		usage.TotalTokens = requestTokens
		if usage.Source == "" {
			usage.Source = compact.UsageSourceEstimated
		}
	}
	return usage, requestTokens, false
}

func estimateModelRequestTokens(req *model.Request) int {
	if req == nil {
		return 0
	}
	total := 0
	for _, part := range req.Instructions {
		total += estimatePartTokens(part)
	}
	for _, message := range req.Messages {
		total += estimateMessageTokens(message)
	}
	for _, spec := range req.Tools {
		total += estimateToolSpecTokens(spec)
	}
	if req.Output != nil {
		total += estimateOutputSpecTokens(req.Output)
	}
	return max(total, 0)
}

func estimatePartTokens(part model.Part) int {
	total := 0
	if part.Text != nil {
		total += estimateTextTokens(part.Text.Text)
	}
	if part.Reasoning != nil && part.Reasoning.VisibleText != nil {
		total += estimateTextTokens(*part.Reasoning.VisibleText)
	}
	if part.ToolUse != nil {
		total += estimateTextTokens(part.ToolUse.Name)
		total += estimateTextTokens(string(part.ToolUse.Input))
	}
	if part.ToolResult != nil {
		total += estimateTextTokens(part.ToolResult.Name)
		for _, nested := range part.ToolResult.Content {
			total += estimatePartTokens(nested)
		}
	}
	if part.Media != nil {
		total += estimateMediaPartTokens(part.Media)
	}
	if part.JSON != nil {
		total += estimateTextTokens(string(part.JSON.Value))
	}
	if part.FileRef != nil {
		total += estimateTextTokens(part.FileRef.Name)
		total += estimateTextTokens(part.FileRef.MimeType)
		total += estimateTextTokens(part.FileRef.URI)
		total += estimateTextTokens(part.FileRef.FileID)
		total += estimateTextTokens(part.FileRef.LocalRef)
	}
	if total > 0 {
		return total
	}
	raw, err := json.Marshal(part)
	if err != nil {
		return 0
	}
	return estimateTextTokens(string(raw))
}

const (
	estimatedImageMediaTokens = prefixusage.EstimatedImageMediaTokens
	estimatedOtherMediaTokens = prefixusage.EstimatedOtherMediaTokens
)

func estimateMediaPartTokens(media *model.MediaPart) int {
	if media == nil {
		return 0
	}
	total := estimateTextTokens(string(media.Modality)) +
		estimateTextTokens(media.MimeType) +
		estimateTextTokens(media.Name) +
		estimateTextTokens(media.Source.URI) +
		estimateTextTokens(media.Source.FileID) +
		estimateTextTokens(media.Source.LocalRef)
	switch media.Modality {
	case model.MediaModalityImage:
		total += estimatedImageMediaTokens
	default:
		total += estimatedOtherMediaTokens
	}
	return total
}

func estimateToolSpecTokens(spec model.ToolSpec) int {
	total := estimateTextTokens(string(spec.Kind))
	if spec.Function != nil {
		total += estimateTextTokens(spec.Function.Name)
		total += estimateTextTokens(spec.Function.Description)
		total += estimateAnyTokens(spec.Function.Parameters)
	}
	if spec.ProviderDefined != nil {
		total += estimateTextTokens(spec.ProviderDefined.Name)
		total += estimateTextTokens(spec.ProviderDefined.Provider)
		total += estimateRawMessageMapTokens(spec.ProviderDefined.ProviderDetails)
	}
	if spec.ProviderExecuted != nil {
		total += estimateTextTokens(spec.ProviderExecuted.Name)
		total += estimateTextTokens(spec.ProviderExecuted.Provider)
		total += estimateRawMessageMapTokens(spec.ProviderExecuted.ProviderDetails)
	}
	if spec.MCP != nil {
		total += estimateTextTokens(spec.MCP.Name)
		total += estimateTextTokens(spec.MCP.Server)
		total += estimateTextTokens(spec.MCP.Tool)
	}
	if total > 0 {
		return total
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return 0
	}
	return estimateTextTokens(string(raw))
}

func estimateOutputSpecTokens(spec *model.OutputSpec) int {
	if spec == nil {
		return 0
	}
	total := estimateTextTokens(string(spec.Mode))
	total += estimateAnyTokens(spec.JSONSchema)
	if spec.MaxOutputTokens > 0 {
		total += estimateTextTokens(fmt.Sprintf("%d", spec.MaxOutputTokens))
	}
	if total > 0 {
		return total
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return 0
	}
	return estimateTextTokens(string(raw))
}

func estimateAnyTokens(value any) int {
	if value == nil {
		return 0
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return estimateTextTokens(string(raw))
}

func estimateRawMessageMapTokens(values map[string]json.RawMessage) int {
	total := 0
	for key, value := range values {
		total += estimateTextTokens(key)
		total += estimateTextTokens(string(value))
	}
	return total
}

func lastEventID(events []*session.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if id := strings.TrimSpace(events[i].ID); id != "" {
			return id
		}
	}
	return ""
}

func compactText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 12 {
		return text[:limit]
	}
	head := limit / 2
	tail := limit - head - 3
	if tail < 0 {
		tail = 0
	}
	return strings.TrimSpace(text[:head]) + "..." + strings.TrimSpace(text[len(text)-tail:])
}

func stringifyAny(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		raw, _ := json.Marshal(value)
		return string(raw)
	}
}

func estimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	tokens := len([]rune(text)) / 4
	if len([]rune(text))%4 != 0 {
		tokens++
	}
	return max(tokens, 1)
}

func intFromAny(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		v, err := typed.Int64()
		if err == nil {
			return int(v), true
		}
	}
	return 0, false
}

func isCompactionOverflowError(err error) bool {
	return model.IsContextOverflow(err)
}
