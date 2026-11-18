package agents

import (
	"reflect"
	"testing"
)

func TestConnectStateRoundTripPreservesModelSelections(t *testing.T) {
	models := []string{"Vendor/Model=beta", "Vendor/Model,alpha"}
	got, err := DecodeConnectState(EncodeConnectState(ConnectState{Agent: "custom", Models: models}))
	if err != nil || !reflect.DeepEqual(got.ConnectRequest("").ModelIDs, models) {
		t.Fatalf("selection = %#v, %v; want %v", got, err, models)
	}
}

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
