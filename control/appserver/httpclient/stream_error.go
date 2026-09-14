package httpclient

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

// classifyStreamReadError distinguishes lost observations from invalid wire
// data. Only an explicit done event is a clean end; cancellation never asks a
// consumer to reconnect an observation it deliberately closed.
func classifyStreamReadError(source string, err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	prefix := "control http client: " + source
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errorcode.New(errorcode.Unavailable, prefix+" ended without a done event")
	}
	if errors.Is(err, bufio.ErrTooLong) {
		return errorcode.New(errorcode.InvalidArgument, prefix+" frame is too large")
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return errorcode.New(errorcode.Unavailable, prefix+" transport interrupted")
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "connection reset") ||
		strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "use of closed network connection") {
		return errorcode.New(errorcode.Unavailable, prefix+" transport interrupted")
	}
	return err
}
