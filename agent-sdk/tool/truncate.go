package tool

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
	"github.com/caelis-labs/caelis/agent-sdk/model"
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
	out.Meta = jsonvalue.CloneMap(result.Meta)
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

// ResultNeedsTruncation reports whether result exceeds the model-visible
// content budget. It does not allocate or construct the truncated result.
func ResultNeedsTruncation(result Result, policy TruncationPolicy) bool {
	if policy.tokenBudget() <= 0 {
		policy = DefaultTruncationPolicy()
	}
	return estimateTokensForParts(result.Content) > policy.tokenBudget()
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
	totalTokens := estimateTokensForJSONValue(parsed)
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
	totalTokens := estimateTokensForJSONValue(input)
	info.EstimatedTokens = totalTokens
	info.EstimatedBytes = totalTokens * approxBytesPerToken
	if totalTokens <= budgetTokens {
		return cloneMapValue(input), info
	}

	out, state := truncateMapToJSONBudget(input, budgetTokens)
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
	keepBudget := budgetBytes
	var out string
	var removedTokens int
	for {
		out, removedTokens = truncateStringWithKeepBudget(s, policy, keepBudget)
		if estimateTextTokens(out) <= policy.tokenBudget() || keepBudget <= 0 {
			return out, removedTokens
		}
		overBytes := max((estimateTextTokens(out)-policy.tokenBudget())*approxBytesPerToken, 1)
		keepBudget = max(keepBudget-overBytes, 0)
	}
}

// TruncateText truncates text and includes total line count when useful.
func TruncateText(s string, policy TruncationPolicy) (string, int) {
	if prefix, body, totalLines, ok := splitExistingTotalOutputHeader(s); ok {
		return truncateTextWithLinePrefix(s, prefix, body, totalLines, policy)
	}

	if estimateTextTokens(s) <= policy.tokenBudget() || policy.tokenBudget() <= 0 {
		return s, 0
	}
	if strings.Contains(s, "\n") {
		lines := strings.Count(s, "\n") + 1
		prefix := fmt.Sprintf("Total output lines: %d\n\n", lines)
		return truncateTextWithLinePrefix(s, prefix, s, lines, policy)
	}
	return TruncateString(s, policy)
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
		cost := estimateTokensForJSONString(v)
		if cost <= *remaining {
			*remaining -= cost
			return v
		}
		if truncated, ok := truncateJSONText(v, *remaining, state); ok {
			if cost := estimateTokensForJSONString(truncated); cost <= *remaining {
				*remaining -= cost
				return truncated
			}
		}
		truncated, ok := truncateTextForJSONBudget(v, *remaining)
		if !ok {
			if state != nil {
				state.omitted++
			}
			return nil
		}
		*remaining = max(*remaining-estimateTokensForJSONString(truncated), 0)
		return truncated
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			leftCost := estimateTokensForJSONValue(v[keys[i]])
			rightCost := estimateTokensForJSONValue(v[keys[j]])
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
			keyCost := estimateTokensForJSONString(key)
			if keyCost >= *remaining {
				if state != nil {
					state.omitted++
				}
				continue
			}
			*remaining -= keyCost
			valueCost := estimateTokensForJSONValue(v[key])
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
		cost := estimateTokensForJSONValue(value)
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
	if estimateTokensForJSONString(result) > remaining {
		truncated, ok := truncateTextForJSONBudget(result, remaining)
		if !ok {
			return "", false
		}
		result = truncated
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

func estimateTokensForJSONValue(value any) int {
	data, err := json.Marshal(value)
	if err != nil {
		return estimateTokensForValue(value)
	}
	return estimateTextTokens(string(data))
}

func estimateTokensForJSONString(value string) int {
	data, err := json.Marshal(value)
	if err != nil {
		return estimateTextTokens(value)
	}
	return estimateTextTokens(string(data))
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

func truncateStringWithKeepBudget(s string, policy TruncationPolicy, keepBudget int) (string, int) {
	keepBudget = max(keepBudget, 0)
	leftBudget := keepBudget / 2
	rightBudget := keepBudget - leftBudget
	prefixEnd, suffixStart := splitUTF8Bounds(s, leftBudget, rightBudget)
	left := s[:prefixEnd]
	right := s[suffixStart:]
	removedBytes := len(s) - (len(left) + len(right))
	removedTokens := approxTokensFromBytes(removedBytes)
	marker := formatTruncationMarker(policy, removedTokens, removedBytes)
	return left + marker + right, removedTokens
}

func truncateTextForJSONBudget(s string, budgetTokens int) (string, bool) {
	if budgetTokens <= 0 {
		return "", false
	}
	if estimateTokensForJSONString(s) <= budgetTokens {
		return s, true
	}
	// Avoid binary search over full TruncateText passes on large multiline payloads:
	// each pass is O(lines) and repeated probes can exceed interactive timeouts.
	textBudget := max(budgetTokens*3/4, 1)
	out, _ := TruncateText(s, TruncationPolicy{MaxTokens: textBudget})
	for attempt := 0; attempt < 5 && estimateTokensForJSONString(out) > budgetTokens; attempt++ {
		over := estimateTokensForJSONString(out) - budgetTokens
		textBudget = max(textBudget-over, textBudget*7/10, 1)
		out, _ = TruncateText(s, TruncationPolicy{MaxTokens: textBudget})
	}
	return out, estimateTokensForJSONString(out) <= budgetTokens
}

func truncateTextWithLinePrefix(original string, prefix string, body string, totalLines int, policy TruncationPolicy) (string, int) {
	prefixCost := estimateTextTokens(prefix)
	if prefixCost >= policy.tokenBudget() && policy.tokenBudget() > 0 {
		return TruncateString(original, policy)
	}
	bodyPolicy := subTruncationPolicy(policy, prefixCost, len(prefix))
	truncatedBody, removed := truncateLineUnits(body, totalLines, bodyPolicy)
	if removed == 0 {
		return prefix + truncatedBody, removed
	}
	return prefix + truncatedBody, removed
}

func truncateLineUnits(s string, totalLines int, policy TruncationPolicy) (string, int) {
	budgetTokens := policy.tokenBudget()
	if budgetTokens <= 0 || estimateTextTokens(s) <= budgetTokens {
		return s, 0
	}
	if !strings.Contains(s, "\n") {
		return TruncateString(s, policy)
	}
	lines := strings.Split(s, "\n")
	lineBudget := max(min(budgetTokens/3, 1024), 8)
	lineTok := make([]int, len(lines))
	for i, line := range lines {
		lineTok[i] = estimateTextTokens(truncateLineUnit(line, lineBudget))
	}
	head, tail := 0, 0
	best := buildLineTruncation(lines, head, tail, lineBudget)
	for {
		progressed := false
		if head+tail < len(lines) {
			if estimateLineTruncationTokens(lineTok, head+1, tail) <= budgetTokens {
				head++
				progressed = true
			}
		}
		if head+tail < len(lines) {
			if estimateLineTruncationTokens(lineTok, head, tail+1) <= budgetTokens {
				tail++
				progressed = true
			}
		}
		if !progressed {
			break
		}
		best = buildLineTruncation(lines, head, tail, lineBudget)
	}
	for estimateTextTokens(best) > budgetTokens && lineBudget > 1 {
		lineBudget = max(lineBudget*8/10, 1)
		for i, line := range lines {
			lineTok[i] = estimateTextTokens(truncateLineUnit(line, lineBudget))
		}
		best = buildLineTruncation(lines, head, tail, lineBudget)
	}
	if estimateTextTokens(best) > budgetTokens {
		return TruncateString(s, policy)
	}
	removed := max(estimateTextTokens(s)-estimateTextTokens(best), 1)
	return best, removed
}

func estimateLineTruncationTokens(lineTok []int, headCount, tailCount int) int {
	if len(lineTok) == 0 || headCount+tailCount == 0 {
		return 0
	}
	if headCount+tailCount >= len(lineTok) {
		sum := 0
		for _, cost := range lineTok {
			sum += cost
		}
		if len(lineTok) > 1 {
			sum += approxTokensFromBytes(len(lineTok) - 1)
		}
		return sum
	}
	sum := 0
	for i := 0; i < headCount && i < len(lineTok); i++ {
		sum += lineTok[i]
	}
	tailStart := max(len(lineTok)-tailCount, headCount)
	for i := tailStart; i < len(lineTok); i++ {
		sum += lineTok[i]
	}
	omitted := len(lineTok) - headCount - tailCount
	parts := headCount + tailCount + 1
	if parts > 1 {
		sum += approxTokensFromBytes(parts - 1)
	}
	if omitted > 0 {
		sum += estimateTextTokens(fmt.Sprintf("...%d lines omitted...", omitted))
	}
	return sum
}

func buildLineTruncation(lines []string, headCount int, tailCount int, lineBudget int) string {
	if len(lines) == 0 {
		return ""
	}
	if headCount+tailCount >= len(lines) {
		parts := make([]string, 0, len(lines))
		for _, line := range lines {
			parts = append(parts, truncateLineUnit(line, lineBudget))
		}
		return strings.Join(parts, "\n")
	}
	parts := make([]string, 0, headCount+tailCount+1)
	for i := 0; i < headCount && i < len(lines); i++ {
		parts = append(parts, truncateLineUnit(lines[i], lineBudget))
	}
	omitted := len(lines) - headCount - tailCount
	if omitted > 0 {
		parts = append(parts, fmt.Sprintf("...%d lines omitted...", omitted))
	}
	tailStart := max(len(lines)-tailCount, headCount)
	for i := tailStart; i < len(lines); i++ {
		parts = append(parts, truncateLineUnit(lines[i], lineBudget))
	}
	return strings.Join(parts, "\n")
}

func truncateLineUnit(line string, budgetTokens int) string {
	if budgetTokens <= 0 || estimateTextTokens(line) <= budgetTokens {
		return line
	}
	out, _ := TruncateString(line, TruncationPolicy{MaxTokens: budgetTokens})
	return out
}

func truncateMapToJSONBudget(input map[string]any, budgetTokens int) (map[string]any, *truncationState) {
	body, protected := splitProtectedJSONFields(input, budgetTokens)
	target := budgetTokens
	if len(protected) > 0 {
		target = max(budgetTokens-estimateTokensForJSONValue(protected), 1)
	}
	var last map[string]any
	var lastState *truncationState
	for attempt := 0; attempt < 8; attempt++ {
		remaining := target
		state := &truncationState{}
		truncated := truncateValue(body, &remaining, state)
		out, _ := truncated.(map[string]any)
		if out == nil {
			out = map[string]any{}
		}
		mergeProtectedJSONFields(out, protected)
		last = out
		lastState = state
		if estimateTokensForJSONValue(out) <= budgetTokens {
			return out, state
		}
		over := estimateTokensForJSONValue(out) - budgetTokens
		target = min(max(target-over-8, 1), max(target*9/10, 1))
	}
	if last == nil {
		last = map[string]any{}
	}
	if lastState == nil {
		lastState = &truncationState{}
	}
	if estimateTokensForJSONValue(last) > budgetTokens {
		lastState.omitted++
		fallback := compactJSONMapFallback(body, target)
		mergeProtectedJSONFields(fallback, protected)
		if estimateTokensForJSONValue(fallback) <= budgetTokens {
			return fallback, lastState
		}
		return cloneMapValue(protected), lastState
	}
	return last, lastState
}

func splitProtectedJSONFields(input map[string]any, budgetTokens int) (map[string]any, map[string]any) {
	body := cloneMapValue(input)
	hint, _ := body["system_hint"].(string)
	if strings.TrimSpace(hint) == "" {
		return body, nil
	}
	protected := map[string]any{"system_hint": hint}
	if estimateTokensForJSONValue(protected) >= budgetTokens {
		return body, nil
	}
	delete(body, "system_hint")
	return body, protected
}

func mergeProtectedJSONFields(target map[string]any, protected map[string]any) {
	for key, value := range protected {
		target[key] = cloneTruncationValue(value)
	}
}

func compactJSONMapFallback(input map[string]any, budgetTokens int) map[string]any {
	if budgetTokens <= 0 {
		return map[string]any{}
	}
	emptyCost := estimateTokensForJSONValue(map[string]any{"result": ""})
	if emptyCost >= budgetTokens {
		return map[string]any{}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		raw = []byte(fmt.Sprint(input))
	}
	text, ok := truncateTextForJSONBudget(string(raw), budgetTokens-emptyCost)
	if !ok {
		return map[string]any{"result": ""}
	}
	out := map[string]any{"result": text}
	for estimateTokensForJSONValue(out) > budgetTokens && text != "" {
		nextBudget := max(estimateTokensForJSONString(text)-1, 0)
		next, ok := truncateTextForJSONBudget(text, nextBudget)
		if !ok || next == text {
			return map[string]any{"result": ""}
		}
		text = next
		out["result"] = text
	}
	if estimateTokensForJSONValue(out) > budgetTokens {
		return map[string]any{}
	}
	return out
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
