//go:build windows

package host

import "github.com/caelis-labs/caelis/agent-sdk/sandbox/consoleoutput"

// hostOutputDecoder stores normalized console text on Windows because
// PowerShell pipe output can contain CLIXML, UTF-16, and codepage-specific
// bytes that should not become durable terminal output.
type hostOutputDecoder struct {
	decoder consoleoutput.ConsoleOutputDecoder
}

func (d *hostOutputDecoder) Decode(chunk []byte) consoleoutput.StreamChunk {
	return consoleoutput.DecodeStreamChunk(&d.decoder, chunk, consoleoutput.StoreDecoded)
}

func (d *hostOutputDecoder) Flush() consoleoutput.StreamChunk {
	return consoleoutput.FlushStreamChunk(&d.decoder, consoleoutput.StoreDecoded)
}

func (*hostOutputDecoder) committedCursor(total int64) int64 {
	return max(total, 0)
}

func newCappedOutputBuffer(max int) *consoleoutput.CappedBuffer {
	return consoleoutput.NewCappedBuffer(max)
}
