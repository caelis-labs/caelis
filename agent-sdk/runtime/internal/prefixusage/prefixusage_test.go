package prefixusage

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestForRequestExcludesMessagesAndInlineMediaBytes(t *testing.T) {
	t.Parallel()

	request := func(data string, message string) *model.Request {
		return &model.Request{
			Instructions: []model.Part{model.NewTextPart("follow the system contract")},
			Messages: []model.Message{
				model.NewMessage(model.RoleUser,
					model.NewTextPart(message),
					model.NewMediaPart(model.MediaModalityImage, model.MediaSource{
						Kind: model.MediaSourceInline,
						Data: data,
					}, "image/png", "attachment.png"),
				),
			},
		}
	}

	small := ForRequest(request("aW1n", "short"))
	large := ForRequest(request(strings.Repeat("A", 1<<20), strings.Repeat("message ", 10000)))
	if small != large {
		t.Fatalf("prefix snapshots differ with message/attachment size: small=%+v large=%+v", small, large)
	}
}

func TestForRequestChangesWithInstructionsAndUsesBoundedInstructionMedia(t *testing.T) {
	t.Parallel()

	base := ForRequest(&model.Request{Instructions: []model.Part{model.NewTextPart("short")}})
	changed := ForRequest(&model.Request{Instructions: []model.Part{model.NewTextPart(strings.Repeat("long instruction ", 400))}})
	if base.Fingerprint == changed.Fingerprint || changed.Tokens <= base.Tokens {
		t.Fatalf("prefix snapshots = base=%+v changed=%+v, want a larger changed prefix", base, changed)
	}

	media := func(data string) Snapshot {
		return ForRequest(&model.Request{Instructions: []model.Part{
			model.NewMediaPart(model.MediaModalityImage, model.MediaSource{
				Kind: model.MediaSourceInline,
				Data: data,
			}, "image/png", "policy.png"),
		}})
	}
	smallMedia := media("aW1n")
	largeMedia := media(strings.Repeat("A", 1<<20))
	if smallMedia != largeMedia {
		t.Fatalf("instruction media snapshots differ by base64 size: small=%+v large=%+v", smallMedia, largeMedia)
	}
	if smallMedia.Tokens < EstimatedImageMediaTokens {
		t.Fatalf("instruction media tokens = %d, want at least %d", smallMedia.Tokens, EstimatedImageMediaTokens)
	}
}

func TestForRequestTracksActualProjectedToolsAtRequestBoundary(t *testing.T) {
	t.Parallel()

	base := &model.Request{Tools: []model.ToolSpec{
		model.NewFunctionToolSpec("ToolSearch", "", map[string]any{"type": "object"}),
	}}
	before := ForRequest(base)
	afterRequest := *base
	afterRequest.Tools = append(
		append([]model.ToolSpec(nil), base.Tools...),
		model.NewFunctionToolSpec(
			"mcp__calendar__demo__create_event",
			"Create a calendar event",
			map[string]any{"type": "object"},
		),
	)
	after := ForRequest(&afterRequest)
	if before.Fingerprint == after.Fingerprint || after.Tokens <= before.Tokens {
		t.Fatalf("request prefix before=%+v after=%+v, want admitted tool reflected at request boundary", before, after)
	}
}
