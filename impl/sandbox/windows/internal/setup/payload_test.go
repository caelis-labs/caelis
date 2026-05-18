package setup

import "testing"

func TestPayloadEncodeDecodeDefaultsUsersAndVersion(t *testing.T) {
	encoded, err := EncodePayload(Payload{StateRoot: `C:\caelis`})
	if err != nil {
		t.Fatalf("EncodePayload() error = %v", err)
	}
	payload, err := DecodePayload(encoded)
	if err != nil {
		t.Fatalf("DecodePayload() error = %v", err)
	}
	if payload.Version != PayloadVersion {
		t.Fatalf("Version = %d, want %d", payload.Version, PayloadVersion)
	}
	if payload.OfflineUsername != OfflineUser || payload.OnlineUsername != OnlineUser {
		t.Fatalf("users = %q/%q", payload.OfflineUsername, payload.OnlineUsername)
	}
}
