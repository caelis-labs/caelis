package taskstream

import (
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

func TestStreamErrorWirePreservesRetryClassWithoutLeakingDetail(t *testing.T) {
	t.Parallel()

	slow := DecodeStreamError(EncodeStreamError(ErrSlowConsumer))
	if !errors.Is(slow, ErrSlowConsumer) {
		t.Fatalf("slow consumer round trip = %v", slow)
	}

	wire := EncodeStreamError(errorcode.Wrap(
		errorcode.Unavailable,
		"provider secret must not cross the wire",
		errors.New("token=secret"),
	))
	if wire.Code != string(errorcode.Unavailable) ||
		wire.Message != "Task stream is unavailable" {
		t.Fatalf("wire error = %#v", wire)
	}
	if got := DecodeStreamError(wire); errorcode.CodeOf(got) != errorcode.Unavailable {
		t.Fatalf("decoded code = %q, error = %v", errorcode.CodeOf(got), got)
	}
}
