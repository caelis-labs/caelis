package acp

import (
	"fmt"
	"io"
	"os"

	sdkstdio "github.com/caelis-labs/acp-go-sdk/transport/stdio"
)

// sdkOwnedServerInput gives the SDK an independently closable handle for file
// inputs such as process stdin. Non-file readers retain the existing ownership
// transfer contract so their Close method can still interrupt a blocked read.
func sdkOwnedServerInput(in io.Reader) (io.Reader, func(), error) {
	file, ok := in.(*os.File)
	if !ok {
		return in, func() {}, nil
	}
	duplicate, err := sdkstdio.DuplicateFile(file)
	if err != nil {
		return nil, nil, fmt.Errorf("acp: duplicate server input: %w", err)
	}
	return duplicate, func() { _ = duplicate.Close() }, nil
}

// sdkOwnedServerOutput gives the SDK an independently closable handle for file
// outputs such as process stdout. This lets connection shutdown interrupt a
// blocked write without closing the caller's process-level descriptor.
// Non-file writers retain the existing ownership transfer contract.
func sdkOwnedServerOutput(out io.Writer) (io.Writer, func(), error) {
	file, ok := out.(*os.File)
	if !ok {
		return out, func() {}, nil
	}
	duplicate, err := sdkstdio.DuplicateFile(file)
	if err != nil {
		return nil, nil, fmt.Errorf("acp: duplicate server output: %w", err)
	}
	return duplicate, func() { _ = duplicate.Close() }, nil
}
