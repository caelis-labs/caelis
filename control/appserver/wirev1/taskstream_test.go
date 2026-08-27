package wirev1

import (
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
)

func TestStreamErrorWirePreservesRetryClassWithoutLeakingDetail(t *testing.T) {
	t.Parallel()

	slow := DecodeTaskStreamError(EncodeTaskStreamError(controltaskstream.ErrSlowConsumer))
	if !errors.Is(slow, controltaskstream.ErrSlowConsumer) {
		t.Fatalf("slow consumer round trip = %v", slow)
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
