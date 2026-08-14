package schema

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDecodeAuthMethodsDefaultsMissingTypeToAgentAndKeepsTerminal(t *testing.T) {
	methods := DecodeAuthMethods([]json.RawMessage{
		json.RawMessage(`{"id":"browser","name":"Browser login"}`),
		json.RawMessage(`{"id":"terminal","name":"Terminal login","type":"terminal","args":["login"],"env":{"LOGIN":"1"}}`),
		json.RawMessage(`{"id":"","name":"invalid"}`),
	})
	want := []AuthMethod{
		{ID: "browser", Name: "Browser login", Type: AuthMethodTypeAgent},
		{ID: "terminal", Name: "Terminal login", Type: AuthMethodTypeTerminal, Args: []string{"login"}, Env: map[string]string{"LOGIN": "1"}},
	}
	if !reflect.DeepEqual(methods, want) {
		t.Fatalf("DecodeAuthMethods() = %#v, want %#v", methods, want)
	}
}
