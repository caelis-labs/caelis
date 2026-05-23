//go:build windows

package win32

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	SWHide                  = 0
	SWNormal                = 1
	logonWithProfile        = 0x00000001
	logon32LogonInteractive = 2
	logon32ProviderDefault  = 0
	seeMaskNoCloseProcess   = 0x00000040
	disableMaxPrivilege     = 0x00000001
	luaToken                = 0x00000004
	writeRestricted         = 0x00000008
	codePageUTF8            = 65001
)

type tokenDefaultDACL struct {
	DefaultDacl *windows.ACL
}

var (
	procCreateProcessWithLogonW = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateProcessWithLogonW")
	procCreateRestrictedToken   = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")
	procDeriveCapabilitySIDs    = windows.NewLazySystemDLL("kernelbase.dll").NewProc("DeriveCapabilitySidsFromName")
	procLogonUserW              = windows.NewLazySystemDLL("advapi32.dll").NewProc("LogonUserW")
	procShellExecuteExW         = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")
)

type Token = windows.Token

type LogonCredentials struct {
	Username string
	Domain   string
	Password string
}

type LogonProcessOptions struct {
	LoadProfile bool
	Env         []string
}

type CapabilitySIDs struct {
	Group      []string
	Capability []string
}

type Process struct {
	pid           int
	processHandle windows.Handle
	stdin         *os.File
	stdout        *os.File
	stderr        *os.File

	waitOnce sync.Once
	waitErr  error
}

type shellExecuteInfo struct {
	Size       uint32
	Mask       uint32
	Hwnd       windows.Handle
	Verb       *uint16
	File       *uint16
	Parameters *uint16
	Directory  *uint16
	Show       int32
	InstApp    windows.Handle
	IDList     uintptr
	Class      *uint16
	KeyClass   windows.Handle
	HotKey     uint32
	Icon       windows.Handle
	Process    windows.Handle
}

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
	if strings.TrimSpace(e.File) == "" {
		return "elevated launch canceled by the user"
	}
	return fmt.Sprintf("elevated launch canceled by the user for %s", e.File)
}

func (e ElevatedLaunchCanceledError) Unwrap() error {
	return e.Err
}

func IsElevated() (bool, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return false, err
	}
	defer token.Close()
	return token.IsElevated(), nil
}

func WithNamedMutex(ctx context.Context, name string, timeout time.Duration, fn func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("win32: mutex name is required")
	}
	if timeout > 0 {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
	}
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	handle, err := windows.CreateMutex(nil, false, namePtr)
	if handle == 0 {
		return fmt.Errorf("create mutex %s: %w", name, err)
	}
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		closeHandle(handle)
		return fmt.Errorf("create mutex %s: %w", name, err)
	}
	defer closeHandle(handle)
	for {
		waitResult, err := windows.WaitForSingleObject(handle, 250)
		if err != nil {
			return fmt.Errorf("wait mutex %s: %w", name, err)
		}
		switch waitResult {
		case windows.WAIT_OBJECT_0, windows.WAIT_ABANDONED:
			defer func() {
				_ = windows.ReleaseMutex(handle)
			}()
			if fn == nil {
				return nil
			}
			return fn()
		case uint32(windows.WAIT_TIMEOUT):
			select {
			case <-ctx.Done():
				return fmt.Errorf("wait mutex %s: %w", name, ctx.Err())
			default:
			}
		default:
			return fmt.Errorf("wait mutex %s: unexpected wait result %d", name, waitResult)
		}
	}
}

func ShellExecuteRunAs(file string, args string, cwd string) error {
	verbPtr, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	filePtr, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	argsPtr, err := windows.UTF16PtrFromString(args)
	if err != nil {
		return err
	}
	var cwdPtr *uint16
	if strings.TrimSpace(cwd) != "" {
		cwdPtr, err = windows.UTF16PtrFromString(cwd)
		if err != nil {
			return err
		}
	}
	return windows.ShellExecute(0, verbPtr, filePtr, argsPtr, cwdPtr, SWNormal)
}

func RunElevatedAndWait(file string, args []string, cwd string) error {
	return RunElevatedAndWaitContext(context.Background(), file, args, cwd)
}

func RunElevatedAndWaitContext(ctx context.Context, file string, args []string, cwd string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	file = strings.TrimSpace(file)
	if file == "" {
		return fmt.Errorf("run elevated: file is required")
	}
	verbPtr, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	filePtr, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	argsPtr, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		return err
	}
	var cwdPtr *uint16
	if strings.TrimSpace(cwd) != "" {
		cwdPtr, err = windows.UTF16PtrFromString(cwd)
		if err != nil {
			return err
		}
	}
	info := shellExecuteInfo{
		Size:       uint32(unsafe.Sizeof(shellExecuteInfo{})),
		Mask:       seeMaskNoCloseProcess,
		Verb:       verbPtr,
		File:       filePtr,
		Parameters: argsPtr,
		Directory:  cwdPtr,
		Show:       SWHide,
	}
	process, err := shellExecuteRunAsContext(ctx, file, &info)
	if err != nil {
		return err
	}
	defer closeHandle(process)
	for {
		waitResult, err := windows.WaitForSingleObject(process, 250)
		if err != nil {
			return fmt.Errorf("wait elevated %s: %w", file, err)
		}
		if waitResult == windows.WAIT_OBJECT_0 {
			break
		}
		if waitResult != uint32(windows.WAIT_TIMEOUT) {
			return fmt.Errorf("wait elevated %s: unexpected wait result %d", file, waitResult)
		}
		select {
		case <-ctx.Done():
			_ = windows.TerminateProcess(process, 1)
			return ctx.Err()
		default:
		}
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(process, &exitCode); err != nil {
		return fmt.Errorf("read elevated exit code %s: %w", file, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("elevated setup exited with code %d", exitCode)
	}
	return nil
}

func shellExecuteRunAsContext(ctx context.Context, file string, info *shellExecuteInfo) (windows.Handle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if info == nil {
		return 0, fmt.Errorf("ShellExecuteExW runas %s: missing launch info", file)
	}
	type launchResult struct {
		process windows.Handle
		err     error
	}
	launchDone := make(chan launchResult, 1)
	abandoned := make(chan struct{})
	go func() {
		r1, _, callErr := syscall.SyscallN(procShellExecuteExW.Addr(), uintptr(unsafe.Pointer(info)))
		result := launchResult{}
		if r1 == 0 {
			if callErr == windows.ERROR_CANCELLED {
				result.err = ElevatedLaunchCanceledError{File: file, Err: callErr}
			} else {
				result.err = fmt.Errorf("ShellExecuteExW runas %s: %w", file, callErr)
			}
		} else if info.Process == 0 {
			result.err = fmt.Errorf("ShellExecuteExW runas %s did not return a process handle", file)
		} else {
			result.process = info.Process
		}
		select {
		case <-abandoned:
			if result.process != 0 {
				_ = windows.TerminateProcess(result.process, 1)
				closeHandle(result.process)
			}
		case launchDone <- result:
		}
	}()
	select {
	case result := <-launchDone:
		return result.process, result.err
	case <-ctx.Done():
		close(abandoned)
		return 0, ctx.Err()
	}
}

func LookupAccountSIDString(account string) (string, error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return "", fmt.Errorf("win32: account name is required")
	}
	sid, _, _, err := windows.LookupSID("", account)
	if err != nil {
		return "", err
	}
	if sid == nil {
		return "", fmt.Errorf("win32: account %q has no SID", account)
	}
	value := sid.String()
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("win32: account %q has empty SID", account)
	}
	return value, nil
}

func RestrictedCurrentProcessToken() (Token, error) {
	return RestrictedCurrentProcessTokenWithSIDs(nil)
}

func RestrictedCurrentProcessTokenWithSIDs(restrictingSIDs []string) (Token, error) {
	var current windows.Token
	err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_DUPLICATE|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_QUERY|windows.TOKEN_ADJUST_DEFAULT|windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_ADJUST_SESSIONID,
		&current,
	)
	if err != nil {
		return 0, err
	}
	defer current.Close()
	restricting, capabilitySIDs, daclSIDs, err := restrictingSIDAttributes(current, restrictingSIDs)
	if err != nil {
		return 0, err
	}
	var restrictingPtr uintptr
	if len(restricting) > 0 {
		restrictingPtr = uintptr(unsafe.Pointer(&restricting[0]))
	}
	var restricted windows.Token
	flags := uintptr(disableMaxPrivilege)
	if len(restricting) > 0 {
		flags |= luaToken | writeRestricted
	}
	r1, _, callErr := syscall.SyscallN(
		procCreateRestrictedToken.Addr(),
		uintptr(current),
		flags,
		0,
		0,
		0,
		0,
		uintptr(len(restricting)),
		restrictingPtr,
		uintptr(unsafe.Pointer(&restricted)),
	)
	runtime.KeepAlive(restricting)
	runtime.KeepAlive(capabilitySIDs)
	runtime.KeepAlive(daclSIDs)
	if r1 == 0 {
		return 0, callErr
	}
	if len(daclSIDs) > 0 {
		if err := setDefaultDACL(restricted, daclSIDs); err != nil {
			_ = restricted.Close()
			return 0, err
		}
		if err := enableSinglePrivilege(restricted, "SeChangeNotifyPrivilege"); err != nil {
			_ = restricted.Close()
			return 0, err
		}
	}
	return restricted, nil
}

func DeriveCapabilitySIDs(name string) (CapabilitySIDs, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return CapabilitySIDs{}, fmt.Errorf("win32: capability name is required")
	}
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return CapabilitySIDs{}, err
	}
	var groupArray uintptr
	var groupCount uint32
	var capabilityArray uintptr
	var capabilityCount uint32
	r1, _, callErr := syscall.SyscallN(
		procDeriveCapabilitySIDs.Addr(),
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(&groupArray)),
		uintptr(unsafe.Pointer(&groupCount)),
		uintptr(unsafe.Pointer(&capabilityArray)),
		uintptr(unsafe.Pointer(&capabilityCount)),
	)
	if r1 == 0 {
		return CapabilitySIDs{}, fmt.Errorf("DeriveCapabilitySidsFromName %q: %w", name, callErr)
	}
	defer freeSIDArray(groupArray, groupCount)
	defer freeSIDArray(capabilityArray, capabilityCount)
	groups, err := sidArrayStrings(groupArray, groupCount)
	if err != nil {
		return CapabilitySIDs{}, err
	}
	capabilities, err := sidArrayStrings(capabilityArray, capabilityCount)
	if err != nil {
		return CapabilitySIDs{}, err
	}
	return CapabilitySIDs{Group: groups, Capability: capabilities}, nil
}

func AllowNullDeviceForSIDs(sids []string) error {
	if len(sids) == 0 {
		return nil
	}
	name, err := windows.UTF16PtrFromString(`\\.\NUL`)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(
		name,
		windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return err
	}
	defer closeHandle(handle)
	sd, err := windows.GetSecurityInfo(handle, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(sids))
	keepAlive := make([]*windows.SID, 0, len(sids))
	for _, value := range sids {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		sid, err := windows.StringToSid(value)
		if err != nil {
			return fmt.Errorf("win32: parse null-device SID %q: %w", value, err)
		}
		keepAlive = append(keepAlive, sid)
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_GENERIC_EXECUTE,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	if len(entries) == 0 {
		return nil
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	next, err := windows.ACLFromEntries(entries, dacl)
	runtime.KeepAlive(keepAlive)
	if err != nil {
		return err
	}
	if err := windows.SetSecurityInfo(handle, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, next, nil); err != nil {
		return err
	}
	runtime.KeepAlive(next)
	return nil
}

func ValidateCredentials(creds LogonCredentials) error {
	username, domain := splitDomainUser(creds.Username, creds.Domain)
	if strings.TrimSpace(username) == "" {
		return fmt.Errorf("win32: logon username is required")
	}
	if creds.Password == "" {
		return fmt.Errorf("win32: logon password is required")
	}
	userPtr, err := windows.UTF16PtrFromString(username)
	if err != nil {
		return err
	}
	var domainPtr *uint16
	if strings.TrimSpace(domain) != "" {
		domainPtr, err = windows.UTF16PtrFromString(domain)
		if err != nil {
			return err
		}
	}
	passwordPtr, err := windows.UTF16PtrFromString(creds.Password)
	if err != nil {
		return err
	}
	var token windows.Handle
	r1, _, callErr := syscall.SyscallN(
		procLogonUserW.Addr(),
		ptr(userPtr),
		ptr(domainPtr),
		ptr(passwordPtr),
		uintptr(logon32LogonInteractive),
		uintptr(logon32ProviderDefault),
		uintptr(unsafe.Pointer(&token)),
	)
	if r1 == 0 {
		display := username
		if strings.TrimSpace(domain) != "" {
			display = domain + `\` + username
		}
		return fmt.Errorf("LogonUserW %s: %w", display, callErr)
	}
	closeHandle(token)
	return nil
}

func StartProcessWithLogon(creds LogonCredentials, executable string, args []string, cwd string, options ...LogonProcessOptions) (*Process, error) {
	opt := LogonProcessOptions{}
	if len(options) > 0 {
		opt = options[0]
	}
	username, domain := splitDomainUser(creds.Username, creds.Domain)
	if strings.TrimSpace(username) == "" {
		return nil, fmt.Errorf("win32: logon username is required")
	}
	if creds.Password == "" {
		return nil, fmt.Errorf("win32: logon password is required")
	}
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return nil, fmt.Errorf("win32: executable path is required")
	}

	userPtr, err := windows.UTF16PtrFromString(username)
	if err != nil {
		return nil, err
	}
	var domainPtr *uint16
	if strings.TrimSpace(domain) != "" {
		domainPtr, err = windows.UTF16PtrFromString(domain)
		if err != nil {
			return nil, err
		}
	}
	passwordPtr, err := windows.UTF16PtrFromString(creds.Password)
	if err != nil {
		return nil, err
	}
	executablePtr, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return nil, err
	}
	commandLine := windows.ComposeCommandLine(append([]string{executable}, args...))
	commandLinePtr, err := windows.UTF16PtrFromString(commandLine)
	if err != nil {
		return nil, err
	}
	var cwdPtr *uint16
	if strings.TrimSpace(cwd) != "" {
		cwdPtr, err = windows.UTF16PtrFromString(cwd)
		if err != nil {
			return nil, err
		}
	}
	envBlock, err := environmentBlock(opt.Env)
	if err != nil {
		return nil, err
	}
	var envPtr *uint16
	if len(envBlock) > 0 {
		envPtr = &envBlock[0]
	}

	stdinRead, stdinWrite, err := createChildPipe(parentWrites)
	if err != nil {
		return nil, err
	}
	stdoutRead, stdoutWrite, err := createChildPipe(parentReads)
	if err != nil {
		closeHandle(stdinRead)
		closeHandle(stdinWrite)
		return nil, err
	}
	stderrRead, stderrWrite, err := createChildPipe(parentReads)
	if err != nil {
		closeHandle(stdinRead)
		closeHandle(stdinWrite)
		closeHandle(stdoutRead)
		closeHandle(stdoutWrite)
		return nil, err
	}

	startupInfo := logonStartupInfo(stdinRead, stdoutWrite, stderrWrite)
	var processInfo windows.ProcessInformation
	flags := logonCreationFlags()
	logonFlags := logonFlagsForOptions(opt)
	r1, _, callErr := syscall.SyscallN(
		procCreateProcessWithLogonW.Addr(),
		ptr(userPtr),
		ptr(domainPtr),
		ptr(passwordPtr),
		logonFlags,
		ptr(executablePtr),
		ptr(commandLinePtr),
		uintptr(flags),
		ptr(envPtr),
		ptr(cwdPtr),
		uintptr(unsafe.Pointer(startupInfo)),
		uintptr(unsafe.Pointer(&processInfo)),
	)
	runtime.KeepAlive(envBlock)
	if r1 == 0 {
		closeHandle(stdinRead)
		closeHandle(stdinWrite)
		closeHandle(stdoutRead)
		closeHandle(stdoutWrite)
		closeHandle(stderrRead)
		closeHandle(stderrWrite)
		return nil, fmt.Errorf("CreateProcessWithLogonW %s: %w", executable, callErr)
	}
	closeHandle(stdinRead)
	closeHandle(stdoutWrite)
	closeHandle(stderrWrite)
	closeHandle(processInfo.Thread)

	return &Process{
		pid:           int(processInfo.ProcessId),
		processHandle: processInfo.Process,
		stdin:         os.NewFile(uintptr(stdinWrite), "caelis-logon-stdin"),
		stdout:        os.NewFile(uintptr(stdoutRead), "caelis-logon-stdout"),
		stderr:        os.NewFile(uintptr(stderrRead), "caelis-logon-stderr"),
	}, nil
}

func StartProcessAsUser(token Token, executable string, args []string, cwd string, env []string) (*Process, error) {
	if token == 0 {
		return nil, fmt.Errorf("win32: token is required")
	}
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return nil, fmt.Errorf("win32: executable path is required")
	}
	executablePtr, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return nil, err
	}
	commandLine := windows.ComposeCommandLine(append([]string{executable}, args...))
	commandLinePtr, err := windows.UTF16PtrFromString(commandLine)
	if err != nil {
		return nil, err
	}
	var cwdPtr *uint16
	if strings.TrimSpace(cwd) != "" {
		cwdPtr, err = windows.UTF16PtrFromString(cwd)
		if err != nil {
			return nil, err
		}
	}
	envBlock, err := environmentBlock(env)
	if err != nil {
		return nil, err
	}
	var envPtr *uint16
	if len(envBlock) > 0 {
		envPtr = &envBlock[0]
	}

	stdinRead, stdinWrite, err := createChildPipe(parentWrites)
	if err != nil {
		return nil, err
	}
	stdoutRead, stdoutWrite, err := createChildPipe(parentReads)
	if err != nil {
		closeHandle(stdinRead)
		closeHandle(stdinWrite)
		return nil, err
	}
	stderrRead, stderrWrite, err := createChildPipe(parentReads)
	if err != nil {
		closeHandle(stdinRead)
		closeHandle(stdinWrite)
		closeHandle(stdoutRead)
		closeHandle(stdoutWrite)
		return nil, err
	}

	startupInfo, cleanupStartupInfo, err := childStartupInfo(stdinRead, stdoutWrite, stderrWrite)
	if err != nil {
		closeHandle(stdinRead)
		closeHandle(stdinWrite)
		closeHandle(stdoutRead)
		closeHandle(stdoutWrite)
		closeHandle(stderrRead)
		closeHandle(stderrWrite)
		return nil, err
	}
	defer cleanupStartupInfo()
	var processInfo windows.ProcessInformation
	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW | windows.EXTENDED_STARTUPINFO_PRESENT)
	err = windows.CreateProcessAsUser(
		token,
		executablePtr,
		commandLinePtr,
		nil,
		nil,
		true,
		flags,
		envPtr,
		cwdPtr,
		&startupInfo.StartupInfo,
		&processInfo,
	)
	runtime.KeepAlive(envBlock)
	if err != nil {
		closeHandle(stdinRead)
		closeHandle(stdinWrite)
		closeHandle(stdoutRead)
		closeHandle(stdoutWrite)
		closeHandle(stderrRead)
		closeHandle(stderrWrite)
		return nil, fmt.Errorf("CreateProcessAsUser %s: %w", executable, err)
	}
	closeHandle(stdinRead)
	closeHandle(stdoutWrite)
	closeHandle(stderrWrite)
	closeHandle(processInfo.Thread)

	return &Process{
		pid:           int(processInfo.ProcessId),
		processHandle: processInfo.Process,
		stdin:         os.NewFile(uintptr(stdinWrite), "caelis-token-stdin"),
		stdout:        os.NewFile(uintptr(stdoutRead), "caelis-token-stdout"),
		stderr:        os.NewFile(uintptr(stderrRead), "caelis-token-stderr"),
	}, nil
}

func (p *Process) PID() int {
	if p == nil {
		return 0
	}
	return p.pid
}

func (p *Process) Stdin() io.WriteCloser {
	if p == nil {
		return nil
	}
	return p.stdin
}

func (p *Process) Stdout() io.Reader {
	if p == nil {
		return nil
	}
	return p.stdout
}

func (p *Process) Stderr() io.Reader {
	if p == nil {
		return nil
	}
	return p.stderr
}

func (p *Process) Wait() error {
	if p == nil {
		return nil
	}
	p.waitOnce.Do(func() {
		_, err := windows.WaitForSingleObject(p.processHandle, windows.INFINITE)
		if err != nil {
			p.waitErr = err
			return
		}
		var exitCode uint32
		if err := windows.GetExitCodeProcess(p.processHandle, &exitCode); err != nil {
			p.waitErr = err
			return
		}
		p.closeFiles()
		closeHandle(p.processHandle)
		p.processHandle = 0
		if exitCode != 0 {
			p.waitErr = ExitError{ExitCode: int(exitCode)}
		}
	})
	return p.waitErr
}

func (p *Process) Kill() error {
	if p == nil || p.processHandle == 0 {
		return nil
	}
	return windows.TerminateProcess(p.processHandle, 1)
}

func (p *Process) closeFiles() {
	if p.stdin != nil {
		_ = p.stdin.Close()
		p.stdin = nil
	}
	if p.stdout != nil {
		_ = p.stdout.Close()
		p.stdout = nil
	}
	if p.stderr != nil {
		_ = p.stderr.Close()
		p.stderr = nil
	}
}

func ProtectData(data []byte, name string) ([]byte, error) {
	return protectData(data, name, 0)
}

func ProtectMachineData(data []byte, name string) ([]byte, error) {
	return protectData(data, name, windows.CRYPTPROTECT_LOCAL_MACHINE)
}

func protectData(data []byte, name string, flags uint32) ([]byte, error) {
	in := dataBlob(data)
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, namePtr, nil, 0, nil, flags, &out); err != nil {
		return nil, err
	}
	defer localFree(windows.Handle(unsafe.Pointer(out.Data)))
	return blobBytes(out), nil
}

func UnprotectData(data []byte) ([]byte, error) {
	in := dataBlob(data)
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, err
	}
	defer localFree(windows.Handle(unsafe.Pointer(out.Data)))
	return blobBytes(out), nil
}

func ProtectString(value string, name string) (string, error) {
	data, err := ProtectData([]byte(value), name)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func ProtectMachineString(value string, name string) (string, error) {
	data, err := ProtectMachineData([]byte(value), name)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func UnprotectString(value string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	plain, err := UnprotectData(data)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func dataBlob(data []byte) windows.DataBlob {
	if len(data) == 0 {
		return windows.DataBlob{}
	}
	return windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
}

func blobBytes(blob windows.DataBlob) []byte {
	if blob.Size == 0 || blob.Data == nil {
		return nil
	}
	return append([]byte(nil), unsafe.Slice(blob.Data, int(blob.Size))...)
}

func environmentBlock(env []string) ([]uint16, error) {
	if len(env) == 0 {
		return nil, nil
	}
	var builder strings.Builder
	for _, item := range env {
		if strings.TrimSpace(item) == "" {
			continue
		}
		builder.WriteString(item)
		builder.WriteByte(0)
	}
	builder.WriteByte(0)
	return utf16.Encode([]rune(builder.String())), nil
}

func DecodeConsoleOutputToUTF8(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if utf8.Valid(data) {
		return append([]byte(nil), data...), nil
	}
	codePage, err := windows.GetConsoleOutputCP()
	if err != nil || codePage == 0 || codePage == codePageUTF8 {
		codePage = windows.GetACP()
	}
	decoded, err := decodeCodePageToUTF8(codePage, data)
	if err == nil {
		return decoded, nil
	}
	ansiCodePage := windows.GetACP()
	if ansiCodePage != 0 && ansiCodePage != codePage {
		return decodeCodePageToUTF8(ansiCodePage, data)
	}
	return nil, err
}

func decodeCodePageToUTF8(codePage uint32, data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if codePage == 0 {
		codePage = windows.GetACP()
	}
	n, err := windows.MultiByteToWideChar(codePage, 0, &data[0], int32(len(data)), nil, 0)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	wide := make([]uint16, n)
	n, err = windows.MultiByteToWideChar(codePage, 0, &data[0], int32(len(data)), &wide[0], n)
	if err != nil {
		return nil, err
	}
	return []byte(string(utf16.Decode(wide[:n]))), nil
}

func restrictingSIDAttributes(token windows.Token, values []string) ([]windows.SIDAndAttributes, []*windows.SID, []*windows.SID, error) {
	if len(values) == 0 {
		return nil, nil, nil, nil
	}
	capabilitySIDs := make([]*windows.SID, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		sid, err := windows.StringToSid(value)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("win32: parse restricting SID %q: %w", value, err)
		}
		capabilitySIDs = append(capabilitySIDs, sid)
	}
	if len(capabilitySIDs) == 0 {
		return nil, nil, nil, nil
	}
	userSID, err := tokenUserSID(token)
	if err != nil {
		return nil, nil, nil, err
	}
	logonSID, err := tokenLogonSID(token)
	if err != nil {
		return nil, nil, nil, err
	}
	everyoneSID, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		return nil, nil, nil, err
	}
	all := make([]*windows.SID, 0, len(capabilitySIDs)+3)
	all = append(all, capabilitySIDs...)
	all = append(all, userSID, logonSID, everyoneSID)
	out := make([]windows.SIDAndAttributes, 0, len(all))
	for _, sid := range all {
		out = append(out, windows.SIDAndAttributes{Sid: sid})
	}
	daclSIDs := make([]*windows.SID, 0, len(capabilitySIDs)+2)
	daclSIDs = append(daclSIDs, logonSID, everyoneSID)
	daclSIDs = append(daclSIDs, capabilitySIDs...)
	return out, capabilitySIDs, daclSIDs, nil
}

func tokenUserSID(token windows.Token) (*windows.SID, error) {
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("win32: get token user SID: %w", err)
	}
	sid, err := user.User.Sid.Copy()
	if err != nil {
		return nil, fmt.Errorf("win32: copy token user SID: %w", err)
	}
	return sid, nil
}

func tokenLogonSID(token windows.Token) (*windows.SID, error) {
	groups, err := token.GetTokenGroups()
	if err != nil {
		return nil, fmt.Errorf("win32: get token groups: %w", err)
	}
	for _, group := range groups.AllGroups() {
		if group.Attributes&windows.SE_GROUP_LOGON_ID != windows.SE_GROUP_LOGON_ID {
			continue
		}
		sid, err := group.Sid.Copy()
		if err != nil {
			return nil, fmt.Errorf("win32: copy token logon SID: %w", err)
		}
		return sid, nil
	}
	return nil, fmt.Errorf("win32: token logon SID not found")
}

func setDefaultDACL(token windows.Token, sids []*windows.SID) error {
	if len(sids) == 0 {
		return nil
	}
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(sids))
	for _, sid := range sids {
		if sid == nil {
			continue
		}
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	if len(entries) == 0 {
		return nil
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("win32: build token default DACL: %w", err)
	}
	info := tokenDefaultDACL{DefaultDacl: acl}
	if err := windows.SetTokenInformation(token, windows.TokenDefaultDacl, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return fmt.Errorf("win32: set token default DACL: %w", err)
	}
	runtime.KeepAlive(entries)
	runtime.KeepAlive(sids)
	runtime.KeepAlive(acl)
	return nil
}

func enableSinglePrivilege(token windows.Token, name string) error {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, namePtr, &luid); err != nil {
		return fmt.Errorf("win32: lookup privilege %s: %w", name, err)
	}
	privileges := windows.Tokenprivileges{PrivilegeCount: 1}
	privileges.Privileges[0] = windows.LUIDAndAttributes{
		Luid:       luid,
		Attributes: windows.SE_PRIVILEGE_ENABLED,
	}
	if err := windows.AdjustTokenPrivileges(token, false, &privileges, uint32(unsafe.Sizeof(privileges)), nil, nil); err != nil {
		return fmt.Errorf("win32: enable privilege %s: %w", name, err)
	}
	if err := windows.GetLastError(); err != nil {
		return fmt.Errorf("win32: enable privilege %s: %w", name, err)
	}
	return nil
}

func sidArrayStrings(array uintptr, count uint32) ([]string, error) {
	if array == 0 || count == 0 {
		return nil, nil
	}
	raw := unsafe.Slice((*uintptr)(unsafe.Pointer(array)), int(count))
	out := make([]string, 0, len(raw))
	for _, sidPtr := range raw {
		if sidPtr == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(sidPtr))
		if !sid.IsValid() {
			return nil, fmt.Errorf("win32: derived capability SID is invalid")
		}
		out = append(out, sid.String())
	}
	return out, nil
}

func freeSIDArray(array uintptr, count uint32) {
	if array == 0 {
		return
	}
	if count > 0 {
		raw := unsafe.Slice((*uintptr)(unsafe.Pointer(array)), int(count))
		for _, sidPtr := range raw {
			if sidPtr != 0 {
				_, _ = windows.LocalFree(windows.Handle(sidPtr))
			}
		}
	}
	_, _ = windows.LocalFree(windows.Handle(array))
}

type pipeDirection int

const (
	parentReads pipeDirection = iota
	parentWrites
)

func createChildPipe(direction pipeDirection) (windows.Handle, windows.Handle, error) {
	sa := windows.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		InheritHandle: 1,
	}
	var readHandle windows.Handle
	var writeHandle windows.Handle
	if err := windows.CreatePipe(&readHandle, &writeHandle, &sa, 0); err != nil {
		return 0, 0, err
	}
	switch direction {
	case parentReads:
		if err := windows.SetHandleInformation(readHandle, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
			closeHandle(readHandle)
			closeHandle(writeHandle)
			return 0, 0, err
		}
	case parentWrites:
		if err := windows.SetHandleInformation(writeHandle, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
			closeHandle(readHandle)
			closeHandle(writeHandle)
			return 0, 0, err
		}
	}
	return readHandle, writeHandle, nil
}

func logonCreationFlags() uint32 {
	return uint32(windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW)
}

func logonFlagsForOptions(opt LogonProcessOptions) uintptr {
	if opt.LoadProfile {
		return uintptr(logonWithProfile)
	}
	return 0
}

func logonStartupInfo(stdinRead, stdoutWrite, stderrWrite windows.Handle) *windows.StartupInfo {
	return &windows.StartupInfo{
		Cb:        uint32(unsafe.Sizeof(windows.StartupInfo{})),
		Flags:     windows.STARTF_USESTDHANDLES,
		StdInput:  stdinRead,
		StdOutput: stdoutWrite,
		StdErr:    stderrWrite,
	}
}

func childStartupInfo(stdinRead, stdoutWrite, stderrWrite windows.Handle) (*windows.StartupInfoEx, func(), error) {
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, nil, fmt.Errorf("win32: create child handle allowlist: %w", err)
	}
	handles := []windows.Handle{stdinRead, stdoutWrite, stderrWrite}
	if err := attributes.Update(
		windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
		unsafe.Pointer(&handles[0]),
		uintptr(len(handles))*unsafe.Sizeof(handles[0]),
	); err != nil {
		attributes.Delete()
		return nil, nil, fmt.Errorf("win32: update child handle allowlist: %w", err)
	}
	startupInfo := &windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:        uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags:     windows.STARTF_USESTDHANDLES,
			StdInput:  stdinRead,
			StdOutput: stdoutWrite,
			StdErr:    stderrWrite,
		},
		ProcThreadAttributeList: attributes.List(),
	}
	cleanup := func() {
		runtime.KeepAlive(handles)
		attributes.Delete()
	}
	return startupInfo, cleanup, nil
}

func splitDomainUser(username string, explicitDomain string) (string, string) {
	username = strings.TrimSpace(username)
	domain := strings.TrimSpace(explicitDomain)
	if before, after, ok := strings.Cut(username, `\`); ok {
		if domain == "" {
			domain = before
		}
		username = after
	}
	if domain == "" && !strings.Contains(username, "@") {
		domain = "."
	}
	return username, domain
}

func ptr(value *uint16) uintptr {
	if value == nil {
		return 0
	}
	return uintptr(unsafe.Pointer(value))
}

func closeHandle(handle windows.Handle) {
	if handle != 0 {
		_ = windows.CloseHandle(handle)
	}
}

func localFree(handle windows.Handle) {
	if handle != 0 {
		_, _ = windows.LocalFree(handle)
	}
}
