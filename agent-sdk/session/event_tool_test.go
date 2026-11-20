package session

import (
	"encoding/json"
	"testing"
)

func TestEventToolReadsLegacyFailureIntoOutcome(t *testing.T) {
	const raw = `{"result":{"is_error":true,"content":[{"kind":"text","text":{"text":"failed"}}]}}`
	var decoded EventTool
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Outcome != ToolOutcomeFailed || len(decoded.Result.Content) != 1 {
		t.Fatalf("legacy failure lost: %+v", decoded)
	}
	const knownEffect = `{"outcome":"succeeded","result":{"is_error":true}}`
	if err := json.Unmarshal([]byte(knownEffect), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Outcome != ToolOutcomeSucceeded {
		t.Fatalf("legacy result overrode outcome: %+v", decoded)
	}
}

func TestEventToolUnmarshalPreservesOnlyInputNumbers(t *testing.T) {
	t.Parallel()
	var event Event
	if err := json.Unmarshal([]byte(`{"type":"tool_result","tool":{"input":{"decimal":9007199254740991.1,"integer":900719925474099312345678901234567890,"exponent":1e1000},"output":{"value":12.5}},"notice":{"meta":{"value":12.5}},"_meta":{"value":900719925474099312345678901234567890}}`), &event); err != nil {
		t.Fatal(err)
	}
	if event.Tool == nil {
		t.Fatal("missing decoded tool payload")
	}
	for key, want := range map[string]json.Number{
		"decimal":  "9007199254740991.1",
		"integer":  "900719925474099312345678901234567890",
		"exponent": "1e1000",
	} {
		if got, ok := event.Tool.Input[key].(json.Number); !ok || got != want {
			t.Fatalf("tool input[%q] = %#v, want exact JSON number %s", key, event.Tool.Input[key], want)
		}
	}
	if got, ok := event.Tool.Output["value"].(float64); !ok || got != 12.5 {
		t.Fatalf("tool output value = %#v, want float64(12.5)", event.Tool.Output["value"])
	}
	if got, ok := event.Notice.Meta["value"].(float64); !ok || got != 12.5 {
		t.Fatalf("notice metadata value = %#v, want float64(12.5)", event.Notice.Meta["value"])
	}
	if got := event.Meta["value"]; got != json.Number("900719925474099312345678901234567890") {
		t.Fatalf("durable metadata value = %#v, want existing json.Number behavior", got)
	}
}
