package compact

import (
	"testing"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func TestCompactEventDataContractMetadataRoundTrip(t *testing.T) {
	msg := sdkmodel.NewTextMessage(sdkmodel.RoleUser, "keep this")
	data := CompactEventData{
		Revision:            3,
		ContractVersion:     CompactContractVersion,
		SummarizedThroughID: "event-9",
		Generator:           "model_markdown",
		Trigger:             "manual",
		SourceEventCount:    8,
		RetainedUserInputs:  []string{"keep this", "keep this", ""},
		ReplacementHistory:  []*sdksession.Event{{Type: sdksession.EventTypeUser, Message: &msg, Text: "keep this"}},
		TotalTokens:         100,
		ContextWindowTokens: 1000,
	}
	value := CompactEventDataValue(data)
	event := &sdksession.Event{
		Type: sdksession.EventTypeCompact,
		Meta: map[string]any{MetaKeyCompact: value},
	}

	got, ok := CompactEventDataFromEvent(event)
	if !ok {
		t.Fatal("CompactEventDataFromEvent() ok = false")
	}
	if got.ContractVersion != CompactContractVersion || got.SourceEventCount != 8 {
		t.Fatalf("contract/source metadata = %d/%d, want %d/8", got.ContractVersion, got.SourceEventCount, CompactContractVersion)
	}
	if got.RetainedUserCount != 1 || got.ReplacementHistoryCount != 1 {
		t.Fatalf("retained/replacement counts = %d/%d, want 1/1", got.RetainedUserCount, got.ReplacementHistoryCount)
	}
	if len(got.RetainedUserInputs) != 1 || got.RetainedUserInputs[0] != "keep this" {
		t.Fatalf("retained inputs = %#v, want deduped input", got.RetainedUserInputs)
	}
}
