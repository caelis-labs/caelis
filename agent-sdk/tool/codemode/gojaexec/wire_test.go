package gojaexec

import (
	"encoding/binary"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

type headerOnly struct {
	read bool
	kind byte
}

func (r *headerOnly) Read(p []byte) (int, error) {
	if r.read {
		panic("payload read before length rejection")
	}
	r.read = true
	var h [13]byte
	binary.BigEndian.PutUint32(h[:], 1<<30)
	h[4] = r.kind
	return copy(p, h[:]), nil
}

func TestRPCSizeRejectsBeforePayloadRead(t *testing.T) {
	for _, kind := range []byte{frameResult, frameError, frameFatal, frameReply, frameSource} {
		if _, err := readFrame(&headerOnly{kind: kind}, 1024, true); err == nil {
			t.Fatalf("oversize frame %d was not rejected at header", kind)
		}
	}
	if _, err := readFrame(&headerOnly{kind: frameCall}, 1024, false); err == nil {
		t.Fatal("worker accepted an oversized inbound call frame")
	}
	if boundedReply(codemode.Reply{Parts: make([]codemode.Part, 100)}, 1024) {
		t.Fatal("oversized reply would be encoded")
	}
}
