//go:build windows

package conpty

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/win32"
	"golang.org/x/sys/windows"
)

type Config struct {
	Command string
	Args    []string
	Dir     string
	Env     []string
	Rows    int
	Cols    int
	Token   win32.Token
}

type Session struct {
	console windows.Handle
	input   *os.File
	output  *os.File

	process windows.Handle
	pid     int

	waitOnce sync.Once
	waitCode int
	waitErr  error
}

func Start(cfg Config) (*Session, error) {
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		return nil, fmt.Errorf("conpty: command is required")
	}
	if !filepath.IsAbs(command) {
		resolved, err := exec.LookPath(command)
		if err != nil {
			return nil, err
		}
		command = resolved
		cfg.Command = resolved
	}
	console, input, output, err := createConsole(cfg.Rows, cfg.Cols)
	if err != nil {
		return nil, err
	}
	session := &Session{
		console:  console,
		input:    input,
		output:   output,
		waitCode: -1,
	}
	if err := session.startProcess(cfg); err != nil {
		_ = session.Close()
		return nil, err
	}
	return session, nil
}

func (s *Session) PID() int {
	if s == nil {
		return 0
	}
	return s.pid
}

func (s *Session) Input() io.WriteCloser {
	if s == nil {
		return nil
	}
	return s.input
}

func (s *Session) Output() io.Reader {
	if s == nil {
		return nil
	}
	return s.output
}

func (s *Session) Resize(rows int, cols int) error {
	if s == nil || s.console == 0 {
		return nil
	}
	size := consoleSize(rows, cols)
	return windows.ResizePseudoConsole(s.console, size)
}

func (s *Session) Wait() (int, error) {
	if s == nil {
		return 0, nil
	}
	s.waitOnce.Do(func() {
		if s.process == 0 {
			s.waitCode = 0
			return
		}
		if _, err := windows.WaitForSingleObject(s.process, windows.INFINITE); err != nil {
			s.waitErr = err
			return
		}
		var exitCode uint32
		if err := windows.GetExitCodeProcess(s.process, &exitCode); err != nil {
			s.waitErr = err
			return
		}
		s.waitCode = int(exitCode)
		closeHandle(s.process)
		s.process = 0
		if exitCode != 0 {
			s.waitErr = win32.ExitError{ExitCode: int(exitCode)}
		}
	})
	return s.waitCode, s.waitErr
}

func (s *Session) Kill() error {
	if s == nil || s.process == 0 {
		return nil
	}
	return windows.TerminateProcess(s.process, 1)
}

func (s *Session) Close() error {
	firstErr := s.CloseConsole()
	if s.output != nil {
		if err := s.output.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.output = nil
	}
	if s.process != 0 {
		closeHandle(s.process)
		s.process = 0
	}
	return firstErr
}

func (s *Session) CloseConsole() error {
	var firstErr error
	if s.input != nil {
		if err := s.input.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.input = nil
	}
	if s.console != 0 {
		windows.ClosePseudoConsole(s.console)
		s.console = 0
	}
	return firstErr
}

func (s *Session) startProcess(cfg Config) error {
	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return err
	}
	defer attrList.Delete()
	if err := attrList.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, unsafe.Pointer(s.console), unsafe.Sizeof(s.console)); err != nil {
		return err
	}

	commandUTF16, err := windows.UTF16PtrFromString(cfg.Command)
	if err != nil {
		return err
	}
	commandLine, err := windows.UTF16FromString(windows.ComposeCommandLine(append([]string{cfg.Command}, cfg.Args...)))
	if err != nil {
		return err
	}
	var dirUTF16 *uint16
	if strings.TrimSpace(cfg.Dir) != "" {
		dirUTF16, err = windows.UTF16PtrFromString(cfg.Dir)
		if err != nil {
			return err
		}
	}
	env, err := environmentBlock(cfg.Env)
	if err != nil {
		return err
	}
	var envPtr *uint16
	if len(env) > 0 {
		envPtr = &env[0]
	}

	startupInfo := windows.StartupInfoEx{}
	startupInfo.StartupInfo.Cb = uint32(unsafe.Sizeof(startupInfo))
	startupInfo.StartupInfo.Flags = windows.STARTF_USESTDHANDLES
	startupInfo.StartupInfo.StdInput = windows.InvalidHandle
	startupInfo.StartupInfo.StdOutput = windows.InvalidHandle
	startupInfo.StartupInfo.StdErr = windows.InvalidHandle
	startupInfo.ProcThreadAttributeList = attrList.List()
	processInfo := windows.ProcessInformation{}
	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
	if cfg.Token != 0 {
		err = windows.CreateProcessAsUser(
			windows.Token(cfg.Token),
			commandUTF16,
			&commandLine[0],
			nil,
			nil,
			false,
			flags,
			envPtr,
			dirUTF16,
			&startupInfo.StartupInfo,
			&processInfo,
		)
	} else {
		err = windows.CreateProcess(
			commandUTF16,
			&commandLine[0],
			nil,
			nil,
			false,
			flags,
			envPtr,
			dirUTF16,
			&startupInfo.StartupInfo,
			&processInfo,
		)
	}
	if err != nil {
		return err
	}
	closeHandle(processInfo.Thread)
	s.process = processInfo.Process
	s.pid = int(processInfo.ProcessId)
	return nil
}

func createConsole(rows int, cols int) (windows.Handle, *os.File, *os.File, error) {
	var inputRead windows.Handle
	var inputWrite windows.Handle
	if err := windows.CreatePipe(&inputRead, &inputWrite, nil, 0); err != nil {
		return 0, nil, nil, err
	}
	var outputRead windows.Handle
	var outputWrite windows.Handle
	if err := windows.CreatePipe(&outputRead, &outputWrite, nil, 0); err != nil {
		closeHandle(inputRead)
		closeHandle(inputWrite)
		return 0, nil, nil, err
	}
	var console windows.Handle
	if err := windows.CreatePseudoConsole(consoleSize(rows, cols), inputRead, outputWrite, 0, &console); err != nil {
		closeHandle(inputRead)
		closeHandle(inputWrite)
		closeHandle(outputRead)
		closeHandle(outputWrite)
		return 0, nil, nil, err
	}
	closeHandle(inputRead)
	closeHandle(outputWrite)
	return console,
		os.NewFile(uintptr(inputWrite), "caelis-conpty-input"),
		os.NewFile(uintptr(outputRead), "caelis-conpty-output"),
		nil
}

func consoleSize(rows int, cols int) windows.Coord {
	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}
	if rows > 32767 {
		rows = 32767
	}
	if cols > 32767 {
		cols = 32767
	}
	return windows.Coord{X: int16(cols), Y: int16(rows)}
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

func closeHandle(handle windows.Handle) {
	if handle != 0 {
		_ = windows.CloseHandle(handle)
	}
}
