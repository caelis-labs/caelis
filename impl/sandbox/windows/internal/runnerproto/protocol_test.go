package runnerproto

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	frame, err := NewFrame(TypeSpawn, Spawn{
		Command:       "Write-Output ok",
		CWD:           `C:\work`,
		Timeout:       time.Second,
		StdinOpen:     true,
		ReadRoots:     []string{`C:\Windows`},
		CapabilitySID: []string{"S-1-5-32-1"},
		Network:       "offline",
	})
	if err != nil {
		t.Fatalf("NewFrame() error = %v", err)
	}
	if err := NewWriter(&buf).WriteFrame(frame); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	got, err := NewReader(&buf).ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if got.Type != TypeSpawn {
		t.Fatalf("Type = %q, want %q", got.Type, TypeSpawn)
	}
	var spawn Spawn
	if err := got.DecodePayload(&spawn); err != nil {
		t.Fatalf("DecodePayload() error = %v", err)
	}
	if spawn.Command != "Write-Output ok" || !spawn.StdinOpen || spawn.Timeout != time.Second {
		t.Fatalf("spawn = %+v", spawn)
	}
	if len(spawn.CapabilitySID) != 1 || spawn.CapabilitySID[0] != "S-1-5-32-1" {
		t.Fatalf("CapabilitySID = %#v", spawn.CapabilitySID)
	}
}

func TestReaderRejectsOversizedFrame(t *testing.T) {
	var buf bytes.Buffer
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], MaxFrameSize+1)
	buf.Write(header[:])
	if _, err := NewReader(&buf).ReadFrame(); err == nil || !strings.Contains(err.Error(), "invalid frame size") {
		t.Fatalf("ReadFrame() error = %v, want invalid frame size", err)
	}
}

func TestReaderRejectsVersionMismatch(t *testing.T) {
	var buf bytes.Buffer
	if err := NewWriter(&buf).WriteFrame(Frame{Version: 99, Type: TypeHello}); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	if _, err := NewReader(&buf).ReadFrame(); err == nil || !strings.Contains(err.Error(), "unsupported protocol version") {
		t.Fatalf("ReadFrame() error = %v, want version mismatch", err)
	}
}
