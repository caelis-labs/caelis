package runtime

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestCodeModeProjectionDistinguishesNullFromText(t *testing.T) {
	for _, tc := range []struct {
		name       string
		structured bool
		want       string
	}{
		{name: "text-only"},
		{name: "structured-null", structured: true, want: "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &codeModeHost{limits: codemode.DefaultLimits(), codeModeResults: newCodeModeResults()}
			result := tool.Result{Content: []tool.ContentBlock{tool.Text(`{"not":"data"}`)}}
			if tc.structured {
				result.Content = append(result.Content, tool.StructuredContent{Value: json.RawMessage(`null`)})
			}
			def := tool.Definition{OutputSchema: map[string]any{"type": "null"}}
			reply, err := h.project("call", result, codemode.OutcomeSucceeded, def)
			if err != nil || string(reply.Value) != tc.want || reply.BodyText != `{"not":"data"}` || len(reply.Parts) != len(result.Content) {
				t.Fatalf("unexpected projection: %+v, %v", reply, err)
			}
		})
	}
}

func TestCodeModeProjectionFailureDoesNotRetainMedia(t *testing.T) {
	raw := json.RawMessage(`{"ok":true}`)
	image := tool.Image{Data: []byte("original-bytes"), MIMEType: "image/png", Name: "fixture"}
	result := tool.Result{Content: []tool.ContentBlock{
		image,
		tool.StructuredContent{Value: raw},
	}}
	for _, failure := range []string{"duplicate_structured", "structured_budget"} {
		t.Run(failure, func(t *testing.T) {
			limits := codemode.DefaultLimits()
			retained := len(image.Data) + len(image.MIMEType) + len(image.Name) + len(raw)
			limits.RetainedBytes = retained * 2
			h := &codeModeHost{limits: limits, codeModeResults: newCodeModeResults()}
			first, err := h.project("first", result, codemode.OutcomeSucceeded, tool.Definition{OutputSchema: map[string]any{"type": "object"}})
			if err != nil {
				t.Fatal(err)
			}
			invalid := result
			invalid.Content = append(result.Content, tool.StructuredContent{Value: raw})
			if failure == "structured_budget" {
				invalid = result
				// Original parts fit exactly; the additional structured Value does not.
				h.limits.ReplyBytes = len("failed")*6 + 128 + 256 + len("image/png")*6 + len(raw)*6 + 64
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
			if _, err := h.project("overflow", tool.Result{Content: []tool.ContentBlock{tool.Text("x")}}, codemode.OutcomeSucceeded, tool.Definition{OutputSchema: map[string]any{"type": "object"}}); err == nil || !strings.Contains(err.Error(), "retention budget") {
				t.Fatalf("one byte over budget: %v", err)
			}
			if h.retained != limits.RetainedBytes || len(h.handles) != 2 || len(h.replies) != 2 {
				t.Fatal("budget failure changed retained state")
			}
			for _, reply := range []codemode.Reply{first, second} {
				parts, err := h.returnedParts(codemode.Output{Reply: true, CallID: reply.CallID})
				if err != nil || len(parts) != 2 || !reflect.DeepEqual(parts[0], image) {
					t.Fatalf("original media lost after failures: %+v %v", parts, err)
				}
			}
		})
	}
}
