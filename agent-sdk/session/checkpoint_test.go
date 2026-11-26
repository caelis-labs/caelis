package session

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
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

func TestContextAppendContractRejectsFutureAndMissingCoverage(t *testing.T) {
	valid := Event{Type: EventTypeContext, ContextEdit: &ContextEditProvenance{Version: 1, SourceStamp: "stamp", SourceThroughID: "source", SourceThroughSeq: 1, Hook: "retrieval", LogicalRequestID: "request"}}
	if _, err := MigrateEvent(valid); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"version", "stamp", "id", "seq", "hook", "request", "type"} {
		t.Run(field, func(t *testing.T) {
			e := CloneEvent(&valid)
			switch field {
			case "version":
				e.ContextEdit.Version = 2
			case "stamp":
				e.ContextEdit.SourceStamp = ""
			case "id":
				e.ContextEdit.SourceThroughID = ""
			case "seq":
				e.ContextEdit.SourceThroughSeq = 0
			case "hook":
				e.ContextEdit.Hook = ""
			case "request":
				e.ContextEdit.LogicalRequestID = ""
			case "type":
				e.Type = EventTypeUser
			}
			if _, err := MigrateEvent(*e); err == nil {
				t.Fatal("unsupported or incomplete append contract accepted")
			}
		})
	}
	var future Event
	if err := future.UnmarshalJSON([]byte(`{"schema":4,"type":"compact","_meta":{"compact":{"contract_version":"3"}}}`)); err == nil || !strings.Contains(err.Error(), "unsupported checkpoint") {
		t.Fatalf("future checkpoint error=%v", err)
	}
}
