package experimental

import (
	"encoding/json"
	"maps"
	"testing"
)

func TestValuesRejectUnknownAndNullWithoutLosingFalse(t *testing.T) {
	for _, raw := range []string{`{"async_compact":null}`, `{"mcp_catalog_refresh":"yes"}`} {
		var values Values
		if json.Unmarshal([]byte(raw), &values) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, enabled := range []bool{false, true} {
		if Validate(Values{"missing": enabled}) == nil {
			t.Fatal("unknown feature accepted")
		}
	}
	var values Values
	if err := json.Unmarshal([]byte(`{"async_compact":false}`), &values); err != nil {
		t.Fatal(err)
	}
	if enabled, present := values[AsyncCompact]; enabled || !present {
		t.Fatal("explicit false disappeared")
	}
}

func TestCatalogOmitsLegacyFeatures(t *testing.T) {
	definitions := Definitions()
	if len(definitions) != 2 || definitions[0].ID != AsyncCompact || definitions[1].ID != IdleCompact {
		t.Fatalf("catalog = %+v", definitions)
	}
	for _, enabled := range []bool{false, true} {
		legacy := Values{MCPRefresh: enabled, ToolRetirement: enabled}
		if err := Validate(legacy); err != nil {
			t.Fatal(err)
		}
		if got := Complete(legacy); !maps.Equal(got, Values{AsyncCompact: false, IdleCompact: false}) {
			t.Fatalf("complete = %v", got)
		}
	}
}

func TestCompleteAppliesDefaultsAndExplicitChoices(t *testing.T) {
	for _, definition := range Definitions() {
		if got := Complete(nil)[definition.ID]; got != definition.Default {
			t.Fatalf("default %s = %v", definition.ID, got)
		}
		for _, enabled := range []bool{false, true} {
			if got := Complete(Values{definition.ID: enabled})[definition.ID]; got != enabled {
				t.Fatalf("choice %s = %v", definition.ID, got)
			}
		}
	}
}
