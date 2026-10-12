package processtest

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

func exited(pid int) (bool, error) {
	return exitedWithStatReader(pid, os.ReadFile)
}

func exitedWithStatReader(pid int, readFile func(string) ([]byte, error)) (bool, error) {
	if err := syscall.Kill(pid, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return true, nil
		}
		return false, err
	}
	data, err := readFile(fmt.Sprintf("/proc/%d/stat", pid))
	if os.IsNotExist(err) {
		// The process may have exited between probes, but a missing /proc
		// mount or a different PID view must not hide a live process.
		if probeErr := syscall.Kill(pid, 0); errors.Is(probeErr, syscall.ESRCH) {
			return true, nil
		}
	}
	if err != nil {
		return false, err
	}
	// The command field is parenthesized and may itself contain spaces or ')'.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return false, fmt.Errorf("invalid process stat: missing command")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) == 0 || len(fields[0]) != 1 {
		return false, fmt.Errorf("invalid process stat: missing state")
	}
	return fields[0] == "Z" || fields[0] == "X", nil
}
