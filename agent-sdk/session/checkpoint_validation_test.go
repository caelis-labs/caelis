package session

import (
	"strings"
	"testing"
)

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
