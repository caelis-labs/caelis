package acputil

import (
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestPrefixTextBlockPrependsTrustedTextWithoutScanningPrompt(t *testing.T) {
	t.Parallel()

	if got := PrefixTextBlock(nil, "slice"); got != nil {
		t.Fatalf("PrefixTextBlock(nil) = %s, want unchanged empty prompt", got)
	}
	prompt := BuildPromptParts("", []model.ContentPart{
		{Type: model.ContentPartText, Text: "slice"},
		{Type: model.ContentPartText, Text: "task"},
	})
	got := PrefixTextBlock(prompt, "slice")
	if len(got) != 3 {
		t.Fatalf("len(PrefixTextBlock()) = %d, want trusted prefix plus existing parts", len(got))
	}
	var first, second, third struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(got[0], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got[1], &second); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got[2], &third); err != nil {
		t.Fatal(err)
	}
	if first.Text != "slice" || second.Text != "slice" || third.Text != "task" {
		t.Fatalf("prefixed parts = %q / %q / %q, want trusted slice then original parts", first.Text, second.Text, third.Text)
	}
}
