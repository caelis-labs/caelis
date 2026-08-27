package schema

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSessionSteeringRequestPreservesContentAndExtensionMeta(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"sessionId":"session-1",
		"prompt":[
			{"type":"text","text":"adjust the plan"},
			{"type":"image","mimeType":"image/png","data":"aGVsbG8="}
		],
		"_meta":{
			"steering":{"idleBehavior":"promptRequired"},
			"vendor.example":{"delivery":"urgent","sequence":9007199254740993}
		}
	}`)
	var request SessionSteeringRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	if request.SessionID != "session-1" || len(request.Prompt) != 2 {
		t.Fatalf("steering request = %#v", request)
	}
	if string(request.Meta["steering"]) != `{"idleBehavior":"promptRequired"}` {
		t.Fatalf("steering request _meta = %#v", request.Meta)
	}
	wantVendor := `{"delivery":"urgent","sequence":9007199254740993}`
	if string(request.Meta["vendor.example"]) != wantVendor {
		t.Fatalf("vendor request _meta = %s, want %s", request.Meta["vendor.example"], wantVendor)
	}

	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	assertEquivalentJSON(t, encoded, raw)
	var roundTrip SessionSteeringRequest
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if string(roundTrip.Meta["vendor.example"]) != wantVendor {
		t.Fatalf("vendor request _meta after round trip = %s, want exact %s", roundTrip.Meta["vendor.example"], wantVendor)
	}
}

func TestSessionSteeringResponseRetainsKnownAndFutureOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		outcome SessionSteeringOutcome
		reason  string
	}{
		{name: "injected", raw: `{"outcome":"injected"}`, outcome: SessionSteeringInjected},
		{name: "started new turn", raw: `{"outcome":"startedNewTurn"}`, outcome: SessionSteeringStartedNewTurn},
		{name: "prompt required", raw: `{"outcome":"promptRequired","reason":"noRunningTurn"}`, outcome: SessionSteeringPromptRequired, reason: "noRunningTurn"},
		{name: "failed", raw: `{"outcome":"failed"}`, outcome: SessionSteeringFailed},
		{name: "future", raw: `{"outcome":"deferred","reason":"agentPolicy"}`, outcome: SessionSteeringOutcome("deferred"), reason: "agentPolicy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var response SessionSteeringResponse
			if err := json.Unmarshal([]byte(tt.raw), &response); err != nil {
				t.Fatal(err)
			}
			if response.Outcome != tt.outcome || response.Reason != tt.reason {
				t.Fatalf("steering response = %#v, want outcome %q reason %q", response, tt.outcome, tt.reason)
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			assertEquivalentJSON(t, encoded, []byte(tt.raw))
		})
	}
}

func assertEquivalentJSON(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode got JSON: %v", err)
	}
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode want JSON: %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch:\n got: %s\nwant: %s", got, want)
	}
}
