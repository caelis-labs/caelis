//go:build windows

// Package jobprocess launches a non-terminal Windows process atomically inside
// a kill-on-close Job Object.
package jobprocess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/job"
	"golang.org/x/sys/windows"
)

const (
	procThreadAttributeHandleList = 0x00020002
	procThreadAttributeJobList    = 0x0002000d
)

type Config struct {
	Token       uintptr
	Application string
	Args        []string
	Dir         string
	Env         []string
	Stdin       bool
}

type Process struct {
	process windows.Handle
	job     *job.Object
	stdin   *os.File
	stdout  *os.File
	stderr  *os.File
	pid     uint32

	mu           sync.Mutex
	processClose sync.Once
	drainMu      sync.Mutex
	drainDone    bool
	drainErr     error
}

func Start(cfg Config) (_ *Process, retErr error) {
	application, err := resolveApplicationPath(cfg.Application)
	if err != nil {
		return nil, err
	}
	jobObject, err := job.New()
	if err != nil {
		return nil, err
	}
	defer func() {
		if retErr != nil {
			_ = jobObject.Close()
		}
	}()
	stdoutRead, stdoutWrite, err := inheritablePipe(false)
	if err != nil {
		return nil, err
	}
	defer closeHandleOnError(&retErr, stdoutRead)
	defer closeHandleOnError(&retErr, stdoutWrite)
	stderrRead, stderrWrite, err := inheritablePipe(false)
	if err != nil {
		return nil, err
	}
	defer closeHandleOnError(&retErr, stderrRead)
	defer closeHandleOnError(&retErr, stderrWrite)
	stdinRead, stdinWrite, err := inheritablePipe(true)
	if err != nil {
		return nil, err
	}
	defer closeHandleOnError(&retErr, stdinRead)
	defer closeHandleOnError(&retErr, stdinWrite)
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return nil, err
	}
	defer attributes.Delete()
	jobList := []windows.Handle{windows.Handle(jobObject.Handle())}
	if err := attributes.Update(procThreadAttributeJobList, unsafe.Pointer(&jobList[0]), unsafe.Sizeof(jobList[0])); err != nil {
		return nil, err
	}
	handles := []windows.Handle{stdinRead, stdoutWrite, stderrWrite}
	if err := attributes.Update(procThreadAttributeHandleList, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return nil, err
	}
	argv := append([]string{application}, cfg.Args...)
	commandLine, err := windows.UTF16FromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return nil, err
	}
	applicationPtr, err := windows.UTF16PtrFromString(application)
	if err != nil {
		return nil, err
	}
	var dirPtr *uint16
	if strings.TrimSpace(cfg.Dir) != "" {
		dirPtr, err = windows.UTF16PtrFromString(cfg.Dir)
		if err != nil {
			return nil, err
		}
	}
	environment := environmentBlock(cfg.Env)
	startup := windows.StartupInfoEx{}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = windows.STARTF_USESTDHANDLES
	startup.StdInput = stdinRead
	startup.StdOutput = stdoutWrite
	startup.StdErr = stderrWrite
	startup.ProcThreadAttributeList = attributes.List()
	processInfo := windows.ProcessInformation{}
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW)
	if cfg.Token != 0 {
		err = windows.CreateProcessAsUser(windows.Token(cfg.Token), applicationPtr, &commandLine[0], nil, nil, true, flags, &environment[0], dirPtr, &startup.StartupInfo, &processInfo)
	} else {
		err = windows.CreateProcess(applicationPtr, &commandLine[0], nil, nil, true, flags, &environment[0], dirPtr, &startup.StartupInfo, &processInfo)
	}
	runtime.KeepAlive(jobList)
	runtime.KeepAlive(handles)
	if err != nil {
		return nil, err
	}
	_ = windows.CloseHandle(processInfo.Thread)
	_ = windows.CloseHandle(stdoutWrite)
	_ = windows.CloseHandle(stderrWrite)
	if stdinRead != 0 {
		_ = windows.CloseHandle(stdinRead)
	}
	var input *os.File
	if cfg.Stdin {
		input = fileForHandle(stdinWrite, "jobprocess-stdin")
	} else {
		_ = windows.CloseHandle(stdinWrite)
	}
	return &Process{
		process: processInfo.Process,
		job:     jobObject,
		stdin:   input,
		stdout:  fileForHandle(stdoutRead, "jobprocess-stdout"),
		stderr:  fileForHandle(stderrRead, "jobprocess-stderr"),
		pid:     processInfo.ProcessId,
	}, nil
}

func (p *Process) Input() io.WriteCloser {
	if p == nil {
		return nil
	}
	return p.stdin
}
func (p *Process) Stdout() io.ReadCloser {
	if p == nil {
		return nil
	}
	return p.stdout
}
func (p *Process) Stderr() io.ReadCloser {
	if p == nil {
		return nil
	}
	return p.stderr
}
func (p *Process) PID() uint32 {
	if p == nil {
		return 0
	}
	return p.pid
}

func (p *Process) WaitRoot() (int, error) {
	if p == nil || p.process == 0 {
		return -1, errors.New("jobprocess: process is unavailable")
	}
	event, err := windows.WaitForSingleObject(p.process, windows.INFINITE)
	if err != nil || event != windows.WAIT_OBJECT_0 {
		return -1, errors.Join(err, fmt.Errorf("jobprocess: unexpected root wait result %#x", event))
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(p.process, &exitCode); err != nil {
		return -1, err
	}
	p.closeProcess()
	if exitCode != 0 {
		return int(exitCode), fmt.Errorf("process exited with code %d", exitCode)
	}
	return 0, nil
}

func (p *Process) Terminate() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job == nil {
		return nil
	}
	return p.job.Terminate(1)
}

func (p *Process) DrainAndClose(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.drainMu.Lock()
	defer p.drainMu.Unlock()
	if p.drainDone {
		return p.drainErr
	}
	p.mu.Lock()
	jobObject := p.job
	p.mu.Unlock()
	if jobObject == nil {
		p.drainDone = true
		p.drainErr = nil
		return nil
	}
	if err := jobObject.TerminateAndWait(ctx, 1); err != nil {
		// Retain the Job handle and its kill-on-close fence. A later retry may
		// prove the complete tree empty; callers must retain their capability
		// use until that proof succeeds.
		p.drainErr = err
		return err
	}
	p.mu.Lock()
	p.drainErr = jobObject.Close()
	if p.job == jobObject {
		p.job = nil
	}
	p.mu.Unlock()
	p.drainDone = true
	return p.drainErr
}

// NeedsDrainRetry reports whether the Job still fences a process tree whose
// complete exit has not yet been proven.
func (p *Process) NeedsDrainRetry() bool {
	if p == nil {
		return false
	}
	p.drainMu.Lock()
	defer p.drainMu.Unlock()
	return !p.drainDone
}

func resolveApplicationPath(application string) (string, error) {
	application = strings.TrimSpace(application)
	if application == "" {
		return "", errors.New("jobprocess: application is required")
	}
	resolved, err := exec.LookPath(application)
	if err != nil {
		return "", fmt.Errorf("jobprocess: resolve application %q: %w", application, err)
	}
	if !filepath.IsAbs(resolved) {
		resolved, err = filepath.Abs(resolved)
		if err != nil {
			return "", fmt.Errorf("jobprocess: make application path absolute: %w", err)
		}
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("jobprocess: inspect application %q: %w", resolved, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("jobprocess: application %q is a directory", resolved)
	}
	return resolved, nil
}

func (p *Process) ClosePipes() {
	if p == nil {
		return
	}
	for _, file := range []*os.File{p.stdin, p.stdout, p.stderr} {
		if file != nil {
			_ = file.Close()
		}
	}
}

func (p *Process) closeProcess() {
	p.processClose.Do(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.process != 0 {
			_ = windows.CloseHandle(p.process)
			p.process = 0
		}
	})
}

func inheritablePipe(childReads bool) (windows.Handle, windows.Handle, error) {
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	var read, write windows.Handle
	if err := windows.CreatePipe(&read, &write, &sa, 0); err != nil {
		return 0, 0, err
	}
	hostHandle := read
	if childReads {
		hostHandle = write
	}
	if err := windows.SetHandleInformation(hostHandle, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		_ = windows.CloseHandle(read)
		_ = windows.CloseHandle(write)
		return 0, 0, err
	}
	return read, write, nil
}

func fileForHandle(handle windows.Handle, name string) *os.File {
	if handle == 0 {
		return nil
	}
	return os.NewFile(uintptr(handle), name)
}

func environmentBlock(values []string) []uint16 {
	latest := make(map[string]string, len(values))
	for _, value := range values {
		index := strings.IndexByte(value, '=')
		if index <= 0 {
			continue
		}
		latest[strings.ToUpper(value[:index])] = value
	}
	normalized := make([]string, 0, len(latest))
	for _, value := range latest {
		normalized = append(normalized, value)
	}
	sort.Slice(normalized, func(i, j int) bool { return strings.ToUpper(normalized[i]) < strings.ToUpper(normalized[j]) })
	block := utf16.Encode([]rune(strings.Join(normalized, "\x00")))
	return append(block, 0, 0)
}

func closeHandleOnError(retErr *error, handle windows.Handle) {
	if retErr != nil && *retErr != nil && handle != 0 {
		_ = windows.CloseHandle(handle)
	}
}
