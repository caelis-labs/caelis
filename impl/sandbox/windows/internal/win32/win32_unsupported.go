//go:build !windows

package win32

import (
	"fmt"
	"io"
	"runtime"
)

type LogonCredentials struct {
	Username string
	Domain   string
	Password string
}

type CapabilitySIDs struct {
	Group      []string
	Capability []string
}

type Process struct{}

type Token uintptr

type ExitError struct {
	ExitCode int
}

func (e ExitError) Error() string {
	return fmt.Sprintf("process exited with code %d", e.ExitCode)
}

type ElevatedLaunchCanceledError struct {
	File string
	Err  error
}

func (e ElevatedLaunchCanceledError) Error() string {
	return "elevated launch canceled by the user"
}

func (e ElevatedLaunchCanceledError) Unwrap() error {
	return e.Err
}

func IsElevated() (bool, error) {
	return false, fmt.Errorf("win32: unsupported on %s", runtime.GOOS)
}

func ShellExecuteRunAs(string, string, string) error {
	return fmt.Errorf("win32: ShellExecute runas unsupported on %s", runtime.GOOS)
}

func RunElevatedAndWait(string, []string, string) error {
	return fmt.Errorf("win32: elevated process launch unsupported on %s", runtime.GOOS)
}

func LookupAccountSIDString(string) (string, error) {
	return "", fmt.Errorf("win32: account SID lookup unsupported on %s", runtime.GOOS)
}

func RestrictedCurrentProcessToken() (Token, error) {
	return 0, fmt.Errorf("win32: restricted tokens unsupported on %s", runtime.GOOS)
}

func RestrictedCurrentProcessTokenWithSIDs([]string) (Token, error) {
	return 0, fmt.Errorf("win32: restricted tokens unsupported on %s", runtime.GOOS)
}

func (t Token) Close() error {
	return nil
}

func DeriveCapabilitySIDs(string) (CapabilitySIDs, error) {
	return CapabilitySIDs{}, fmt.Errorf("win32: capability SID derivation unsupported on %s", runtime.GOOS)
}

func AllowNullDeviceForSIDs([]string) error {
	return fmt.Errorf("win32: null device ACL unsupported on %s", runtime.GOOS)
}

func ValidateCredentials(LogonCredentials) error {
	return fmt.Errorf("win32: credential validation unsupported on %s", runtime.GOOS)
}

func StartProcessWithLogon(LogonCredentials, string, []string, string) (*Process, error) {
	return nil, fmt.Errorf("win32: CreateProcessWithLogon unsupported on %s", runtime.GOOS)
}

func StartProcessAsUser(Token, string, []string, string, []string) (*Process, error) {
	return nil, fmt.Errorf("win32: CreateProcessAsUser unsupported on %s", runtime.GOOS)
}

func (p *Process) PID() int {
	return 0
}

func (p *Process) Stdin() io.WriteCloser {
	return nil
}

func (p *Process) Stdout() io.Reader {
	return nil
}

func (p *Process) Stderr() io.Reader {
	return nil
}

func (p *Process) Wait() error {
	return nil
}

func (p *Process) Kill() error {
	return nil
}

func ProtectData([]byte, string) ([]byte, error) {
	return nil, fmt.Errorf("win32: DPAPI unsupported on %s", runtime.GOOS)
}

func ProtectMachineData([]byte, string) ([]byte, error) {
	return nil, fmt.Errorf("win32: DPAPI unsupported on %s", runtime.GOOS)
}

func UnprotectData([]byte) ([]byte, error) {
	return nil, fmt.Errorf("win32: DPAPI unsupported on %s", runtime.GOOS)
}

func ProtectString(string, string) (string, error) {
	return "", fmt.Errorf("win32: DPAPI unsupported on %s", runtime.GOOS)
}

func ProtectMachineString(string, string) (string, error) {
	return "", fmt.Errorf("win32: DPAPI unsupported on %s", runtime.GOOS)
}

func UnprotectString(string) (string, error) {
	return "", fmt.Errorf("win32: DPAPI unsupported on %s", runtime.GOOS)
}
