package compact

import (
	"testing"

	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func TestCompactEventDataContractMetadataRoundTrip(t *testing.T) {
	data := CompactEventData{
		Revision:            3,
		ContractVersion:     CompactContractVersion,
		SummarizedThroughID: "event-9",
		Generator:           "model_markdown",
		Trigger:             "manual",
		SourceEventCount:    8,
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
}

func TestPromptEventsFromLatestCompactUsesPureTextOverlay(t *testing.T) {
	compactText := "CONTEXT CHECKPOINT\n\n## Current Objective\n- continue from compact"
	compactEvent := &sdksession.Event{
		Type:       sdksession.EventTypeCompact,
		Visibility: sdksession.VisibilityCanonical,
		Text:       compactText,
		Meta: map[string]any{
			MetaKeyCompact: CompactEventDataValue(CompactEventData{
				ContractVersion: CompactContractVersion,
				Generator:       "model_markdown",
			}),
		},
	}
	next := &sdksession.Event{
		Type:       sdksession.EventTypeUser,
		Visibility: sdksession.VisibilityCanonical,
		Text:       "next user turn",
	}

	got := PromptEventsFromLatestCompact([]*sdksession.Event{compactEvent, next})
	if len(got) != 2 {
		t.Fatalf("prompt event count = %d, want 2 (%+v)", len(got), got)
	}
	if got[0].Type != sdksession.EventTypeUser || got[0].Visibility != sdksession.VisibilityOverlay {
		t.Fatalf("first prompt event = %+v, want overlay user text", got[0])
	}
	if got[0].Message != nil || got[0].Protocol != nil {
		t.Fatalf("first prompt event carries duplicated structured payload: message=%+v protocol=%+v", got[0].Message, got[0].Protocol)
	}
	if got[0].Text != compactText {
		t.Fatalf("first prompt text = %q, want exact compact text", got[0].Text)
	}
	if got[1].Text != "next user turn" {
		t.Fatalf("second prompt text = %q, want next turn", got[1].Text)
	}
}

func TestPromptEventsFromLatestCompactPreservesLegacyReplacementHistory(t *testing.T) {
	legacy := &sdksession.Event{
		Type:       sdksession.EventTypeUser,
		Visibility: sdksession.VisibilityOverlay,
		Protocol: &sdksession.EventProtocol{
			Update: &sdksession.ProtocolUpdate{
				SessionUpdate: string(sdksession.ProtocolUpdateTypeUserMessage),
				Content:       sdksession.ProtocolTextContent("legacy retained instruction"),
			},
		},
	}
	compactEvent := &sdksession.Event{
		Type:       sdksession.EventTypeCompact,
		Visibility: sdksession.VisibilityCanonical,
		Text:       "CONTEXT CHECKPOINT\nnew compact text",
		Meta: map[string]any{
			MetaKeyCompact: map[string]any{
				"contract_version":    CompactContractVersion,
				"replacement_history": []*sdksession.Event{legacy},
			},
		},
	}

	got := PromptEventsFromLatestCompact([]*sdksession.Event{compactEvent})
	if len(got) != 1 {
		t.Fatalf("prompt event count = %d, want 1 (%+v)", len(got), got)
	}
	if got[0].Text != "legacy retained instruction" {
		t.Fatalf("prompt text = %q, want legacy retained instruction", got[0].Text)
	}
	if got[0].Message != nil || got[0].Protocol != nil {
		t.Fatalf("legacy prompt overlay should be pure text, got message=%+v protocol=%+v", got[0].Message, got[0].Protocol)
	}
}

func TestPromptEventsFromLatestCompactPreservesLegacyRetainedInputs(t *testing.T) {
	compactText := "CONTEXT CHECKPOINT\nlegacy summary"
	compactEvent := &sdksession.Event{
		Type:       sdksession.EventTypeCompact,
		Visibility: sdksession.VisibilityCanonical,
		Text:       compactText,
		Meta: map[string]any{
			MetaKeyCompact: map[string]any{
				"contract_version":     CompactContractVersion,
				"retained_user_inputs": []string{"legacy user constraint", "legacy user constraint", ""},
			},
		},
	}

	got := PromptEventsFromLatestCompact([]*sdksession.Event{compactEvent})
	if len(got) != 2 {
		t.Fatalf("prompt event count = %d, want retained input plus compact text (%+v)", len(got), got)
	}
	if got[0].Text != "legacy user constraint" || got[1].Text != compactText {
		t.Fatalf("prompt texts = %q / %q, want legacy retained input then compact text", got[0].Text, got[1].Text)
	}
}
