//go:build windows

package adapterhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processTree struct {
	job windows.Handle
}

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

func prepareProcess(command *exec.Cmd) (*processTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Starting suspended closes the otherwise unavoidable window in which the
	// root process could create descendants before joining the Host Job.
	command.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	return &processTree{job: job}, nil
}

func (p *processTree) Started(command *exec.Cmd) (returnErr error) {
	if p == nil || p.job == 0 || command == nil || command.Process == nil {
		return errors.New("adapterhost: incomplete Windows process tree")
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME,
		false,
		uint32(command.Process.Pid),
	)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, windows.CloseHandle(process))
	}()
	if err := windows.AssignProcessToJobObject(p.job, process); err != nil {
		return err
	}
	status, _, _ := ntResumeProcess.Call(uintptr(process))
	if status != 0 {
		return fmt.Errorf("NtResumeProcess returned NTSTATUS %#x", status)
	}
	return nil
}

func (p *processTree) Close() error {
	if p == nil || p.job == 0 {
		return nil
	}
	job := p.job
	p.job = 0
	return windows.CloseHandle(job)
}

func signalProcess(_ context.Context, command *exec.Cmd, process *processTree) error {
	if process != nil && process.job != 0 {
		return windows.TerminateJobObject(process.job, 1)
	}
	if command == nil || command.Process == nil {
		return nil
	}
	err := command.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func killProcess(command *exec.Cmd, process *processTree) error {
	return signalProcess(context.Background(), command, process)
}
