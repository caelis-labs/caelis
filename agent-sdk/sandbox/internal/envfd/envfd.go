// Package envfd transfers private command payloads to pre-sandbox launchers
// without placing inherited environment values in their argv or environment.
package envfd

import (
	"bytes"
	"fmt"
	"os"
	"strings"
)

// Open returns a pipe read end suitable for exec.Cmd.ExtraFiles. The producer
// writes asynchronously so large environments cannot block process startup.
// Close the returned file after Start/Run (including failure): the child owns
// its dup after a successful start; a failed start makes the writer exit on EPIPE.
func Open(data []byte) (*os.File, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	go func() {
		defer writer.Close()
		_, _ = writer.Write(data)
	}()
	return reader, nil
}

// PackNUL packs option tokens for bwrap --args FD. No secret token is copied
// into a process argv; the FD must be attached at descriptor number three.
func PackNUL(args []string) ([]byte, error) {
	var payload bytes.Buffer
	for _, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return nil, fmt.Errorf("sandbox: launch argument contains NUL")
		}
		payload.WriteString(arg)
		payload.WriteByte(0)
	}
	return payload.Bytes(), nil
}
