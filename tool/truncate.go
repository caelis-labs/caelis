package tool

import (
	"fmt"
	"strings"
)

// TruncationPolicy defines limits for tool result truncation.
type TruncationPolicy struct {
	MaxTokens int
	MaxBytes  int
}

// DefaultTruncationPolicy returns the default truncation limits.
func DefaultTruncationPolicy() TruncationPolicy {
	return TruncationPolicy{MaxTokens: 10000, MaxBytes: 40000}
}

// TruncationInfo records what was truncated.
type TruncationInfo struct {
	Truncated      bool
	OriginalTokens int
	RemovedTokens  int
	OriginalBytes  int
	RemovedBytes   int
	Strategy       string
}

// TruncateResult truncates a tool result's Output to fit within the policy.
// Returns the (possibly truncated) result and truncation info.
func TruncateResult(r Result, policy TruncationPolicy) (Result, TruncationInfo) {
	if policy.MaxBytes <= 0 && policy.MaxTokens <= 0 {
		return r, TruncationInfo{}
	}

	output := r.Output
	origLen := len(output)

	// Estimate tokens: ~4 chars per token.
	limit := policy.MaxBytes
	if policy.MaxTokens > 0 {
		tokenLimit := policy.MaxTokens * 4
		if limit <= 0 || tokenLimit < limit {
			limit = tokenLimit
		}
	}

	if limit <= 0 || origLen <= limit {
		return r, TruncationInfo{Truncated: false, OriginalBytes: origLen}
	}

	// Truncate from the middle: keep head and tail.
	headSize := limit * 2 / 3
	tailSize := limit - headSize - 50 // room for the truncation marker

	truncated := output[:headSize] +
		fmt.Sprintf("\n\n... [%d bytes truncated] ...\n\n", origLen-headSize-tailSize) +
		output[origLen-tailSize:]

	cp := r
	cp.Output = truncated
	cp.Truncated = true
	if cp.Metadata == nil {
		cp.Metadata = make(map[string]any)
	}
	cp.Metadata["truncation"] = TruncationInfo{
		Truncated:      true,
		OriginalBytes:  origLen,
		RemovedBytes:   origLen - len(truncated),
		OriginalTokens: origLen / 4,
		RemovedTokens:  (origLen - len(truncated)) / 4,
		Strategy:       "middle",
	}

	return cp, cp.Metadata["truncation"].(TruncationInfo)
}

// TruncateText truncates a text string to fit within maxBytes.
// Uses middle truncation.
func TruncateText(text string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text, false
	}
	head := maxBytes * 2 / 3
	tail := maxBytes - head - 40
	if tail < 0 {
		tail = 0
	}
	return text[:head] + fmt.Sprintf("\n... [%d bytes truncated] ...\n", len(text)-head-tail) + text[len(text)-tail:], true
}

// EstimateTokens returns a rough token count estimate for the given text.
func EstimateTokens(text string) int {
	// Rough: 1 token ≈ 4 chars for English, less for CJK.
	return (len(text) + 3) / 4
}

// FormatTruncationError returns a user-readable truncation summary.
func FormatTruncationInfo(info TruncationInfo) string {
	if !info.Truncated {
		return ""
	}
	return fmt.Sprintf("Output truncated: removed ~%d tokens (~%d bytes) of %d total",
		info.RemovedTokens, info.RemovedBytes, info.OriginalBytes)
}

// String returns a compact summary.
func (info TruncationInfo) String() string {
	if !info.Truncated {
		return "no truncation"
	}
	return fmt.Sprintf("truncated %s: %d/%d bytes removed", info.Strategy, info.RemovedBytes, info.OriginalBytes)
}

// Ensure strings is used.
var _ = strings.Join
