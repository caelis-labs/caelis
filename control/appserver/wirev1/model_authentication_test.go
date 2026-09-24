package wirev1

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1/generated"
)

func TestModelAuthenticationSnapshotsConformAndPreserveReceiptRevision(t *testing.T) {
	for _, snapshot := range []appserver.ModelAuthenticationSnapshot{
		{OperationID: "connect", Sequence: 1, Phase: "waiting_for_browser", VerificationURL: "https://example.invalid/login", UserCode: "DISPLAY-CODE", ChallengeID: "challenge", Prompt: "Authorization response"},
		{OperationID: "connect", Sequence: math.MaxUint64, Phase: "finished", Result: &appserver.CommandResult{OperationID: "connect", Outcome: appserver.OutcomeCommitted, Revision: math.MaxUint64}},
	} {
		validateWireValue(t, "ModelAuthenticationSnapshot", snapshot)
		raw := mustMarshalWire(t, snapshot)
		var decoded appserver.ModelAuthenticationSnapshot
		if err := Unmarshal(raw, &decoded); err != nil || !reflect.DeepEqual(decoded, snapshot) {
			t.Fatalf("snapshot round trip = %+v, %v; want %+v", decoded, err, snapshot)
		}
		var dto generated.ModelAuthenticationSnapshot
		if err := json.Unmarshal(raw, &dto); err != nil {
			t.Fatal(err)
		}
		if string(dto.Sequence) != strconv.FormatUint(snapshot.Sequence, 10) {
			t.Fatalf("generated sequence lost uint64 precision: %s", raw)
		}
		if snapshot.Result != nil && (dto.Result == nil || dto.Result.Revision == nil || string(*dto.Result.Revision) != "18446744073709551615") {
			t.Fatalf("generated receipt lost uint64 precision: %s", raw)
		}
	}
	for _, sequence := range []string{`1`, `"18446744073709551616"`, `"-1"`} {
		raw := []byte(`{"operation_id":"connect","sequence":` + sequence + `,"phase":"starting"}`)
		var decoded appserver.ModelAuthenticationSnapshot
		if err := Unmarshal(raw, &decoded); err == nil {
			t.Fatalf("invalid sequence accepted: %s", sequence)
		}
	}
	for _, revision := range []string{`18446744073709551615`, `"18446744073709551616"`, `"-1"`} {
		raw := []byte(`{"operation_id":"connect","sequence":"1","phase":"finished","result":{"operation_id":"connect","outcome":"committed","revision":` + revision + `}}`)
		var decoded appserver.ModelAuthenticationSnapshot
		if err := Unmarshal(raw, &decoded); err == nil {
			t.Fatalf("invalid nested revision accepted: %s", revision)
		}
	}
	validateWireValue(t, "ModelAuthenticationInput", appserver.ModelAuthenticationInput{ChallengeID: "challenge", Input: "synthetic-code"})
}

func TestACPHostedLauncherRequestConformsToPublicSchema(t *testing.T) {
	revision := uint64(7)
	request := appserver.PrepareACPRequest{WriteBase: appserver.WriteBase{OperationID: "prepare", ExpectedRevision: &revision}, Request: agents.ACPPrepareRequest{AdapterID: "codex", Launcher: agents.LauncherChoiceHosted}}
	validateWireValue(t, "PrepareACPRequest", request)
	raw := mustMarshalWire(t, request)
	if !strings.Contains(string(raw), `"launcher":"hosted"`) {
		t.Fatalf("hosted launcher missing: %s", raw)
	}
	var decoded appserver.PrepareACPRequest
	if err := DecodeRequest(raw, &decoded); err != nil || !reflect.DeepEqual(decoded, request) {
		t.Fatalf("hosted request round trip = %+v, %v", decoded, err)
	}
}
