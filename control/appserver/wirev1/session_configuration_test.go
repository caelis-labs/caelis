package wirev1

import (
	"math"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestCommonSessionProfileAndReceiptWireContract(t *testing.T) {
	profile := sessionprofile.Profile{Version: "test/1", Model: "configured-model", ToolsVersion: "tools/1", Execution: "tools-only", Instructions: "explicit assembly"}
	configuration := sessionprofile.Configuration{SessionID: "session", Revision: math.MaxUint64, Profile: profile,
		LastRequest: &sessionprofile.RequestConfiguration{Revision: math.MaxUint64, RequestID: "request", TurnID: "turn"}}
	receipt := appserver.CommandResult{OperationID: "update", SessionID: "session", Outcome: appserver.OutcomeCommitted, Configuration: &configuration}
	validateWireValue(t, "CommandResult", receipt)
	var decoded appserver.CommandResult
	if err := Unmarshal(mustMarshalWire(t, receipt), &decoded); err != nil || !reflect.DeepEqual(decoded, receipt) {
		t.Fatalf("receipt round trip = %+v, %v", decoded, err)
	}
	request := appserver.UpdateSessionConfigurationRequest{WriteBase: appserver.WriteBase{OperationID: "update", SessionID: "session"}, ExpectedConfigurationRevision: math.MaxUint64, Patch: sessionprofile.ConfigurationPatch{Instructions: stringPointer("new")}}
	validateWireValue(t, "UpdateSessionConfigurationRequest", request)
	var decodedRequest appserver.UpdateSessionConfigurationRequest
	if err := DecodeRequest(mustMarshalWire(t, request), &decodedRequest); err != nil || !reflect.DeepEqual(decodedRequest, request) {
		t.Fatalf("request round trip = %+v, %v", decodedRequest, err)
	}
	create := appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create"}, Profile: &profile}
	validateWireValue(t, "CreateSessionRequest", create)
	var decodedCreate appserver.CreateSessionRequest
	if err := DecodeRequest(mustMarshalWire(t, create), &decodedCreate); err != nil || !reflect.DeepEqual(decodedCreate, create) {
		t.Fatalf("profile round trip = %+v, %v", decodedCreate, err)
	}
	resource := sessionprofile.Resource{ID: "resource", SessionID: "session", Name: "text", MediaType: "text/plain", Size: 5, SHA256: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}
	validateWireValue(t, "SessionResourceRequest", appserver.SessionResourceRequest{WriteBase: appserver.WriteBase{OperationID: "upload", SessionID: "session"}, Name: resource.Name, MediaType: resource.MediaType, Data: []byte("hello"), SHA256: resource.SHA256})
	receipt.Configuration = nil
	receipt.SessionResource = &resource
	validateWireValue(t, "CommandResult", receipt)
	if err := Unmarshal(mustMarshalWire(t, receipt), &decoded); err != nil || !reflect.DeepEqual(decoded, receipt) {
		t.Fatalf("resource receipt = %+v, %v", decoded, err)
	}
}
