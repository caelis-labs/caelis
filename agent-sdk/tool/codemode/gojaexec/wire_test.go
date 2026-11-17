package gojaexec

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestRPCCallFrameIndependentOfOutputBudget(t *testing.T) {
	data, err := json.Marshal(callRequest{Method: "call", Name: "Write", Arguments: json.RawMessage(`{"path":"records.json","content":"` + strings.Repeat("x", 1<<20) + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var pipe bytes.Buffer
	w := wireWriter{w: &pipe}
	if err := w.send(frameCall, 1, data, len(data)); err != nil {
		t.Fatal(err)
	}
	f, err := readFrame(&pipe, 1024, true)
	if err != nil || f.kind != frameCall || f.id != 1 || !bytes.Equal(f.data, data) {
		t.Fatalf("large call frame did not cross the transport intact: bytes=%d err=%v", len(f.data), err)
	}
}

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
