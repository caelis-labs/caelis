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
)

const (
	defaultColumns = 80
	defaultRows    = 24
)

// ConPTYConfig describes one ConPTY process launch. Args excludes Application.
type ConPTYConfig struct {
	Token       uintptr
	Application string
	Args        []string
	Dir         string
	Env         []string
	Columns     int16
	Rows        int16
}

// ConPTYProcess owns one ConPTY process, its terminal pipes, and its Job Object.
type ConPTYProcess struct {
	process windows.Handle
	console windows.Handle
	job     *JobObject
	input   *os.File
	output  *os.File
	pid     uint32

	mu            sync.Mutex
	processClose  sync.Once
	terminalClose sync.Once
}

// StartConPTY creates the terminal and launches the process atomically inside
// a kill-on-close Job Object.
func StartConPTY(cfg ConPTYConfig) (_ *ConPTYProcess, retErr error) {
	resolved, err := resolveApplicationPath(cfg.Application, "conpty")
	if err != nil {
		return nil, err
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

	jobObject, err := NewJob()
	if err != nil {
		_ = windows.CloseHandle(inputWrite)
		_ = windows.CloseHandle(outputRead)
		return nil, fmt.Errorf("conpty: create job: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = jobObject.Close()
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
	jobList := []windows.Handle{windows.Handle(jobObject.Handle())}
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
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = windows.STARTF_USESTDHANDLES
	startup.StdInput = windows.InvalidHandle
	startup.StdOutput = windows.InvalidHandle
	startup.StdErr = windows.InvalidHandle
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

	return &ConPTYProcess{
		process: processInfo.Process,
		console: console,
		job:     jobObject,
		input:   os.NewFile(uintptr(inputWrite), "conpty-input"),
		output:  os.NewFile(uintptr(outputRead), "conpty-output"),
		pid:     processInfo.ProcessId,
	}, nil
}

// Input returns the host side of the terminal input pipe.
func (p *ConPTYProcess) Input() io.WriteCloser {
	if p == nil {
		return nil
	}
	return p.input
}

// Output returns the host side of the merged terminal output pipe.
func (p *ConPTYProcess) Output() io.ReadCloser {
	if p == nil {
		return nil
	}
	return p.output
}

// PID returns the root process identifier.
func (p *ConPTYProcess) PID() uint32 {
	if p == nil {
		return 0
	}
	return p.pid
}

// Wait waits for the root process and returns its exit code.
func (p *ConPTYProcess) Wait() (int, error) {
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
func (p *ConPTYProcess) Terminate() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job != nil {
		return p.job.Terminate(1)
	}
	if p.process != 0 {
		return windows.TerminateProcess(p.process, 1)
	}
	return nil
}

// DrainJob terminates descendants that outlive the terminal root and waits
// until the complete Job tree is empty before its capability fence may be
// released.
func (p *ConPTYProcess) DrainJob(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	jobObject := p.job
	p.mu.Unlock()
	if jobObject == nil {
		return nil
	}
	return jobObject.TerminateAndWait(ctx, 1)
}

// CloseAfterExit releases the tree fence and terminal after Wait. Output is
// owned by the reader and is intentionally not closed here.
func (p *ConPTYProcess) CloseAfterExit() {
	if p == nil {
		return
	}
	p.terminalClose.Do(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.job != nil {
			_ = p.job.Close()
			p.job = nil
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

func (p *ConPTYProcess) closeProcess() {
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

func createPipe() (windows.Handle, windows.Handle, error) {
	var read, write windows.Handle
	if err := windows.CreatePipe(&read, &write, nil, 0); err != nil {
		return 0, 0, err
	}
	return read, write, nil
}
