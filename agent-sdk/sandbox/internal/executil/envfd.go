// Package executil provides internal command-execution, output-wait, and
// process-group helpers shared across sandbox backends.
package executil

import (
	"bytes"
	"fmt"
	"os"
	"strings"
)

// OpenPipe returns a pipe read end suitable for exec.Cmd.ExtraFiles. The
// producer writes asynchronously so large environments cannot block process
// startup. Close the returned file after Start/Run (including failure).
func OpenPipe(data []byte) (*os.File, error) {
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

// PackNUL packs option tokens for bwrap --args FD without exposing secret
// tokens in process argv.
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
