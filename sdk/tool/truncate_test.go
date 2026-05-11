package tool

import (
	"encoding/json"
	"strings"
	"testing"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
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
	if !strings.Contains(stderr, "tokens truncated") {
		t.Fatalf("stderr = %q, want truncation marker", stderr)
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
	if !strings.Contains(stderr, "tokens truncated") {
		t.Fatalf("decoded stderr = %q, want marker", stderr)
	}
}

func TestTruncatePartsPreservesMediaAndTruncatesText(t *testing.T) {
	t.Parallel()

	parts := []sdkmodel.Part{
		sdkmodel.NewTextPart(strings.Repeat("alpha ", 200)),
		sdkmodel.NewMediaPart(sdkmodel.MediaModalityImage, sdkmodel.MediaSource{Kind: sdkmodel.MediaSourceURL, URI: "https://example.test/img.png"}, "image/png", "img"),
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
