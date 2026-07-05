//go:build !windows

package host

import (
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/consoleoutput"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/textstream"
)

// hostOutputDecoder preserves raw bytes in non-Windows host storage and uses a
// UTF-8 decoder only for live callbacks.
type hostOutputDecoder struct {
	decoder textstream.UTF8Decoder
}

// Decode preserves raw pipe bytes in non-Windows host session storage so
// Result()/ReadOutput keep their historical shell-byte semantics. Emit is
// separately decoded to valid UTF-8 for live callbacks.
func (d *hostOutputDecoder) Decode(chunk []byte) consoleoutput.StreamChunk {
	return consoleoutput.StreamChunk{
		Stored: append([]byte(nil), chunk...),
		Emit:   []byte(d.decoder.Decode(chunk)),
	}
}

// Flush only emits pending decoded text. Raw storage already received every
// pipe byte during Decode, so there is no additional stored tail.
func (d *hostOutputDecoder) Flush() consoleoutput.StreamChunk {
	return consoleoutput.StreamChunk{Emit: []byte(d.decoder.Flush())}
}

func newCappedOutputBuffer(max int) *consoleoutput.RawCappedBuffer {
	return consoleoutput.NewRawCappedBuffer(max)
}
