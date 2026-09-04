package wirev1

import (
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

func TestStreamErrorWirePreservesRetryClassWithoutLeakingDetail(t *testing.T) {
	t.Parallel()

	unavailable := DecodeTaskStreamError(EncodeTaskStreamError(errorcode.New(errorcode.Unavailable, "local detail")))
	if errorcode.CodeOf(unavailable) != errorcode.Unavailable {
		t.Fatalf("unavailable round trip = %v", unavailable)
	}

	wire := EncodeTaskStreamError(errorcode.Wrap(
		errorcode.Unavailable,
		"provider secret must not cross the wire",
		errors.New("token=secret"),
	))
	if wire.Code != string(errorcode.Unavailable) ||
		wire.Message != "Task stream is unavailable" {
		t.Fatalf("wire error = %#v", wire)
	}
	if got := DecodeTaskStreamError(wire); errorcode.CodeOf(got) != errorcode.Unavailable {
		t.Fatalf("decoded code = %q, error = %v", errorcode.CodeOf(got), got)
	}
}
