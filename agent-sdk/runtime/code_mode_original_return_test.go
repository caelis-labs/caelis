package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestOriginalReturnKeepsOutputAndMediaBudgets(t *testing.T) {
	for _, media := range []bool{false, true} {
		t.Run(map[bool]string{false: "output", true: "media"}[media], func(t *testing.T) {
			limits := codemode.DefaultLimits()
			limits.OutputBytes = 64
			limits.Calls = 1
			h := &codeModeHost{limits: limits, codeModeResults: newCodeModeResults()}
			result := tool.Result{Content: []model.Part{model.NewJSONPart(json.RawMessage(`{"large":"` + strings.Repeat("x", 100) + `"}`))}}
			if media {
				limits.OutputBytes = 4096
				h.limits = limits
				part := model.Part{Kind: model.PartKindMedia, Media: &model.MediaPart{MimeType: "image/png"}}
				result.Content = []model.Part{part, part}
			}
			if _, err := h.project("original", result, codemode.OutcomeSucceeded, tool.Definition{}); err != nil {
				t.Fatal(err)
			}
			if _, err := h.returnedParts(codemode.Output{Reply: true, CallID: "original"}); err == nil {
				t.Fatal("whole original reply bypassed an existing output/media budget")
			}
			if _, err := h.returnedParts(codemode.Output{Reply: true, CallID: "foreign"}); err == nil {
				t.Fatal("foreign original reply acquired authority")
			}
		})
	}
}
