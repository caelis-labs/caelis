package authentication

import (
	"encoding/json"
	"reflect"
	"testing"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestMethodsDefaultsMissingTypeKeepsTerminalAndSkipsMalformed(t *testing.T) {
	t.Parallel()

	methods := Methods(client.InitializeResponse{AuthMethods: []json.RawMessage{
		json.RawMessage(`{"id":" browser ","name":" Browser login ","args":["ignored"],"env":{"IGNORED":"1"}}`),
		json.RawMessage(`{"id":"terminal","name":"Terminal login","type":" TERMINAL ","args":["login"],"env":{"LOGIN":"1"}}`),
		json.RawMessage(`{"id":"browser","name":"duplicate"}`),
		json.RawMessage(`{"id":"","name":"invalid"}`),
		json.RawMessage(`{"id":"unsupported","name":"Unsupported","type":"browser"}`),
	}})
	want := []controlagents.AuthenticationMethod{
		{ID: "browser", Name: "Browser login", Type: controlagents.AuthenticationAgent},
		{
			ID: "terminal", Name: "Terminal login", Type: controlagents.AuthenticationTerminal,
			Args: []string{"login"}, Env: map[string]string{"LOGIN": "1"},
		},
	}
	if !reflect.DeepEqual(methods, want) {
		t.Fatalf("Methods() = %#v, want %#v", methods, want)
	}
}
