package providers

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

const (
	anthropicWebSearchToolName       = "web_search"
	anthropicWebSearchTool20250305   = "web_search_20250305"
	anthropicWebSearchTool20260209   = "web_search_20260209"
	anthropicReplayKindServerToolUse = "server_tool_use"
	anthropicReplayKindWebSearch     = "web_search_tool_result"
	anthropicReplayProviderDetailKey = "block"
)

var anthropicWebSearchMatcher = providerExecutedToolMatcher{
	ProviderAliases: []string{"", "anthropic", "anthropic-compatible", "deepseek"},
	Names: []string{
		anthropicWebSearchToolName,
		"web-search",
		"websearch",
		anthropicWebSearchTool20250305,
		anthropicWebSearchTool20260209,
	},
	DetailKeys:     []string{"web_search", "webSearch"},
	NestedControls: true,
	IncludeExtra:   true,
}

func anthropicUsesProviderExecutedTools(specs []model.ToolSpec) bool {
	enabled, _, _ := anthropicProviderWebSearchToolConfig(specs)
	return enabled
}

func anthropicProviderExecutedTools(specs []model.ToolSpec) []anthropic.ToolUnionParam {
	enabled, version, extra := anthropicProviderWebSearchToolConfig(specs)
	if !enabled {
		return nil
	}
	return []anthropic.ToolUnionParam{anthropicProviderWebSearchTool(version, extra)}
}

func anthropicProviderWebSearchToolConfig(specs []model.ToolSpec) (bool, string, map[string]json.RawMessage) {
	match := providerExecutedToolMatchFromSpecs(specs, anthropicWebSearchMatcher)
	if !match.Enabled || match.Disabled {
		return false, "", nil
	}
	version := anthropicWebSearchVersionFromDetails(match.Extra)
	for _, spec := range specs {
		if spec.Kind != model.ToolSpecKindProviderExecuted || spec.ProviderExecuted == nil {
			continue
		}
		one := providerExecutedToolMatchFromSpec(spec.ProviderExecuted, anthropicWebSearchMatcher)
		if !one.Enabled || one.Disabled {
			continue
		}
		if v := anthropicWebSearchVersionFromSpec(spec.ProviderExecuted); v != "" {
			version = v
		}
	}
	if version == "" {
		version = anthropicWebSearchTool20260209
	}
	return true, version, match.Extra
}

func anthropicProviderWebSearchTool(version string, extra map[string]json.RawMessage) anthropic.ToolUnionParam {
	switch anthropicNormalizeWebSearchVersion(version) {
	case anthropicWebSearchTool20260209:
		tool := anthropic.WebSearchTool20260209Param{}
		applyAnthropicWebSearch20260209Options(&tool, extra)
		return anthropic.ToolUnionParam{OfWebSearchTool20260209: &tool}
	default:
		tool := anthropic.WebSearchTool20250305Param{}
		applyAnthropicWebSearch20250305Options(&tool, extra)
		return anthropic.ToolUnionParam{OfWebSearchTool20250305: &tool}
	}
}

func applyAnthropicWebSearch20250305Options(tool *anthropic.WebSearchTool20250305Param, extra map[string]json.RawMessage) {
	if tool == nil {
		return
	}
	options := anthropicWebSearchOptionsFromDetails(extra)
	tool.MaxUses = options.maxUses
	tool.AllowedDomains = options.allowedDomains
	tool.BlockedDomains = options.blockedDomains
	tool.UserLocation = options.userLocation
}

func applyAnthropicWebSearch20260209Options(tool *anthropic.WebSearchTool20260209Param, extra map[string]json.RawMessage) {
	if tool == nil {
		return
	}
	options := anthropicWebSearchOptionsFromDetails(extra)
	tool.MaxUses = options.maxUses
	tool.AllowedDomains = options.allowedDomains
	tool.BlockedDomains = options.blockedDomains
	tool.UserLocation = options.userLocation
}

type anthropicWebSearchOptions struct {
	maxUses        param.Opt[int64]
	allowedDomains []string
	blockedDomains []string
	userLocation   anthropic.UserLocationParam
}

func anthropicWebSearchOptionsFromDetails(extra map[string]json.RawMessage) anthropicWebSearchOptions {
	var options anthropicWebSearchOptions
	if len(extra) == 0 {
		return options
	}
	if n, ok := int64ProviderDetail(extra, "max_uses"); ok && n > 0 {
		options.maxUses = anthropic.Int(n)
	}
	if domains := stringSliceProviderDetail(extra, "allowed_domains"); len(domains) > 0 {
		options.allowedDomains = domains
	}
	if domains := stringSliceProviderDetail(extra, "blocked_domains"); len(domains) > 0 {
		options.blockedDomains = domains
	}
	if loc, ok := anthropicUserLocationProviderDetail(extra, "user_location"); ok {
		options.userLocation = loc
	}
	return options
}

func anthropicWebSearchVersionFromSpec(tool *model.ProviderExecutedToolSpec) string {
	if tool == nil {
		return ""
	}
	if version := anthropicNormalizeWebSearchVersion(tool.Name); version != "" {
		return version
	}
	return anthropicWebSearchVersionFromDetails(tool.ProviderDetails)
}

func anthropicWebSearchVersionFromDetails(details map[string]json.RawMessage) string {
	if len(details) == 0 {
		return ""
	}
	for _, key := range []string{"version", "type"} {
		raw := details[key]
		if len(raw) == 0 {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			if version := anthropicNormalizeWebSearchVersion(value); version != "" {
				return version
			}
		}
	}
	return ""
}

func anthropicNormalizeWebSearchVersion(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case anthropicWebSearchTool20250305:
		return anthropicWebSearchTool20250305
	case anthropicWebSearchTool20260209:
		return anthropicWebSearchTool20260209
	default:
		return ""
	}
}

func anthropicReplayContentBlockFromPart(part model.Part) (*anthropic.ContentBlockParamUnion, bool) {
	if part.Kind != model.PartKindReasoning || part.Reasoning == nil || part.Reasoning.Replay == nil {
		return nil, false
	}
	replay := part.Reasoning.Replay
	switch strings.TrimSpace(replay.Kind) {
	case anthropicReplayKindServerToolUse:
		raw := part.Reasoning.ProviderDetails[anthropicReplayProviderDetailKey]
		if len(raw) == 0 {
			return nil, false
		}
		var block anthropic.ServerToolUseBlock
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, false
		}
		param := block.ToParam()
		return &anthropic.ContentBlockParamUnion{OfServerToolUse: &param}, true
	case anthropicReplayKindWebSearch:
		raw := part.Reasoning.ProviderDetails[anthropicReplayProviderDetailKey]
		if len(raw) == 0 {
			return nil, false
		}
		var block anthropic.WebSearchToolResultBlock
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, false
		}
		param := block.ToParam()
		return &anthropic.ContentBlockParamUnion{OfWebSearchToolResult: &param}, true
	default:
		return nil, false
	}
}

func anthropicServerToolReplayPart(provider, kind string, value any) (model.Part, bool) {
	raw, err := anthropicRawJSON(value)
	if err != nil || len(raw) == 0 {
		return model.Part{}, false
	}
	part := model.NewReasoningPart("", model.ReasoningVisibilityTokenOnly)
	if part.Reasoning == nil {
		return model.Part{}, false
	}
	part.Reasoning.Replay = &model.ReplayMeta{
		Provider: provider,
		Kind:     kind,
	}
	part.Reasoning.ProviderDetails = map[string]json.RawMessage{
		anthropicReplayProviderDetailKey: append(json.RawMessage(nil), raw...),
	}
	return part, true
}

func anthropicRawJSON(value any) (json.RawMessage, error) {
	if rawProvider, ok := value.(interface{ RawJSON() string }); ok {
		if raw := strings.TrimSpace(rawProvider.RawJSON()); raw != "" {
			return json.RawMessage(raw), nil
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func anthropicWebSearchResultsFromMessage(msg model.Message, maxResults int) []model.WebSearchResult {
	if maxResults <= 0 {
		maxResults = 5
	}
	results := make([]model.WebSearchResult, 0, maxResults)
	seen := map[string]struct{}{}
	for _, part := range msg.ReasoningParts() {
		if part.Replay == nil || strings.TrimSpace(part.Replay.Kind) != anthropicReplayKindWebSearch {
			continue
		}
		for _, result := range anthropicWebSearchResultsFromRawBlock(part.ProviderDetails[anthropicReplayProviderDetailKey]) {
			if result.URL == "" {
				continue
			}
			if _, ok := seen[result.URL]; ok {
				continue
			}
			seen[result.URL] = struct{}{}
			if strings.TrimSpace(result.RefID) == "" {
				result.RefID = "search-" + strconv.Itoa(len(results))
			}
			results = append(results, result)
			if len(results) >= maxResults {
				return results
			}
		}
	}
	return results
}

func anthropicWebSearchResultsFromRawBlock(raw json.RawMessage) []model.WebSearchResult {
	if len(raw) == 0 {
		return nil
	}
	var block struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &block); err != nil || len(block.Content) == 0 {
		return nil
	}
	content := strings.TrimSpace(string(block.Content))
	if content == "" || content[0] != '[' {
		return nil
	}
	var items []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		PageAge string `json:"page_age"`
	}
	if err := json.Unmarshal(block.Content, &items); err != nil {
		return nil
	}
	results := make([]model.WebSearchResult, 0, len(items))
	for _, item := range items {
		resultURL := strings.TrimSpace(item.URL)
		if resultURL == "" {
			continue
		}
		results = append(results, model.WebSearchResult{
			Title:       strings.TrimSpace(item.Title),
			URL:         resultURL,
			Source:      hostFromURL(resultURL),
			PublishedAt: strings.TrimSpace(item.PageAge),
		})
	}
	return results
}

func hostFromURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return ""
	}
	return strings.TrimSpace(parsed.Hostname())
}

func int64ProviderDetail(details map[string]json.RawMessage, key string) (int64, bool) {
	raw := details[key]
	if len(raw) == 0 {
		return 0, false
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, true
	}
	var floatValue float64
	if err := json.Unmarshal(raw, &floatValue); err == nil {
		return int64(floatValue), true
	}
	return 0, false
}

func stringSliceProviderDetail(details map[string]json.RawMessage, key string) []string {
	raw := details[key]
	if len(raw) == 0 {
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err == nil {
		return compactStrings(values)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return compactStrings([]string{value})
	}
	return nil
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func anthropicUserLocationProviderDetail(details map[string]json.RawMessage, key string) (anthropic.UserLocationParam, bool) {
	raw := details[key]
	if len(raw) == 0 {
		return anthropic.UserLocationParam{}, false
	}
	var input struct {
		City     string `json:"city"`
		Country  string `json:"country"`
		Region   string `json:"region"`
		Timezone string `json:"timezone"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return anthropic.UserLocationParam{}, false
	}
	var out anthropic.UserLocationParam
	if value := strings.TrimSpace(input.City); value != "" {
		out.City = anthropic.String(value)
	}
	if value := strings.TrimSpace(input.Country); value != "" {
		out.Country = anthropic.String(value)
	}
	if value := strings.TrimSpace(input.Region); value != "" {
		out.Region = anthropic.String(value)
	}
	if value := strings.TrimSpace(input.Timezone); value != "" {
		out.Timezone = anthropic.String(value)
	}
	return out, true
}
