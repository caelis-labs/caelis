//go:build !windows

package consoleoutput

func DecodeConsoleOutputToUTF8(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	return append([]byte(nil), data...), nil
}
