package evalharness

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// BenchThreshold defines an upper bound for one benchmark metric.
type BenchThreshold struct {
	Name     string `json:"name"`
	MaxNsOp  int64  `json:"max_ns_op,omitempty"`
	MaxAlloc int64  `json:"max_allocs,omitempty"`
	MaxBPO   int64  `json:"max_bytes_per_op,omitempty"`
}

// BenchResult captures one benchmark iteration for threshold comparison.
type BenchResult struct {
	Name        string `json:"name"`
	NsPerOp     int64  `json:"ns_per_op"`
	AllocsPerOp int64  `json:"allocs_per_op"`
	BytesPerOp  int64  `json:"bytes_per_op"`
}

// BenchFailure describes one threshold violation.
type BenchFailure struct {
	Name      string
	Metric    string
	Actual    int64
	Threshold int64
}

func (f BenchFailure) String() string {
	return fmt.Sprintf("benchmark %s: %s = %d > threshold %d (%.1fx over)",
		f.Name, f.Metric, f.Actual, f.Threshold,
		float64(f.Actual)/float64(f.Threshold))
}

// CheckBenchThresholds compares benchmark results against configured upper
// bounds and returns all violations. This is a pure function suitable for
// programmatic use without a testing.T.
func CheckBenchThresholds(results []BenchResult, thresholds []BenchThreshold) []BenchFailure {
	thresholdMap := make(map[string]BenchThreshold, len(thresholds))
	for _, th := range thresholds {
		thresholdMap[th.Name] = th
	}
	var failures []BenchFailure
	for _, r := range results {
		th, ok := thresholdMap[r.Name]
		if !ok {
			continue
		}
		if th.MaxNsOp > 0 && r.NsPerOp > th.MaxNsOp {
			failures = append(failures, BenchFailure{Name: r.Name, Metric: "ns/op", Actual: r.NsPerOp, Threshold: th.MaxNsOp})
		}
		if th.MaxAlloc > 0 && r.AllocsPerOp > th.MaxAlloc {
			failures = append(failures, BenchFailure{Name: r.Name, Metric: "allocs/op", Actual: r.AllocsPerOp, Threshold: th.MaxAlloc})
		}
		if th.MaxBPO > 0 && r.BytesPerOp > th.MaxBPO {
			failures = append(failures, BenchFailure{Name: r.Name, Metric: "bytes/op", Actual: r.BytesPerOp, Threshold: th.MaxBPO})
		}
	}
	return failures
}

// AssertBenchThresholds is a convenience wrapper around CheckBenchThresholds
// that reports failures via testing.T.
func AssertBenchThresholds(t *testing.T, results []BenchResult, thresholds []BenchThreshold) {
	t.Helper()
	for _, f := range CheckBenchThresholds(results, thresholds) {
		t.Error(f.String())
	}
}

// FailuresSummary formats a list of bench failures for display.
func FailuresSummary(failures []BenchFailure) string {
	if len(failures) == 0 {
		return ""
	}
	var b strings.Builder
	for i, f := range failures {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(f.String())
	}
	return b.String()
}

// DefaultBenchThresholds returns the PR-gate performance thresholds.
func DefaultBenchThresholds() []BenchThreshold {
	return []BenchThreshold{
		{Name: "ViewportSyncLongTranscript", MaxNsOp: 500_000_000, MaxAlloc: 100000, MaxBPO: 100_000_000},
		{Name: "AssistantTailIncrementalSync", MaxNsOp: 100_000_000, MaxAlloc: 100000, MaxBPO: 50_000_000},
		{Name: "AssistantStablePrefixTailMarkdownStream", MaxNsOp: 100_000_000, MaxAlloc: 100000, MaxBPO: 50_000_000},
		{Name: "ToolOutputStream10kChunks", MaxNsOp: 100_000_000, MaxAlloc: 100000, MaxBPO: 50_000_000},
		{Name: "VisibleSelectionRenderLongTranscript", MaxNsOp: 100_000_000, MaxAlloc: 100000, MaxBPO: 50_000_000},
		{Name: "RenderSchedulerMixedStreams", MaxNsOp: 100_000_000, MaxAlloc: 100000, MaxBPO: 50_000_000},
	}
}

// SaveBenchResults writes benchmark results to a JSON file for CI comparison.
func SaveBenchResults(path string, results []BenchResult) error {
	raw, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("evalharness: marshal bench results: %w", err)
	}
	return os.WriteFile(path, raw, 0o644)
}

// LoadBenchResults reads benchmark results from a JSON file.
func LoadBenchResults(path string) ([]BenchResult, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("evalharness: read bench results: %w", err)
	}
	var results []BenchResult
	if err := json.Unmarshal(raw, &results); err != nil {
		return nil, fmt.Errorf("evalharness: unmarshal bench results: %w", err)
	}
	return results, nil
}
