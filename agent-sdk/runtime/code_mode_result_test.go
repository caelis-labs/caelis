package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestCodeModeProjectionFailureDoesNotRetainMedia(t *testing.T) {
	raw := json.RawMessage(`{"ok":true}`)
	result := tool.Result{Content: []model.Part{
		model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: "original-bytes"}, "image/png", "fixture"),
		model.NewJSONPart(raw),
	}, StructuredContent: &tool.StructuredContent{Value: raw, ContentIndex: 1}}
	for _, failure := range []string{"provenance", "structured_budget"} {
		t.Run(failure, func(t *testing.T) {
			limits := codemode.DefaultLimits()
			retained := opaquePartBytes(result.Content[0]) + len(raw)
			limits.RetainedBytes = retained * 2
			h := &codeModeHost{limits: limits, codeModeResults: newCodeModeResults()}
			first, err := h.project("first", result, codemode.OutcomeSucceeded, tool.Definition{OutputSchema: map[string]any{"type": "object"}})
			if err != nil {
				t.Fatal(err)
			}
			invalid := result
			invalid.StructuredContent = &tool.StructuredContent{Value: raw, ContentIndex: 0}
			if failure == "structured_budget" {
				invalid = result
				// Original parts fit exactly; the additional structured Value does not.
				h.limits.ReplyBytes = len("failed")*6 + 128 + 256 + len("image/png")*6 + len(raw)*12 + 64
			}
			for i := range 5 {
				if _, err := h.project("failed", invalid, codemode.OutcomeSucceeded, tool.Definition{OutputSchema: map[string]any{"type": "object"}}); err == nil {
					t.Fatalf("failure %d accepted", i)
				}
				if len(h.handles) != 1 || len(h.replies) != 1 || h.retained != retained {
					t.Errorf("failure %d changed retained state: handles=%d replies=%d bytes=%d", i, len(h.handles), len(h.replies), h.retained)
				}
			}
			h.limits.ReplyBytes = limits.ReplyBytes
			second, err := h.project("second", result, codemode.OutcomeSucceeded, tool.Definition{OutputSchema: map[string]any{"type": "object"}})
			if err != nil || h.retained != limits.RetainedBytes || len(h.handles) != 2 || len(h.replies) != 2 {
				t.Fatalf("legal projection at exact retention limit: handles=%d replies=%d bytes=%d err=%v", len(h.handles), len(h.replies), h.retained, err)
			}
			if _, err := h.project("overflow", tool.Result{Content: []model.Part{model.NewTextPart("x")}}, codemode.OutcomeSucceeded, tool.Definition{OutputSchema: map[string]any{"type": "object"}}); err == nil || !strings.Contains(err.Error(), "retention budget") {
				t.Fatalf("one byte over budget: %v", err)
			}
			if h.retained != limits.RetainedBytes || len(h.handles) != 2 || len(h.replies) != 2 {
				t.Fatal("budget failure changed retained state")
			}
			for _, reply := range []codemode.Reply{first, second} {
				parts, err := h.returnedParts(codemode.Output{Reply: true, CallID: reply.CallID})
				if err != nil || len(parts) != 2 || parts[0].Media != result.Content[0].Media {
					t.Fatalf("original media lost after failures: %+v %v", parts, err)
				}
			}
		})
	}
}
