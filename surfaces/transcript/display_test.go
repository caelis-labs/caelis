package transcript

import (
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestApprovalReviewDisplayPartsParsesFallbackText(t *testing.T) {
	t.Parallel()

	display := ApprovalReviewDisplayParts("", "", "", "⚠ Automatic approval review denied (risk: high, authorization: deny): command writes outside workspace")

	if display.Status != "denied" || display.Risk != "high" || display.Authorization != "deny" || display.Rationale != "command writes outside workspace" {
		t.Fatalf("ApprovalReviewDisplayParts() = %#v, want parsed review fields", display)
	}
}

func TestApprovalReviewTailOutputUsesParsedFallbackFields(t *testing.T) {
	t.Parallel()

	output := ApprovalReviewTailOutput(ApprovalReviewFields{
		Tool:    "RUN_COMMAND",
		Command: "git status",
		Text:    "Automatic approval review approved (risk: low, authorization: allow): safe read-only command",
	})

	want := "Approval review approved RUN_COMMAND git status (risk: low, authorization: allow)\nsafe read-only command\n"
	if output != want {
		t.Fatalf("ApprovalReviewTailOutput() = %q, want %q", output, want)
	}
}

func TestNormalizeToolResultStatusInfersExitCode(t *testing.T) {
	t.Parallel()

	status, isErr := NormalizeToolResultStatus("", map[string]any{"exit_code": 2}, false, "in_progress")

	if status != ToolStatusFailed || !isErr {
		t.Fatalf("NormalizeToolResultStatus() = %q/%v, want failed/true", status, isErr)
	}
}

func TestNormalizeToolStartStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status string
		want   string
	}{
		{name: "empty", status: "", want: ToolStatusRunning},
		{name: "started", status: " started ", want: ToolStatusRunning},
		{name: "custom", status: "in_progress", want: "in_progress"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := NormalizeToolStartStatus(tt.status); got != tt.want {
				t.Fatalf("NormalizeToolStartStatus(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestInferFinalStatusFromRawOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rawOutput map[string]any
		want      string
		wantOK    bool
	}{
		{name: "empty", rawOutput: nil, wantOK: false},
		{name: "state completed", rawOutput: map[string]any{"state": " completed "}, want: ToolStatusCompleted, wantOK: true},
		{name: "state canceled", rawOutput: map[string]any{"state": "canceled"}, want: ToolStatusCancelled, wantOK: true},
		{name: "state timeout", rawOutput: map[string]any{"state": "timed_out"}, want: ToolStatusInterrupted, wantOK: true},
		{name: "exit zero", rawOutput: map[string]any{"exit_code": "0"}, want: ToolStatusCompleted, wantOK: true},
		{name: "exit positive", rawOutput: map[string]any{"exit_code": 2}, want: ToolStatusFailed, wantOK: true},
		{name: "exit negative", rawOutput: map[string]any{"exit_code": -1}, want: ToolStatusCancelled, wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := InferFinalStatusFromRawOutput(tt.rawOutput)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("InferFinalStatusFromRawOutput() = %q/%v, want %q/%v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestStandardToolOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status string
		isErr  bool
		want   string
	}{
		{name: "completed", status: ToolStatusCompleted, want: ToolStatusCompleted},
		{name: "failed status", status: ToolStatusFailed, want: ToolStatusFailed},
		{name: "error flag", status: ToolStatusCompleted, isErr: true, want: ToolStatusFailed},
		{name: "canceled spelling", status: "canceled", want: ToolStatusCancelled},
		{name: "terminated", status: "terminated", want: ToolStatusInterrupted},
		{name: "running", status: ToolStatusRunning, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := StandardToolOutput(tt.status, tt.isErr); got != tt.want {
				t.Fatalf("StandardToolOutput(%q, %v) = %q, want %q", tt.status, tt.isErr, got, tt.want)
			}
		})
	}
}

func TestSuppressToolResultOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		toolName  string
		output    string
		synthetic bool
		isErr     bool
		want      bool
	}{
		{name: "synthetic exploration", toolName: "LIST", synthetic: true, want: true},
		{name: "completed exploration", toolName: "READ", output: " completed ", want: true},
		{name: "errored exploration", toolName: "READ", output: ToolStatusCompleted, isErr: true, want: false},
		{name: "non exploration", toolName: "RUN_COMMAND", synthetic: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := SuppressToolResultOutput(tt.toolName, "", tt.output, tt.synthetic, tt.isErr); got != tt.want {
				t.Fatalf("SuppressToolResultOutput() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTerminalToolOutputTextUsesRuntimeTerminalOutput(t *testing.T) {
	t.Parallel()

	meta := metautil.WithTerminalOutput(acpToolNameMetaForDisplayTest("RUN_COMMAND"), "call-1", "\n")
	output := TerminalToolOutputText(ToolOutputFallbackInput{
		ToolName:  "RUN_COMMAND",
		ToolKind:  "execute",
		Meta:      meta,
		Status:    "in_progress",
		RawOutput: map[string]any{"latest_output": "ignored"},
	})

	if output != "\n" {
		t.Fatalf("TerminalToolOutputText() = %q, want runtime terminal frame newline", output)
	}
}

func TestTerminalTaskStillRunning(t *testing.T) {
	t.Parallel()

	if !TerminalTaskStillRunning(map[string]any{"running": "true"}, nil) {
		t.Fatal("TerminalTaskStillRunning(raw running) = false, want true")
	}
	if !TerminalTaskStillRunning(map[string]any{"state": " running "}, nil) {
		t.Fatal("TerminalTaskStillRunning(raw state) = false, want true")
	}
	meta := map[string]any{
		"caelis": map[string]any{
			"runtime": map[string]any{
				"task": map[string]any{"state": "running"},
			},
		},
	}
	if !TerminalTaskStillRunning(nil, meta) {
		t.Fatal("TerminalTaskStillRunning(meta state) = false, want true")
	}
}

func TestTerminalToolOutputTextUsesRunningTaskCommandObservationWithoutTerminalAnchor(t *testing.T) {
	t.Parallel()

	output := TerminalToolOutputText(ToolOutputFallbackInput{
		ToolName: "TASK",
		Status:   ToolStatusCompleted,
		RawOutput: map[string]any{
			"target_kind":   "command",
			"state":         "running",
			"latest_output": "step 2\n",
		},
	})
	if output != "step 2\n" {
		t.Fatalf("TerminalToolOutputText() = %q, want Task command observation", output)
	}
}

func TestTerminalToolOutputTextPreservesRawWhitespace(t *testing.T) {
	t.Parallel()

	output := TerminalToolOutputText(ToolOutputFallbackInput{
		ToolName:  "RUN_COMMAND",
		ToolKind:  "execute",
		Meta:      metautil.WithTerminalInfo(nil, "call-1"),
		Status:    ToolStatusRunning,
		RawOutput: map[string]any{"latest_output": "\n  still running\n"},
	})
	if output != "\n  still running\n" {
		t.Fatalf("TerminalToolOutputText() = %q, want raw whitespace preserved", output)
	}
}

func TestDelegatedTaskResultTextUsesCanonicalTaskResultWithoutTerminalOutput(t *testing.T) {
	t.Parallel()

	meta := map[string]any{
		"caelis": map[string]any{
			"runtime": map[string]any{
				"task": map[string]any{
					"state":  "completed",
					"result": "child final result",
				},
			},
		},
	}
	for _, tt := range []struct {
		name     string
		toolName string
		status   string
		want     string
	}{
		{name: "spawn final", toolName: "SPAWN", status: ToolStatusCompleted, want: "child final result"},
		{name: "task final", toolName: "TASK", status: ToolStatusCompleted, want: "child final result"},
		{name: "running", toolName: "SPAWN", status: ToolStatusRunning},
		{name: "command", toolName: "RUN_COMMAND", status: ToolStatusCompleted},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := DelegatedTaskResultText(ToolOutputFallbackInput{ToolName: tt.toolName, Status: tt.status, Meta: meta}); got != tt.want {
				t.Fatalf("DelegatedTaskResultText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDelegatedTaskResultTextPrefersDurableRawOutput(t *testing.T) {
	t.Parallel()

	raw := "## 完成\n\n- 第一项\n- 第二项\n\n| 列 | 值 |\n| --- | --- |\n| 文件 | 好 |\n\n```go\nfmt.Println(\"好\")\n```"
	got := DelegatedTaskResultText(ToolOutputFallbackInput{
		ToolName: "TASK",
		Status:   ToolStatusCompleted,
		RawOutput: map[string]any{
			"state":         "completed",
			"target_kind":   "subagent",
			"final_message": raw,
		},
		Meta: map[string]any{
			"caelis": map[string]any{"runtime": map[string]any{"task": map[string]any{"result": "legacy metadata"}}},
		},
	})
	if got != raw {
		t.Fatalf("DelegatedTaskResultText() = %q, want exact durable Final Message %q", got, raw)
	}
}

func TestTerminalFallbacksForNoOutputAndExitCode(t *testing.T) {
	t.Parallel()

	completed := ToolOutputFallbackInput{
		ToolName: "RUN_COMMAND",
		ToolKind: "execute",
		Meta:     metautil.WithTerminalInfo(nil, "call-1"),
		Status:   ToolStatusCompleted,
	}
	if !TerminalNoOutputPlaceholder(completed) {
		t.Fatal("TerminalNoOutputPlaceholder() = false, want completed terminal no-output fallback")
	}

	failed := ToolOutputFallbackInput{
		ToolName:  "RUN_COMMAND",
		ToolKind:  "execute",
		Status:    ToolStatusFailed,
		Error:     true,
		RawOutput: map[string]any{"exit_code": 7},
	}
	if got := TerminalExitCodeOutputText(failed); got != "exit 7" {
		t.Fatalf("TerminalExitCodeOutputText() = %q, want exit 7", got)
	}
}

func acpToolNameMetaForDisplayTest(name string) map[string]any {
	return map[string]any{
		"caelis": map[string]any{
			"runtime": map[string]any{
				"tool": map[string]any{"name": name},
			},
		},
	}
}
