package gojaexec

import (
	"encoding/binary"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

type headerOnly struct{ read bool }

func (r *headerOnly) Read(p []byte) (int, error) {
	if r.read {
		panic("payload read before length rejection")
	}
	r.read = true
	var h [13]byte
	binary.BigEndian.PutUint32(h[:], 1<<30)
	return copy(p, h[:]), nil
}

func TestRPCSizeRejectsBeforePayloadRead(t *testing.T) {
	if _, err := readFrame(&headerOnly{}, 1024); err == nil {
		t.Fatal("oversize frame was not rejected at header")
	}
	if boundedReply(codemode.Reply{Parts: make([]codemode.Part, 100)}, 1024) {
		t.Fatal("oversized reply would be encoded")
	}
}
