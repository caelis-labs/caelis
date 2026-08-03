package tuiapp

import "strings"

func streamSmoothingKey(targetKind string, sessionKey string, streamKind string, actor string) string {
	return strings.TrimSpace(targetKind) + "|" + strings.TrimSpace(sessionKey) + "|" + strings.TrimSpace(streamKind) + "|" + strings.TrimSpace(actor)
}

func chooseRevealClusterCount(clusters []string, desired int, maxPerFrame int) int {
	if len(clusters) == 0 || desired <= 0 {
		return 0
	}
	if maxPerFrame <= 0 || maxPerFrame > len(clusters) {
		maxPerFrame = len(clusters)
	}
	revealLimit := firstLogicalLineClusterLimit(clusters, maxPerFrame)
	if revealLimit <= 0 {
		revealLimit = maxPerFrame
	}
	if desired > len(clusters) {
		desired = len(clusters)
	}
	if desired > revealLimit {
		desired = revealLimit
	}
	minStable := minStableRevealCount(clusters, revealLimit)
	best := 0
	for idx := 1; idx <= desired; idx++ {
		if idx < minStable && idx < len(clusters) {
			continue
		}
		next := ""
		if idx < len(clusters) {
			next = clusters[idx]
		}
		if isNaturalRevealBoundary(clusters[idx-1], next) {
			best = idx
		}
	}
	if best > 0 {
		return best
	}
	lookaheadLimit := min(revealLimit, len(clusters))
	for idx := desired + 1; idx <= lookaheadLimit && idx <= desired+4; idx++ {
		if idx < minStable && idx < len(clusters) {
			continue
		}
		next := ""
		if idx < len(clusters) {
			next = clusters[idx]
		}
		if isNaturalRevealBoundary(clusters[idx-1], next) {
			return idx
		}
	}
	if desired < minStable && minStable <= lookaheadLimit {
		return minStable
	}
	return desired
}

func firstLogicalLineClusterLimit(clusters []string, limit int) int {
	if len(clusters) == 0 || limit <= 0 {
		return 0
	}
	if limit > len(clusters) {
		limit = len(clusters)
	}
	for idx := 1; idx <= limit; idx++ {
		if strings.Contains(clusters[idx-1], "\n") {
			return idx
		}
	}
	return limit
}

func minStableRevealCount(clusters []string, limit int) int {
	if len(clusters) == 0 || limit <= 0 {
		return 0
	}
	if limit > len(clusters) {
		limit = len(clusters)
	}
	const minStableClusters = 2
	const minStableColumns = 6

	columns := 0
	for idx := 1; idx <= limit; idx++ {
		columns += graphemeWidth(clusters[idx-1])
		if idx >= minStableClusters && columns >= minStableColumns {
			return idx
		}
	}
	return limit
}
