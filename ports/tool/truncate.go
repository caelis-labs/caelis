package tool

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/OnslaughtSnail/caelis/ports/model"
)

const approxBytesPerToken = 4

// TruncationPolicy defines the model-visible budget for one tool result.
type TruncationPolicy struct {
	MaxTokens int
	MaxBytes  int
}

// DefaultTruncationPolicy returns the default model-visible tool result budget.
func DefaultTruncationPolicy() TruncationPolicy {
	return TruncationPolicy{MaxTokens: 10000}
}

// TokenBudget returns the effective approximate token budget.
func (p TruncationPolicy) TokenBudget() int {
	return p.tokenBudget()
}

// ByteBudget returns the effective byte budget.
func (p TruncationPolicy) ByteBudget() int {
	return p.byteBudget()
}

// TruncationInfo describes truncation applied to one tool result payload.
type TruncationInfo struct {
	Truncated       bool
	Policy          string
	MaxTokens       int
	MaxBytes        int
	EstimatedTokens int
	EstimatedBytes  int
	RemovedTokens   int
	RemovedBytes    int
	OmittedItems    int
}

// TruncateResultWithInfo returns one bounded tool result and the non-model
// metadata describing any truncation that was applied.
func TruncateResultWithInfo(result Result, policy TruncationPolicy) (Result, TruncationInfo) {
	if policy.tokenBudget() <= 0 {
		policy = DefaultTruncationPolicy()
	}
	out, _ := CloneResult(result, nil)
	out.Content = model.CloneParts(result.Content)
	out.Meta = maps.Clone(result.Meta)
	if len(out.Content) == 0 {
		payload := map[string]any{}
		if result.IsError {
			payload["error"] = "tool call failed"
		}
		out.Content = []model.Part{model.NewJSONPart(mustMarshalMap(payload))}
		return out, newTruncationInfo(policy)
	}
	var info TruncationInfo
	out.Content, info = TruncateParts(out.Content, policy)
	return out, info
}

// TruncateParts applies one shared budget to model content items while
// preserving non-text references such as images and files.
func TruncateParts(parts []model.Part, policy TruncationPolicy) ([]model.Part, TruncationInfo) {
	info := newTruncationInfo(policy)
	budgetTokens := policy.tokenBudget()
	if budgetTokens <= 0 {
		return model.CloneParts(parts), info
	}
	totalTokens := estimateTokensForParts(parts)
	info.EstimatedTokens = totalTokens
	info.EstimatedBytes = totalTokens * approxBytesPerToken
	if totalTokens <= budgetTokens {
		return model.CloneParts(parts), info
	}

	remaining := budgetTokens
	out := make([]model.Part, 0, len(parts)+1)
	omitted := 0
	for _, part := range model.CloneParts(parts) {
		switch {
		case part.JSON != nil:
			if remaining <= 0 {
				omitted++
				continue
			}
			raw := part.JSON.Value
			cost := estimateTextTokens(string(raw))
			if cost <= remaining {
				remaining -= cost
				out = append(out, part)
				continue
			}
			truncatedRaw, subInfo := TruncateJSON(raw, TruncationPolicy{MaxTokens: remaining})
			part.JSON.Value = truncatedRaw
			out = append(out, part)
			remaining = 0
			omitted += subInfo.OmittedItems
		case part.Text != nil:
			if remaining <= 0 {
				omitted++
				continue
			}
			cost := estimateTextTokens(part.Text.Text)
			if cost <= remaining {
				remaining -= cost
				out = append(out, part)
				continue
			}
			part.Text.Text, _ = TruncateText(part.Text.Text, TruncationPolicy{MaxTokens: remaining})
			out = append(out, part)
			remaining = 0
		default:
			out = append(out, part)
		}
	}
	if omitted > 0 {
		out = append(out, model.NewTextPart(fmt.Sprintf("[omitted %d tool result content items]", omitted)))
	}
	info.Truncated = true
	info.OmittedItems = omitted
	info.RemovedTokens = max(totalTokens-budgetTokens, 0)
	info.RemovedBytes = info.RemovedTokens * approxBytesPerToken
	return out, info
}

// TruncateJSON applies recursive truncation to one JSON payload.
func TruncateJSON(raw json.RawMessage, policy TruncationPolicy) (json.RawMessage, TruncationInfo) {
	info := newTruncationInfo(policy)
	budgetTokens := policy.tokenBudget()
	if len(raw) == 0 || budgetTokens <= 0 {
		return append(json.RawMessage(nil), raw...), info
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		text, removed := TruncateText(string(raw), policy)
		if removed == 0 {
			return append(json.RawMessage(nil), raw...), info
		}
		info = truncationInfoForText(string(raw), policy)
		return mustMarshalAny(text), info
	}
	if payload, ok := parsed.(map[string]any); ok {
		out, mapInfo := TruncateMap(payload, policy)
		return mustMarshalMap(out), mapInfo
	}
	totalTokens := estimateTokensForValue(parsed)
	info.EstimatedTokens = totalTokens
	info.EstimatedBytes = totalTokens * approxBytesPerToken
	if totalTokens <= budgetTokens {
		return append(json.RawMessage(nil), raw...), info
	}
	remaining := budgetTokens
	state := &truncationState{}
	truncated := truncateValue(parsed, &remaining, state)
	data := mustMarshalAny(truncated)
	if estimateTextTokens(string(data)) > budgetTokens {
		text, _ := TruncateText(string(data), policy)
		data = mustMarshalAny(text)
	}
	info.Truncated = true
	info.OmittedItems = state.omitted
	info.RemovedTokens = max(totalTokens-budgetTokens, 0)
	info.RemovedBytes = info.RemovedTokens * approxBytesPerToken
	return data, info
}

// TruncateMap applies recursive truncation to one JSON-like object.
func TruncateMap(input map[string]any, policy TruncationPolicy) (map[string]any, TruncationInfo) {
	info := newTruncationInfo(policy)
	budgetTokens := policy.tokenBudget()
	if input == nil || budgetTokens <= 0 {
		return cloneMapValue(input), info
	}
	totalTokens := estimateTokensForValue(input)
	info.EstimatedTokens = totalTokens
	info.EstimatedBytes = totalTokens * approxBytesPerToken
	if totalTokens <= budgetTokens {
		return cloneMapValue(input), info
	}

	remaining := budgetTokens
	state := &truncationState{}
	truncated := truncateValue(input, &remaining, state)
	out, _ := truncated.(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	info.Truncated = true
	info.OmittedItems = state.omitted
	info.RemovedTokens = max(totalTokens-budgetTokens, 0)
	info.RemovedBytes = info.RemovedTokens * approxBytesPerToken
	return out, info
}

// TruncationMetadata returns the compact metadata map for a truncation event.
func TruncationMetadata(info TruncationInfo) map[string]any {
	if !info.Truncated {
		return nil
	}
	return compactTruncationMeta(map[string]any{
		"truncated":        info.Truncated,
		"policy":           info.Policy,
		"max_tokens":       info.MaxTokens,
		"max_bytes":        info.MaxBytes,
		"estimated_tokens": info.EstimatedTokens,
		"estimated_bytes":  info.EstimatedBytes,
		"removed_tokens":   info.RemovedTokens,
		"removed_bytes":    info.RemovedBytes,
		"omitted_items":    info.OmittedItems,
	})
}

// TruncateString truncates one string in the middle on UTF-8 boundaries.
func TruncateString(s string, policy TruncationPolicy) (string, int) {
	if s == "" {
		return s, 0
	}
	budgetBytes := policy.byteBudget()
	if budgetBytes <= 0 || len(s) <= budgetBytes {
		return s, 0
	}
	leftBudget := budgetBytes / 2
	rightBudget := budgetBytes - leftBudget
	prefixEnd, suffixStart := splitUTF8Bounds(s, leftBudget, rightBudget)
	left := s[:prefixEnd]
	right := s[suffixStart:]
	removedBytes := len(s) - (len(left) + len(right))
	removedTokens := approxTokensFromBytes(removedBytes)
	marker := formatTruncationMarker(policy, removedTokens, removedBytes)
	return left + marker + right, removedTokens
}

// TruncateText truncates text and includes total line count when useful.
func TruncateText(s string, policy TruncationPolicy) (string, int) {
	if prefix, body, totalLines, ok := splitExistingTotalOutputHeader(s); ok {
		prefixCost := estimateTextTokens(prefix)
		if prefixCost >= policy.tokenBudget() && policy.tokenBudget() > 0 {
			return TruncateString(s, policy)
		}
		bodyPolicy := subTruncationPolicy(policy, prefixCost, len(prefix))
		truncatedBody, removed := TruncateString(body, bodyPolicy)
		if removed == 0 {
			return prefix + truncatedBody, removed
		}
		return fmt.Sprintf("Total output lines: %d\n\n%s", totalLines, truncatedBody), removed
	}

	truncated, removed := TruncateString(s, policy)
	if removed == 0 {
		return truncated, removed
	}
	if strings.Contains(s, "\n") {
		lines := strings.Count(s, "\n") + 1
		truncated = fmt.Sprintf("Total output lines: %d\n\n%s", lines, truncated)
	}
	return truncated, removed
}

type truncationState struct {
	omitted int
}

func truncateValue(value any, remaining *int, state *truncationState) any {
	if remaining == nil || *remaining <= 0 {
		if state != nil {
			state.omitted++
		}
		return nil
	}
	switch v := value.(type) {
	case string:
		cost := estimateTextTokens(v)
		if cost <= *remaining {
			*remaining -= cost
			return v
		}
		if truncated, ok := truncateJSONText(v, *remaining, state); ok {
			*remaining = 0
			return truncated
		}
		truncated, _ := TruncateText(v, TruncationPolicy{MaxTokens: *remaining})
		*remaining = 0
		return truncated
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			leftCost := estimateTokensForValue(v[keys[i]])
			rightCost := estimateTokensForValue(v[keys[j]])
			if leftCost == rightCost {
				return keys[i] < keys[j]
			}
			return leftCost < rightCost
		})
		out := make(map[string]any, len(v))
		for idx, key := range keys {
			if *remaining <= 0 {
				if state != nil {
					state.omitted++
				}
				continue
			}
			keyCost := estimateTextTokens(key)
			if keyCost < *remaining {
				*remaining -= keyCost
			}
			valueCost := estimateTokensForValue(v[key])
			remainingKeys := len(keys) - idx - 1
			valueBudget := max(*remaining-remainingKeys, 1)
			if valueCost > valueBudget {
				subRemaining := valueBudget
				val := truncateValue(v[key], &subRemaining, state)
				*remaining = max(*remaining-(valueBudget-subRemaining), 0)
				if val != nil {
					out[key] = val
				}
				continue
			}
			val := truncateValue(v[key], remaining, state)
			if val == nil {
				continue
			}
			out[key] = val
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			if *remaining <= 0 {
				if state != nil {
					state.omitted++
				}
				continue
			}
			val := truncateValue(item, remaining, state)
			if val == nil {
				continue
			}
			out = append(out, val)
		}
		return out
	default:
		text := fmt.Sprint(value)
		cost := estimateTextTokens(text)
		if cost <= *remaining {
			*remaining -= cost
			return value
		}
		if state != nil {
			state.omitted++
		}
		return nil
	}
}

func truncateJSONText(s string, remaining int, state *truncationState) (string, bool) {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return "", false
	}
	subRemaining := remaining
	truncated := truncateValue(parsed, &subRemaining, state)
	if truncated == nil {
		return "", false
	}
	data, err := json.Marshal(truncated)
	if err != nil {
		return "", false
	}
	result := string(data)
	if estimateTextTokens(result) > remaining {
		result, _ = TruncateText(result, TruncationPolicy{MaxTokens: remaining})
	}
	return result, true
}

func newTruncationInfo(policy TruncationPolicy) TruncationInfo {
	info := TruncationInfo{
		MaxTokens: policy.MaxTokens,
		MaxBytes:  policy.MaxBytes,
	}
	if policy.MaxBytes > 0 && policy.MaxTokens <= 0 {
		info.Policy = "bytes"
	} else {
		info.Policy = "tokens"
	}
	return info
}

func truncationInfoForText(text string, policy TruncationPolicy) TruncationInfo {
	total := estimateTextTokens(text)
	budget := policy.tokenBudget()
	info := newTruncationInfo(policy)
	info.Truncated = true
	info.EstimatedTokens = total
	info.EstimatedBytes = total * approxBytesPerToken
	info.RemovedTokens = max(total-budget, 0)
	info.RemovedBytes = info.RemovedTokens * approxBytesPerToken
	return info
}

func estimateTokensForParts(parts []model.Part) int {
	sum := 0
	for _, part := range parts {
		switch {
		case part.Text != nil:
			sum += estimateTextTokens(part.Text.Text)
		case part.JSON != nil:
			sum += estimateTextTokens(string(part.JSON.Value))
		case part.ToolResult != nil:
			sum += estimateTokensForParts(part.ToolResult.Content)
		}
	}
	return sum
}

func estimateTokensForValue(value any) int {
	switch v := value.(type) {
	case string:
		return estimateTextTokens(v)
	case map[string]any:
		sum := 0
		for key, val := range v {
			sum += estimateTextTokens(key)
			sum += estimateTokensForValue(val)
		}
		return sum
	case []any:
		sum := 0
		for _, item := range v {
			sum += estimateTokensForValue(item)
		}
		return sum
	case json.RawMessage:
		return estimateTextTokens(string(v))
	default:
		return estimateTextTokens(fmt.Sprint(value))
	}
}

func estimateTextTokens(s string) int {
	if s == "" {
		return 0
	}
	bytes := len(s)
	tokens := bytes / approxBytesPerToken
	if bytes%approxBytesPerToken != 0 {
		tokens++
	}
	return tokens
}

func approxTokensFromBytes(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	tokens := bytes / approxBytesPerToken
	if bytes%approxBytesPerToken != 0 {
		tokens++
	}
	return tokens
}

func (p TruncationPolicy) tokenBudget() int {
	if p.MaxTokens > 0 {
		return p.MaxTokens
	}
	if p.MaxBytes > 0 {
		return approxTokensFromBytes(p.MaxBytes)
	}
	return 0
}

func (p TruncationPolicy) byteBudget() int {
	if p.MaxBytes > 0 {
		return p.MaxBytes
	}
	if p.MaxTokens > 0 {
		return p.MaxTokens * approxBytesPerToken
	}
	return 0
}

func splitUTF8Bounds(s string, leftBudget, rightBudget int) (int, int) {
	if leftBudget < 0 {
		leftBudget = 0
	}
	if rightBudget < 0 {
		rightBudget = 0
	}
	length := len(s)
	targetSuffix := max(length-rightBudget, 0)
	prefixEnd := 0
	suffixStart := length
	for idx, r := range s {
		end := idx + utf8.RuneLen(r)
		if end <= leftBudget {
			prefixEnd = end
		}
		if idx >= targetSuffix {
			suffixStart = idx
			break
		}
	}
	if suffixStart < prefixEnd {
		suffixStart = prefixEnd
	}
	return prefixEnd, suffixStart
}

func formatTruncationMarker(policy TruncationPolicy, removedTokens, removedBytes int) string {
	if policy.MaxTokens > 0 {
		if removedTokens <= 0 {
			return "...truncated..."
		}
		return fmt.Sprintf("...%d tokens truncated...", removedTokens)
	}
	if removedBytes <= 0 {
		return "...truncated..."
	}
	return fmt.Sprintf("...%d chars truncated...", removedBytes)
}

func splitExistingTotalOutputHeader(s string) (string, string, int, bool) {
	if !strings.HasPrefix(s, "Total output lines: ") {
		return "", "", 0, false
	}
	header, body, ok := strings.Cut(s, "\n\n")
	if !ok {
		return "", "", 0, false
	}
	countText := strings.TrimSpace(strings.TrimPrefix(header, "Total output lines: "))
	totalLines, err := strconv.Atoi(countText)
	if err != nil || totalLines <= 0 {
		return "", "", 0, false
	}
	return header + "\n\n", body, totalLines, true
}

func subTruncationPolicy(policy TruncationPolicy, usedTokens int, usedBytes int) TruncationPolicy {
	out := policy
	if out.MaxTokens > 0 {
		out.MaxTokens = max(out.MaxTokens-usedTokens, 0)
	}
	if out.MaxBytes > 0 {
		out.MaxBytes = max(out.MaxBytes-usedBytes, 0)
	}
	return out
}

func compactTruncationMeta(meta map[string]any) map[string]any {
	out := make(map[string]any, len(meta))
	for key, value := range meta {
		switch typed := value.(type) {
		case int:
			if typed == 0 && key != "max_tokens" && key != "max_bytes" {
				continue
			}
		case bool:
			if !typed && key != "truncated" {
				continue
			}
		case string:
			if strings.TrimSpace(typed) == "" {
				continue
			}
		}
		out[key] = value
	}
	return out
}

func cloneMapValue(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = cloneTruncationValue(value)
	}
	return out
}

func cloneTruncationValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMapValue(typed)
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneTruncationValue(item))
		}
		return out
	case json.RawMessage:
		return append(json.RawMessage(nil), typed...)
	default:
		return typed
	}
}

func jsonObject(raw json.RawMessage) (map[string]any, bool) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return nil, false
	}
	return payload, true
}

func mustMarshalMap(value map[string]any) json.RawMessage {
	if value == nil {
		return json.RawMessage(`{}`)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func mustMarshalAny(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return raw
}
