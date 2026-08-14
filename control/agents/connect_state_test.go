package agents

import (
	"reflect"
	"testing"
)

func TestConnectStateRoundTripPreservesConfigValuePunctuation(t *testing.T) {
	want := NormalizeConnectState(ConnectState{
		Agent: "claude", Launcher: LauncherChoiceManaged, Model: "opus",
		ConfigValues: map[string]string{"instructions": "short, exact=a=b", "mode": "review"},
	})
	got, err := DecodeConnectState(EncodeConnectState(want))
	if err != nil {
		t.Fatalf("DecodeConnectState() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("connect state = %#v, want %#v", got, want)
	}
}
