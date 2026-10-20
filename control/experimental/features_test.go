package experimental

import (
	"encoding/json"
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
