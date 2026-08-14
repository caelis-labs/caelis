package prefixusage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

const (
	// Inline media is opaque provider input. Keep its local budget bounded by
	// modality instead of treating base64 or file bytes as prompt text.
	EstimatedImageMediaTokens = 4096
	EstimatedOtherMediaTokens = 8192
)

// Snapshot identifies and estimates the model-request prefix controlled by
// runtime assembly. Messages are deliberately excluded because provider usage
// plus post-snapshot event deltas account for conversation history.
type Snapshot struct {
	Fingerprint string
	Tokens      int
}

// ForRequest returns a deterministic local estimate of instructions, tools,
// and output shape. Inline media bytes are removed before hashing and charged
// through a bounded per-modality budget.
func ForRequest(req *model.Request) Snapshot {
	if req == nil {
		return Snapshot{}
	}
	prefix := model.Request{
		Instructions: model.CloneParts(req.Instructions),
		Tools:        model.CloneToolSpecs(req.Tools),
		Output:       model.CloneOutputSpec(req.Output),
	}
	mediaTokens := stripInlineMediaData(prefix.Instructions)
	raw, err := json.Marshal(prefix)
	if err != nil {
		return Snapshot{}
	}
	sum := sha256.Sum256(raw)
	return Snapshot{
		Fingerprint: hex.EncodeToString(sum[:]),
		Tokens:      estimateTextTokens(string(raw)) + mediaTokens,
	}
}

func stripInlineMediaData(parts []model.Part) int {
	total := 0
	for i := range parts {
		part := &parts[i]
		if part.Media != nil {
			part.Media.Source.Data = ""
			switch part.Media.Modality {
			case model.MediaModalityImage:
				total += EstimatedImageMediaTokens
			default:
				total += EstimatedOtherMediaTokens
			}
		}
		if part.ToolResult != nil {
			total += stripInlineMediaData(part.ToolResult.Content)
		}
	}
	return total
}

func estimateTextTokens(text string) int {
	runes := utf8.RuneCountInString(text)
	if runes == 0 {
		return 0
	}
	return (runes + 3) / 4
}
