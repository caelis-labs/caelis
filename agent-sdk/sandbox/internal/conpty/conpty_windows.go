//go:build windows

// Package conpty starts an interactive Windows process inside a ConPTY and a
// kill-on-close Job Object. A non-zero token launches the process through
// CreateProcessAsUser so sandbox backends can retain their restricted-token
// boundary while adding terminal semantics.
package conpty

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	defaultColumns             = 80
	defaultRows                = 24
	procThreadAttributeJobList = 0x0002000d
)

// Config describes one ConPTY process launch. Args excludes Application.
type Config struct {
	Token       uintptr
	Application string
	Args        []string
	Dir         string
	Env         []string
	Columns     int16
	Rows        int16
}

// Process owns one ConPTY process, its terminal pipes, and its Job Object.
type Process struct {
	process windows.Handle
	console windows.Handle
	job     windows.Handle
	input   *os.File
	output  *os.File
	pid     uint32

	mu            sync.Mutex
	processClose  sync.Once
	terminalClose sync.Once
}

// Start creates the terminal and launches the process atomically inside the
// Job Object. The process never runs outside the tree-lifetime fence.
func Start(cfg Config) (_ *Process, retErr error) {
	application := strings.TrimSpace(cfg.Application)
	if application == "" {
		return nil, errors.New("conpty: application is required")
	}
	resolved, err := exec.LookPath(application)
	if err != nil {
		return nil, fmt.Errorf("conpty: resolve application %q: %w", application, err)
	}
	columns, rows := cfg.Columns, cfg.Rows
	if columns <= 0 {
		columns = defaultColumns
	}
	if rows <= 0 {
		rows = defaultRows
	}

	inputRead, inputWrite, err := createPipe()
	if err != nil {
		return nil, fmt.Errorf("conpty: create input pipe: %w", err)
	}
	defer func() { closeHandleOnError(&retErr, inputRead) }()
	outputRead, outputWrite, err := createPipe()
	if err != nil {
		_ = windows.CloseHandle(inputWrite)
		return nil, fmt.Errorf("conpty: create output pipe: %w", err)
	}
	defer func() { closeHandleOnError(&retErr, outputWrite) }()

	var console windows.Handle
	if err := windows.CreatePseudoConsole(windows.Coord{X: columns, Y: rows}, inputRead, outputWrite, 0, &console); err != nil {
		_ = windows.CloseHandle(inputWrite)
		_ = windows.CloseHandle(outputRead)
		return nil, fmt.Errorf("conpty: create pseudo console: %w", err)
	}
	defer func() {
		if retErr != nil {
			windows.ClosePseudoConsole(console)
		}
	}()
	_ = windows.CloseHandle(inputRead)
	inputRead = 0
	_ = windows.CloseHandle(outputWrite)
	outputWrite = 0

	jobHandle, err := newKillOnCloseJob()
	if err != nil {
		_ = windows.CloseHandle(inputWrite)
		_ = windows.CloseHandle(outputRead)
		return nil, fmt.Errorf("conpty: create job: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = windows.CloseHandle(jobHandle)
		}
	}()

	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		_ = windows.CloseHandle(inputWrite)
		_ = windows.CloseHandle(outputRead)
		return nil, fmt.Errorf("conpty: allocate process attributes: %w", err)
	}
	defer attributes.Delete()
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, handleValuePointer(console), unsafe.Sizeof(console)); err != nil {
		_ = windows.CloseHandle(inputWrite)
		_ = windows.CloseHandle(outputRead)
		return nil, fmt.Errorf("conpty: attach pseudo console attribute: %w", err)
	}
	jobList := []windows.Handle{jobHandle}
	if err := attributes.Update(procThreadAttributeJobList, unsafe.Pointer(&jobList[0]), unsafe.Sizeof(jobList[0])); err != nil {
		_ = windows.CloseHandle(inputWrite)
		_ = windows.CloseHandle(outputRead)
		return nil, fmt.Errorf("conpty: attach job attribute: %w", err)
	}

	argv := append([]string{resolved}, cfg.Args...)
	commandLine, err := windows.UTF16FromString(windows.ComposeCommandLine(argv))
	if err != nil {
		_ = windows.CloseHandle(inputWrite)
		_ = windows.CloseHandle(outputRead)
		return nil, fmt.Errorf("conpty: encode command line: %w", err)
	}
	applicationPtr, err := windows.UTF16PtrFromString(resolved)
	if err != nil {
		_ = windows.CloseHandle(inputWrite)
		_ = windows.CloseHandle(outputRead)
		return nil, fmt.Errorf("conpty: encode application: %w", err)
	}
	var dirPtr *uint16
	if strings.TrimSpace(cfg.Dir) != "" {
		dirPtr, err = windows.UTF16PtrFromString(cfg.Dir)
		if err != nil {
			_ = windows.CloseHandle(inputWrite)
			_ = windows.CloseHandle(outputRead)
			return nil, fmt.Errorf("conpty: encode working directory: %w", err)
		}
	}
	environment := environmentBlock(cfg.Env)
	startup := windows.StartupInfoEx{}
	startup.StartupInfo.Cb = uint32(unsafe.Sizeof(startup))
	startup.StartupInfo.Flags = windows.STARTF_USESTDHANDLES
	startup.StartupInfo.StdInput = windows.InvalidHandle
	startup.StartupInfo.StdOutput = windows.InvalidHandle
	startup.StartupInfo.StdErr = windows.InvalidHandle
	startup.ProcThreadAttributeList = attributes.List()
	processInfo := windows.ProcessInformation{}
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	if cfg.Token != 0 {
		err = windows.CreateProcessAsUser(
			windows.Token(cfg.Token), applicationPtr, &commandLine[0], nil, nil, false,
			flags, &environment[0], dirPtr, &startup.StartupInfo, &processInfo,
		)
	} else {
		err = windows.CreateProcess(
			applicationPtr, &commandLine[0], nil, nil, false,
			flags, &environment[0], dirPtr, &startup.StartupInfo, &processInfo,
		)
	}
	runtime.KeepAlive(jobList)
	if err != nil {
		_ = windows.CloseHandle(inputWrite)
		_ = windows.CloseHandle(outputRead)
		return nil, fmt.Errorf("conpty: create process: %w", err)
	}
	_ = windows.CloseHandle(processInfo.Thread)

	return &Process{
		process: processInfo.Process,
		console: console,
		job:     jobHandle,
		input:   os.NewFile(uintptr(inputWrite), "conpty-input"),
		output:  os.NewFile(uintptr(outputRead), "conpty-output"),
		pid:     processInfo.ProcessId,
	}, nil
}

// Input returns the host side of the terminal input pipe.
func (p *Process) Input() io.WriteCloser {
	if p == nil {
		return nil
	}
	return p.input
}

// Output returns the host side of the merged terminal output pipe.
func (p *Process) Output() io.ReadCloser {
	if p == nil {
		return nil
	}
	return p.output
}

// PID returns the root process identifier.
func (p *Process) PID() uint32 {
	if p == nil {
		return 0
	}
	return p.pid
}

// Wait waits for the root process and returns its exit code.
func (p *Process) Wait() (int, error) {
	if p == nil || p.process == 0 {
		return -1, errors.New("conpty: process is unavailable")
	}
	event, err := windows.WaitForSingleObject(p.process, windows.INFINITE)
	if err != nil {
		return -1, fmt.Errorf("conpty: wait for process: %w", err)
	}
	if event != windows.WAIT_OBJECT_0 {
		return -1, fmt.Errorf("conpty: unexpected wait result %#x", event)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(p.process, &exitCode); err != nil {
		return -1, fmt.Errorf("conpty: read exit code: %w", err)
	}
	p.closeProcess()
	if exitCode != 0 {
		return int(exitCode), fmt.Errorf("process exited with code %d", exitCode)
	}
	return 0, nil
}

// Terminate kills the complete Job Object process tree.
func (p *Process) Terminate() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job != 0 {
		return windows.TerminateJobObject(p.job, 1)
	}
	if p.process != 0 {
		return windows.TerminateProcess(p.process, 1)
	}
	return nil
}

// CloseAfterExit releases the tree fence and terminal after Wait. Output is
// owned by the reader and is intentionally not closed here.
func (p *Process) CloseAfterExit() {
	if p == nil {
		return
	}
	p.terminalClose.Do(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.job != 0 {
			_ = windows.CloseHandle(p.job)
			p.job = 0
		}
		if p.input != nil {
			_ = p.input.Close()
		}
		if p.console != 0 {
			windows.ClosePseudoConsole(p.console)
			p.console = 0
		}
	})
}

func (p *Process) closeProcess() {
	if p == nil {
		return
	}
	p.processClose.Do(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.process != 0 {
			_ = windows.CloseHandle(p.process)
			p.process = 0
		}
	})
}

func handleValuePointer(handle windows.Handle) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&handle))
}

func createPipe() (windows.Handle, windows.Handle, error) {
	var read, write windows.Handle
	if err := windows.CreatePipe(&read, &write, nil, 0); err != nil {
		return 0, 0, err
	}
	return read, write, nil
}

func newKillOnCloseJob() (windows.Handle, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		handle, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func environmentBlock(values []string) []uint16 {
	latest := make(map[string]string, len(values))
	for _, value := range values {
		index := strings.IndexByte(value, '=')
		if index < 0 {
			continue
		}
		if index == 0 {
			index = strings.IndexByte(value[1:], '=') + 1
			if index <= 0 {
				continue
			}
		}
		latest[strings.ToUpper(value[:index])] = value
	}
	normalized := make([]string, 0, len(latest))
	for _, value := range latest {
		normalized = append(normalized, value)
	}
	sort.Slice(normalized, func(i, j int) bool {
		return strings.ToUpper(normalized[i]) < strings.ToUpper(normalized[j])
	})
	block := utf16.Encode([]rune(strings.Join(normalized, "\x00")))
	return append(block, 0, 0)
}

func closeHandleOnError(retErr *error, handle windows.Handle) {
	if retErr != nil && *retErr != nil && handle != 0 {
		_ = windows.CloseHandle(handle)
	}
}
