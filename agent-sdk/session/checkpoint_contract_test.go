package session

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"
)

func TestCheckpointDataContractMetadataRoundTrip(t *testing.T) {
	const version = 1
	data := CheckpointData{
		Revision:             3,
		ContractVersion:      version,
		SummarizedThroughID:  "event-9",
		SummarizedThroughSeq: 9,
		Generator:            "model_markdown",
		Trigger:              "manual",
		SourceEventCount:     8,
		TotalTokens:          100,
		ContextWindowTokens:  1000,
		DiscoveredTools:      []string{"mcp__calendar__demo__create_event", "mcp__calendar__demo__create_event", ""},
	}
	value := CheckpointDataValue(data)
	event := &Event{
		Type: EventTypeCompact,
		Meta: map[string]any{CheckpointMetadataKey: value},
	}

	got, ok := CheckpointDataFromEvent(event)
	if !ok {
		t.Fatal("CheckpointDataFromEvent() ok = false")
	}
	if got.ContractVersion != version || got.SourceEventCount != 8 {
		t.Fatalf("contract/source metadata = %d/%d, want %d/8", got.ContractVersion, got.SourceEventCount, version)
	}
	if len(got.DiscoveredTools) != 1 || got.DiscoveredTools[0] != "mcp__calendar__demo__create_event" {
		t.Fatalf("discovered tools = %v, want normalized MCP tool", got.DiscoveredTools)
	}
}

func TestCheckpointDataPreservesExactDistinctToolNames(t *testing.T) {
	t.Parallel()

	value := CheckpointDataValue(CheckpointData{
		ContractVersion: 1,
		DiscoveredTools: []string{"Search", "SEARCH", "Search"},
	})
	event := &Event{Type: EventTypeCompact, Meta: map[string]any{CheckpointMetadataKey: value}}
	got, ok := CheckpointDataFromEvent(event)
	if !ok {
		t.Fatal("CheckpointDataFromEvent() ok = false")
	}
	if len(got.DiscoveredTools) != 2 || got.DiscoveredTools[0] != "Search" || got.DiscoveredTools[1] != "SEARCH" {
		t.Fatalf("discovered tools = %v, want exact [Search SEARCH]", got.DiscoveredTools)
	}
}

func TestCompactEventSequencePersistsAsExactDecimalString(t *testing.T) {
	for _, sequence := range []uint64{
		9007199254740991,
		9007199254740992,
		9007199254740993,
		math.MaxUint64,
	} {
		sequence := sequence
		t.Run(strconv.FormatUint(sequence, 10), func(t *testing.T) {
			before := Event{
				Type: EventTypeCompact,
				Meta: map[string]any{CheckpointMetadataKey: CheckpointDataValue(CheckpointData{
					ContractVersion: 1, SummarizedThroughSeq: sequence,
				})},
			}
			raw, err := json.Marshal(before)
			if err != nil {
				t.Fatal(err)
			}
			var after Event
			if err := json.Unmarshal(raw, &after); err != nil {
				t.Fatal(err)
			}
			got, ok := CheckpointDataFromEvent(&after)
			if !ok || got.SummarizedThroughSeq != sequence {
				t.Fatalf("CheckpointDataFromEvent() = %#v, %v; want sequence %d", got, ok, sequence)
			}
		})
	}
}
