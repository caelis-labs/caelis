package tuiapp

import (
	"os"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/internal/evalharness"
)

// TestRegressionBenchThresholds runs real benchmarks and checks them against
// configured upper bounds. Gated behind CAELIS_BENCH_REGRESSION=1 so that
// ordinary `go test ./...` does not pay the benchmark cost.
func TestRegressionBenchThresholds(t *testing.T) {
	if os.Getenv("CAELIS_BENCH_REGRESSION") == "" {
		t.Skip("skipping bench threshold gate; set CAELIS_BENCH_REGRESSION=1 to enable")
	}

	type benchCase struct {
		name string
		fn   func(b *testing.B)
	}
	cases := []benchCase{
		{
			name: "ViewportSyncLongTranscript",
			fn: func(b *testing.B) {
				m := newPerfTestModel()
				seedLongTranscript(m, 2000)
				for i := 0; i < b.N; i++ {
					m.syncViewportContent()
				}
			},
		},
		{
			name: "AssistantTailIncrementalSync",
			fn: func(b *testing.B) {
				m := newPerfTestModel()
				seedLongTranscript(m, 2000)
				_, _ = m.handleStreamBlock("answer", "assistant", "hello", false)
				_, _ = m.Update(perfTickAt(frameTickViewportSync, time.Now()))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_, _ = m.handleStreamBlock("answer", "assistant", " x", false)
					_, _ = m.Update(perfTickAt(frameTickViewportSync, time.Now()))
				}
			},
		},
		{
			name: "ToolOutputStream10kChunks",
			fn: func(b *testing.B) {
				m := newPerfTestModel()
				block := m.ensureMainACPTurnBlock("session-1")
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					block.UpdateToolWithMeta("call-1", "RUN_COMMAND", "go test", "line\n", false, false, ToolUpdateMeta{TaskID: "task-1"})
					m.markViewportBlockDirty(block.BlockID())
					m.syncViewportContent()
				}
			},
		},
	}

	thresholds := evalharness.DefaultBenchThresholds()
	thresholdMap := make(map[string]evalharness.BenchThreshold, len(thresholds))
	for _, th := range thresholds {
		thresholdMap[th.Name] = th
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			th, hasThreshold := thresholdMap[tc.name]
			if !hasThreshold {
				t.Skipf("no threshold configured for %s", tc.name)
			}

			result := testing.Benchmark(func(b *testing.B) {
				tc.fn(b)
			})

			nsPerOp := result.NsPerOp()
			allocsPerOp := result.AllocsPerOp()
			bytesPerOp := result.AllocedBytesPerOp()

			t.Logf("%s: %d ns/op, %d allocs/op, %d bytes/op", tc.name, nsPerOp, allocsPerOp, bytesPerOp)

			benchResult := evalharness.BenchResult{
				Name:        tc.name,
				NsPerOp:     nsPerOp,
				AllocsPerOp: allocsPerOp,
				BytesPerOp:  bytesPerOp,
			}
			failures := evalharness.CheckBenchThresholds(
				[]evalharness.BenchResult{benchResult},
				[]evalharness.BenchThreshold{th},
			)
			for _, f := range failures {
				t.Error(f.String())
			}
		})
	}
}

// TestRegressionBenchResultSerialization is a fast unit test that verifies the
// JSON round-trip of bench results. No benchmarks are executed.
func TestRegressionBenchResultSerialization(t *testing.T) {
	results := []evalharness.BenchResult{
		{Name: "ViewportSyncLongTranscript", NsPerOp: 1000, AllocsPerOp: 5, BytesPerOp: 512},
		{Name: "AssistantTailIncrementalSync", NsPerOp: 500, AllocsPerOp: 2, BytesPerOp: 128},
	}

	path := t.TempDir() + "/bench-results.json"
	if err := evalharness.SaveBenchResults(path, results); err != nil {
		t.Fatalf("SaveBenchResults() error = %v", err)
	}

	loaded, err := evalharness.LoadBenchResults(path)
	if err != nil {
		t.Fatalf("LoadBenchResults() error = %v", err)
	}

	if len(loaded) != len(results) {
		t.Fatalf("loaded %d results, want %d", len(loaded), len(results))
	}
	for i := range results {
		if loaded[i] != results[i] {
			t.Fatalf("result[%d] mismatch: got %v, want %v", i, loaded[i], results[i])
		}
	}
}

// TestRegressionBenchThresholdCheck verifies the pure CheckBenchThresholds
// function without using &testing.T{} as a mock.
func TestRegressionBenchThresholdCheck(t *testing.T) {
	results := []evalharness.BenchResult{
		{Name: "Fast", NsPerOp: 100, AllocsPerOp: 1, BytesPerOp: 64},
		{Name: "Slow", NsPerOp: 1_000_000, AllocsPerOp: 500, BytesPerOp: 5_000_000},
	}

	t.Run("pass", func(t *testing.T) {
		failures := evalharness.CheckBenchThresholds(results, []evalharness.BenchThreshold{
			{Name: "Fast", MaxNsOp: 200, MaxAlloc: 10, MaxBPO: 1024},
			{Name: "Slow", MaxNsOp: 2_000_000, MaxAlloc: 1000, MaxBPO: 10_000_000},
		})
		if len(failures) != 0 {
			t.Fatalf("expected 0 failures, got %d: %s", len(failures), evalharness.FailuresSummary(failures))
		}
	})

	t.Run("fail_ns", func(t *testing.T) {
		failures := evalharness.CheckBenchThresholds(results, []evalharness.BenchThreshold{
			{Name: "Fast", MaxNsOp: 50},
		})
		if len(failures) != 1 {
			t.Fatalf("expected 1 failure, got %d", len(failures))
		}
		if failures[0].Metric != "ns/op" {
			t.Fatalf("failure metric = %q, want ns/op", failures[0].Metric)
		}
		if failures[0].Actual != 100 {
			t.Fatalf("failure actual = %d, want 100", failures[0].Actual)
		}
		if failures[0].Threshold != 50 {
			t.Fatalf("failure threshold = %d, want 50", failures[0].Threshold)
		}
	})

	t.Run("fail_allocs", func(t *testing.T) {
		failures := evalharness.CheckBenchThresholds(results, []evalharness.BenchThreshold{
			{Name: "Slow", MaxAlloc: 100},
		})
		if len(failures) != 1 {
			t.Fatalf("expected 1 failure, got %d", len(failures))
		}
		if failures[0].Metric != "allocs/op" {
			t.Fatalf("failure metric = %q, want allocs/op", failures[0].Metric)
		}
		if failures[0].Actual != 500 {
			t.Fatalf("failure actual = %d, want 500", failures[0].Actual)
		}
	})

	t.Run("fail_bytes", func(t *testing.T) {
		failures := evalharness.CheckBenchThresholds(results, []evalharness.BenchThreshold{
			{Name: "Slow", MaxBPO: 1_000_000},
		})
		if len(failures) != 1 {
			t.Fatalf("expected 1 failure, got %d", len(failures))
		}
		if failures[0].Metric != "bytes/op" {
			t.Fatalf("failure metric = %q, want bytes/op", failures[0].Metric)
		}
	})

	t.Run("multi_metric_fail", func(t *testing.T) {
		failures := evalharness.CheckBenchThresholds(results, []evalharness.BenchThreshold{
			{Name: "Slow", MaxNsOp: 100, MaxAlloc: 10, MaxBPO: 100},
		})
		if len(failures) != 3 {
			t.Fatalf("expected 3 failures, got %d: %s", len(failures), evalharness.FailuresSummary(failures))
		}
	})

	t.Run("no_matching_threshold", func(t *testing.T) {
		failures := evalharness.CheckBenchThresholds(results, []evalharness.BenchThreshold{
			{Name: "Unknown", MaxNsOp: 1},
		})
		if len(failures) != 0 {
			t.Fatalf("expected 0 failures for unmatched threshold, got %d", len(failures))
		}
	})
}
