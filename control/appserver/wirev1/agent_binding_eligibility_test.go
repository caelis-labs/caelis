package wirev1

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver/wirev1/generated"
)

func TestAgentBindingEligibilityPreservesEmptyAndPresentCandidates(t *testing.T) {
	for _, ids := range [][]string{nil, {}, {"provider:model", "acp:agent"}} {
		status := agentbinding.HandleStatus{EligibleProfileIDs: ids}
		validateWireValue(t, "AgentHandleStatus", status)
		raw := mustMarshalWire(t, status)
		var decoded agentbinding.HandleStatus
		if err := Unmarshal(raw, &decoded); err != nil || !reflect.DeepEqual(decoded.EligibleProfileIDs, ids) {
			t.Fatalf("eligibility round trip = %#v, %v; want %#v", decoded.EligibleProfileIDs, err, ids)
		}
		var dto generated.AgentHandleStatus
		if err := json.Unmarshal(raw, &dto); err != nil || !reflect.DeepEqual(dto.EligibleProfileIds, ids) {
			t.Fatalf("generated eligibility = %#v, %v; want %#v", dto.EligibleProfileIds, err, ids)
		}
		if ids != nil && len(ids) == 0 && !bytes.Contains(raw, []byte(`"eligible_profile_ids":[]`)) {
			t.Fatalf("empty candidates did not produce an array: %s", raw)
		}
		if ids == nil && bytes.Contains(raw, []byte(`"eligible_profile_ids"`)) {
			t.Fatalf("unnegotiated candidates entered wire response: %s", raw)
		}
	}
	var legacy agentbinding.HandleStatus
	if err := Unmarshal([]byte(`{"Definition":{},"Binding":{},"Profile":{"backend":{},"effort":{}}}`), &legacy); err != nil || legacy.EligibleProfileIDs != nil {
		t.Fatalf("legacy missing field must remain distinguishable: %#v, %v", legacy.EligibleProfileIDs, err)
	}
}
