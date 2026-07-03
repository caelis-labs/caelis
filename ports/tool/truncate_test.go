package tool

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/ports/model"
)

func TestTruncateMapPreservesKeysAndReportsMetadata(t *testing.T) {
	t.Parallel()

	payload := map[string]any{
		"stdout":    "ok",
		"stderr":    strings.Repeat("permission denied\n", 200),
		"exit_code": 1,
	}
	out, info := TruncateMap(payload, TruncationPolicy{MaxTokens: 80})

	if out["stdout"] != "ok" {
		t.Fatalf("stdout = %#v, want ok", out["stdout"])
	}
	if out["exit_code"] != 1 {
		t.Fatalf("exit_code = %#v, want 1", out["exit_code"])
	}
	stderr, _ := out["stderr"].(string)
	if !strings.Contains(stderr, "lines omitted") {
		t.Fatalf("stderr = %q, want omitted line marker", stderr)
	}
	if out["_tool_truncation"] != nil || out["output_meta"] != nil {
		t.Fatalf("out = %#v, should not expose truncation metadata in model payload", out)
	}
	if meta := TruncationMetadata(info); meta == nil || meta["truncated"] != true {
		t.Fatalf("TruncationMetadata(%#v) = %#v, want metadata", info, meta)
	}
}

func TestTruncateTextDoesNotDuplicateExistingLineHeader(t *testing.T) {
	t.Parallel()

	input := "Total output lines: 10\n\n" + strings.Repeat("line\n", 200)
	out, removed := TruncateText(input, TruncationPolicy{MaxTokens: 40})
	if removed == 0 {
		t.Fatal("removed = 0, want truncation")
	}
	if got := strings.Count(out, "Total output lines:"); got != 1 {
		t.Fatalf("header count = %d, want 1 in %q", got, out)
	}
	if got := estimateTextTokens(out); got > 40 {
		t.Fatalf("truncated text estimated tokens = %d, want <= 40", got)
	}
}

func TestTruncateTextSingleLongLineFitsBudgetAfterMarker(t *testing.T) {
	t.Parallel()

	out, removed := TruncateText(strings.Repeat("x", 1000), TruncationPolicy{MaxTokens: 40})
	if removed == 0 {
		t.Fatal("removed = 0, want truncation")
	}
	if got := estimateTextTokens(out); got > 40 {
		t.Fatalf("truncated text estimated tokens = %d, want <= 40; out=%q", got, out)
	}
}

func TestTruncateMapSingleLongValueKeepsMarkerAndFitsBudget(t *testing.T) {
	t.Parallel()

	out, info := TruncateMap(map[string]any{
		"result": strings.Repeat("x", 1000),
	}, TruncationPolicy{MaxTokens: 80})
	if !info.Truncated {
		t.Fatalf("info.Truncated = false, want true")
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("json.Marshal(out) error = %v", err)
	}
	if got := estimateTextTokens(string(raw)); got > 80 {
		t.Fatalf("serialized truncated output estimated tokens = %d, want <= 80; out=%s", got, raw)
	}
	result, _ := out["result"].(string)
	if !strings.Contains(result, "tokens truncated") {
		t.Fatalf("result = %q, want truncation marker", result)
	}
	if _, second := TruncateMap(out, TruncationPolicy{MaxTokens: 80}); second.Truncated {
		t.Fatalf("second truncation still required: %#v", second)
	}
}

func TestTruncateTextKeepsLineStructureAndTruncatesLongLines(t *testing.T) {
	t.Parallel()

	longHead := strings.Repeat("A", 320)
	longTail := strings.Repeat("Z", 320)
	lines := []string{longHead}
	for i := 0; i < 40; i++ {
		lines = append(lines, "middle log line")
	}
	lines = append(lines, longTail)
	out, removed := TruncateText(strings.Join(lines, "\n"), TruncationPolicy{MaxTokens: 120})
	if removed == 0 {
		t.Fatal("removed = 0, want truncation")
	}
	if got := estimateTextTokens(out); got > 120 {
		t.Fatalf("truncated text estimated tokens = %d, want <= 120; out=%q", got, out)
	}
	if !strings.Contains(out, "Total output lines: 42") {
		t.Fatalf("out = %q, want total line header", out)
	}
	if !strings.Contains(out, "lines omitted") {
		t.Fatalf("out = %q, want omitted line marker", out)
	}
	if !strings.Contains(out, "tokens truncated") {
		t.Fatalf("out = %q, want long-line truncation marker", out)
	}
	if !strings.Contains(out, strings.Repeat("A", 8)) || !strings.Contains(out, strings.Repeat("Z", 8)) {
		t.Fatalf("out = %q, want head and tail line content preserved", out)
	}
}

func TestTruncateMapUsesSerializedJSONBudget(t *testing.T) {
	t.Parallel()

	payload := map[string]any{
		"state":     "completed",
		"exit_code": 1,
		"result":    strings.Repeat("log line with \"quoted\" fields and slash \\ markers\n", 20),
	}
	out, info := TruncateMap(payload, TruncationPolicy{MaxTokens: 80})
	if !info.Truncated {
		t.Fatalf("info.Truncated = false, want serialized JSON truncation; info=%#v", info)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("json.Marshal(out) error = %v", err)
	}
	if got := estimateTextTokens(string(raw)); got > 80 {
		t.Fatalf("serialized truncated output estimated tokens = %d, want <= 80; raw=%s", got, raw)
	}
	if _, second := TruncateMap(out, TruncationPolicy{MaxTokens: 80}); second.Truncated {
		t.Fatalf("second truncation still required: %#v", second)
	}
}

func TestTruncateMapRolloutSizedEscapedOutputFitsDefaultBudget(t *testing.T) {
	t.Parallel()

	line := `{"_msg":"sandbox failure with \"quoted\" fields","path":"C:\\tmp\\workspace\\log.json","error":"permission denied"}`
	payload := map[string]any{
		"state":     "completed",
		"exit_code": 1,
		"result":    strings.Repeat(line+"\n", 420),
	}
	out, info := TruncateMap(payload, DefaultTruncationPolicy())
	if !info.Truncated {
		t.Fatalf("info.Truncated = false, want rollout-sized escaped output truncation; info=%#v", info)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("json.Marshal(out) error = %v", err)
	}
	if got := estimateTextTokens(string(raw)); got > DefaultTruncationPolicy().TokenBudget() {
		t.Fatalf("serialized truncated output estimated tokens = %d, want <= %d", got, DefaultTruncationPolicy().TokenBudget())
	}
	result, _ := out["result"].(string)
	if !strings.Contains(result, "lines omitted") {
		t.Fatalf("result = %q, want omitted line marker", result)
	}
	if _, second := TruncateMap(out, DefaultTruncationPolicy()); second.Truncated {
		t.Fatalf("second truncation still required: %#v", second)
	}
}

func TestTruncateMapRecursesIntoJSONString(t *testing.T) {
	t.Parallel()

	inner := map[string]any{
		"status": "failed",
		"stderr": strings.Repeat("denied\n", 300),
	}
	raw, _ := json.Marshal(inner)
	out, info := TruncateMap(map[string]any{"payload": string(raw)}, TruncationPolicy{MaxTokens: 80})
	if meta := TruncationMetadata(info); meta == nil {
		t.Fatalf("TruncationMetadata(%#v) = nil, want metadata", info)
	}

	payloadText, _ := out["payload"].(string)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payloadText), &decoded); err != nil {
		t.Fatalf("payload is not valid JSON after truncation: %v\n%s", err, payloadText)
	}
	if decoded["status"] != "failed" {
		t.Fatalf("decoded status = %#v, want failed", decoded["status"])
	}
	stderr, _ := decoded["stderr"].(string)
	if !strings.Contains(stderr, "lines omitted") {
		t.Fatalf("decoded stderr = %q, want omitted line marker", stderr)
	}
}

func TestTruncateMapProgressPayloadCompletesPromptly(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("progress line\n", DefaultTruncationPolicy().ByteBudget()/2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, info := TruncateMap(map[string]any{
			"task_id": "task-1",
			"state":   "running",
			"running": true,
			"result":  large,
		}, TruncationPolicy{MaxTokens: DefaultTruncationPolicy().MaxTokens})
		if !info.Truncated {
			t.Error("info.Truncated = false, want truncation")
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TruncateMap() did not return promptly for running tool progress payload")
	}
}

func TestTruncateLineUnitsLargeMultilineOutputCompletesPromptly(t *testing.T) {
	t.Parallel()

	input := strings.Repeat("progress line\n", 5000)
	done := make(chan struct{})
	go func() {
		defer close(done)
		out, removed := TruncateText(input, TruncationPolicy{MaxTokens: 40})
		if removed == 0 {
			t.Error("removed = 0, want truncation")
		}
		if got := estimateTextTokens(out); got > 40 {
			t.Errorf("truncated text estimated tokens = %d, want <= 40", got)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TruncateText() did not return promptly for large multiline output")
	}
}

func TestTruncatePartsPreservesMediaAndTruncatesText(t *testing.T) {
	t.Parallel()

	parts := []model.Part{
		model.NewTextPart(strings.Repeat("alpha ", 200)),
		model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceURL, URI: "https://example.test/img.png"}, "image/png", "img"),
	}
	out, info := TruncateParts(parts, TruncationPolicy{MaxTokens: 30})
	if !info.Truncated {
		t.Fatal("info.Truncated = false, want true")
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	if out[1].Media == nil || out[1].Media.Source.URI == "" {
		t.Fatalf("media part = %#v, want preserved", out[1])
	}
	if out[0].Text == nil || !strings.Contains(out[0].Text.Text, "tokens truncated") {
		t.Fatalf("text part = %#v, want truncation marker", out[0])
	}
}
