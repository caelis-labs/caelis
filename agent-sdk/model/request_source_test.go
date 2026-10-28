package model

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRequestSourceClonesWithoutChangingProviderJSON(t *testing.T) {
	req := &Request{Messages: []Message{NewTextMessage(RoleUser, "hello")}, Source: &RequestSource{
		SourceThroughSeq: 7, SourcePrefixHash: "source", MessagesHash: "messages",
	}}
	cloned := CloneRequest(req)
	if !reflect.DeepEqual(cloned, req) || cloned.Source == req.Source {
		t.Fatal("request source was not copied independently")
	}
	cloned.Source.SourceThroughSeq++
	if req.Source.SourceThroughSeq != 7 {
		t.Fatal("clone changed original coverage")
	}
	withSource, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Source = nil
	withoutSource, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if string(withSource) != string(withoutSource) {
		t.Fatal("local coverage changed provider JSON")
	}
}
