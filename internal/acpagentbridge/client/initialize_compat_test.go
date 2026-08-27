package client

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestInitializeResponsePreservesExtensionMeta(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"protocolVersion":1,
		"agentCapabilities":{
			"sessionCapabilities":{"vendor.session":{"supported":true}},
			"_meta":{"legacy":{"supported":true}}
		},
		"authMethods":[{
			"id":"vendor-auth",
			"name":"Vendor auth",
			"type":"vendor.example/auth",
			"challenge":{"kind":"future"}
		}],
		"_meta":{
			"steering":{"supported":true},
			"vendor.example":{"revision":2,"features":["alpha"]}
		}
	}`)
	var response InitializeResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if _, ok := response.Meta["steering"]; !ok {
		t.Fatalf("top-level initialize _meta = %#v, want steering", response.Meta)
	}
	if _, ok := response.Meta["vendor.example"]; !ok {
		t.Fatalf("top-level initialize _meta = %#v, want vendor extension", response.Meta)
	}
	if _, ok := response.AgentCapabilities.Meta["legacy"]; !ok {
		t.Fatalf("agent capabilities _meta = %#v, want independent legacy extension", response.AgentCapabilities.Meta)
	}
	if _, ok := response.AgentCapabilities.SessionCapabilities["vendor.session"]; !ok {
		t.Fatalf("session capabilities = %#v, want unknown vendor capability", response.AgentCapabilities.SessionCapabilities)
	}
	if len(response.AuthMethods) != 1 {
		t.Fatalf("auth methods = %#v, want unknown vendor descriptor", response.AuthMethods)
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var encodedObject map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &encodedObject); err != nil {
		t.Fatal(err)
	}
	if _, ok := encodedObject["_meta"]; !ok {
		t.Fatalf("encoded initialize response = %s, want top-level _meta", encoded)
	}
	var encodedCapabilities map[string]json.RawMessage
	if err := json.Unmarshal(encodedObject["agentCapabilities"], &encodedCapabilities); err != nil {
		t.Fatal(err)
	}
	if _, ok := encodedCapabilities["_meta"]; !ok {
		t.Fatalf("encoded agent capabilities = %s, want independent _meta", encodedObject["agentCapabilities"])
	}
	if _, ok := encodedCapabilities["sessionCapabilities"]; !ok {
		t.Fatalf("encoded agent capabilities = %s, want session capabilities", encodedObject["agentCapabilities"])
	}
	if _, ok := encodedObject["authMethods"]; !ok {
		t.Fatalf("encoded initialize response = %s, want raw auth methods", encoded)
	}
	var roundTrip InitializeResponse
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	roundTripMethods := roundTrip.AuthMethods
	responseMethods := response.AuthMethods
	roundTrip.AuthMethods = nil
	response.AuthMethods = nil
	if !reflect.DeepEqual(roundTrip, response) {
		t.Fatalf("initialize compatibility round trip = %#v, want %#v", roundTrip, response)
	}
	if len(roundTripMethods) != len(responseMethods) {
		t.Fatalf("auth method count = %d, want %d", len(roundTripMethods), len(responseMethods))
	}
	for index := range responseMethods {
		var got, want any
		if err := json.Unmarshal(roundTripMethods[index], &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(responseMethods[index], &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("auth method %d = %#v, want %#v", index, got, want)
		}
	}
}
