//go:build windows

package winproc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

const procThreadAttributeHandleList = 0x00020002

type JobProcessConfig struct {
	Token       uintptr
	Application string
	Args        []string
	Dir         string
	Env         []string
	Stdin       bool
}

type JobProcess struct {
	process windows.Handle
	job     *JobObject
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

// StartJobProcess launches a non-terminal Windows process atomically inside a
// kill-on-close Job Object.
func StartJobProcess(cfg JobProcessConfig) (_ *JobProcess, retErr error) {
	application, err := resolveApplicationPath(cfg.Application, "jobprocess")
	if err != nil {
		return nil, err
	}
	commandLine, err := jobCommandLine(application, cfg.Args)
	if err != nil {
		return nil, err
	}
	jobObject, err := NewJob()
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
	return &JobProcess{
		process: processInfo.Process,
		job:     jobObject,
		stdin:   input,
		stdout:  fileForHandle(stdoutRead, "jobprocess-stdout"),
		stderr:  fileForHandle(stderrRead, "jobprocess-stderr"),
		pid:     processInfo.ProcessId,
	}, nil
}

// The Win32 limit includes the complete quoted UTF-16 command line and NUL.
func jobCommandLine(application string, args []string) ([]uint16, error) {
	argv := append([]string{application}, args...)
	line, err := windows.UTF16FromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return nil, errorcode.Wrap(errorcode.InvalidArgument, "Windows command line contains invalid characters", err)
	}
	if len(line) > 32767 {
		return nil, errorcode.New(errorcode.InvalidArgument, "Windows encoded command line exceeds 32767 UTF-16 units")
	}
	return line, nil
}

func (p *JobProcess) Input() io.WriteCloser {
	if p == nil || p.stdin == nil {
		return nil
	}
	return p.stdin
}

func (p *JobProcess) Stdout() io.ReadCloser {
	if p == nil {
		return nil
	}
	return p.stdout
}

func (p *JobProcess) Stderr() io.ReadCloser {
	if p == nil {
		return nil
	}
	return p.stderr
}

func (p *JobProcess) PID() uint32 {
	if p == nil {
		return 0
	}
	return p.pid
}

func (p *JobProcess) WaitRoot() (int, error) {
	if p == nil || p.process == 0 {
		return -1, errors.New("jobprocess: process is unavailable")
	}
	defer p.closeProcess()
	event, err := windows.WaitForSingleObject(p.process, windows.INFINITE)
	if err != nil || event != windows.WAIT_OBJECT_0 {
		return -1, errors.Join(err, fmt.Errorf("jobprocess: unexpected root wait result %#x", event))
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(p.process, &exitCode); err != nil {
		return -1, err
	}
	if exitCode != 0 {
		return int(exitCode), fmt.Errorf("process exited with code %d", exitCode)
	}
	return 0, nil
}

func (p *JobProcess) Terminate() error {
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

func (p *JobProcess) DrainAndClose(ctx context.Context) error {
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
func (p *JobProcess) NeedsDrainRetry() bool {
	if p == nil {
		return false
	}
	p.drainMu.Lock()
	defer p.drainMu.Unlock()
	return !p.drainDone
}

func (p *JobProcess) ClosePipes() {
	if p == nil {
		return
	}
	for _, file := range []*os.File{p.stdin, p.stdout, p.stderr} {
		if file != nil {
			_ = file.Close()
		}
	}
}

func (p *JobProcess) closeProcess() {
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
