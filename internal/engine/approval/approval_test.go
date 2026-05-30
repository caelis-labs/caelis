package approval

import (
	"context"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
)

func TestAskToolsOnlyReviewsSelectedTools(t *testing.T) {
	policy := AskTools("write_file")

	selected, err := policy.ReviewToolCall(context.Background(), Request{
		Call: model.ToolCall{Name: "WRITE_FILE"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Verdict != VerdictAsk {
		t.Fatalf("selected verdict = %q, want ask", selected.Verdict)
	}

	ignored, err := policy.ReviewToolCall(context.Background(), Request{
		Call: model.ToolCall{Name: "run_command"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ignored.Verdict != "" {
		t.Fatalf("ignored verdict = %q, want no review", ignored.Verdict)
	}
}

func TestWithSessionModeManualAsksForEveryTool(t *testing.T) {
	policy := WithSessionMode(AskTools("write_file"))

	manual, err := policy.ReviewToolCall(context.Background(), Request{
		Mode: ModeManual,
		Call: model.ToolCall{Name: "read_file"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if manual.Verdict != VerdictAsk {
		t.Fatalf("manual verdict = %q, want ask", manual.Verdict)
	}

	auto, err := policy.ReviewToolCall(context.Background(), Request{
		Mode: ModeAutoReview,
		Call: model.ToolCall{Name: "read_file"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if auto.Verdict != "" {
		t.Fatalf("auto-review verdict = %q, want base policy to ignore read_file", auto.Verdict)
	}
}
