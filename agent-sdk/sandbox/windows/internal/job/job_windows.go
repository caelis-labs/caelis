//go:build windows

package job

import (
	"context"
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type Object struct {
	handle windows.Handle
}

type basicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

func New() (*Object, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		handle,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return &Object{handle: handle}, nil
}

func (j *Object) Terminate(exitCode uint32) error {
	if j == nil || j.handle == 0 {
		return nil
	}
	return windows.TerminateJobObject(j.handle, exitCode)
}

// WaitEmpty waits until every process in the Job has exited. Waiting only for
// the root process is insufficient because descendants may outlive it.
func (j *Object) WaitEmpty(ctx context.Context) error {
	if j == nil || j.handle == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		var info basicAccountingInformation
		if err := windows.QueryInformationJobObject(j.handle, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
			return err
		}
		if info.ActiveProcesses == 0 {
			return nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// TerminateAndWait snapshots the Job members, terminates the tree, and waits
// for both Job accounting and every captured process handle to signal.
func (j *Object) TerminateAndWait(ctx context.Context, exitCode uint32) error {
	if j == nil || j.handle == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	handles, snapshotErr := j.processHandles()
	defer func() {
		for _, handle := range handles {
			_ = windows.CloseHandle(handle)
		}
	}()
	terminateErr := j.Terminate(exitCode)
	waitErr := j.WaitEmpty(ctx)
	var processErrs []error
	for _, handle := range handles {
		for {
			event, err := windows.WaitForSingleObject(handle, 10)
			if err != nil {
				processErrs = append(processErrs, err)
				break
			}
			if event == windows.WAIT_OBJECT_0 {
				break
			}
			if err := ctx.Err(); err != nil {
				processErrs = append(processErrs, err)
				break
			}
		}
	}
	return errors.Join(append([]error{snapshotErr, terminateErr, waitErr}, processErrs...)...)
}

func (j *Object) processHandles() ([]windows.Handle, error) {
	buffer := make([]byte, 4096)
	for {
		err := windows.QueryInformationJobObject(j.handle, windows.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(&buffer[0])), uint32(len(buffer)), nil)
		if errors.Is(err, windows.ERROR_MORE_DATA) {
			buffer = make([]byte, len(buffer)*2)
			continue
		}
		if err != nil {
			return nil, err
		}
		count := *(*uint32)(unsafe.Pointer(&buffer[4]))
		ids := unsafe.Slice((*uintptr)(unsafe.Pointer(&buffer[8])), int(count))
		handles := make([]windows.Handle, 0, len(ids))
		for _, id := range ids {
			handle, openErr := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(id))
			if openErr != nil {
				if errors.Is(openErr, windows.ERROR_INVALID_PARAMETER) {
					// The member exited between the Job snapshot and OpenProcess;
					// Job accounting below remains the authoritative drain fence.
					continue
				}
				for _, opened := range handles {
					_ = windows.CloseHandle(opened)
				}
				return nil, openErr
			}
			handles = append(handles, handle)
		}
		return handles, nil
	}
}

// Handle returns the native Job handle for PROC_THREAD_ATTRIBUTE_JOB_LIST.
func (j *Object) Handle() uintptr {
	if j == nil {
		return 0
	}
	return uintptr(j.handle)
}

func (j *Object) Close() error {
	if j == nil || j.handle == 0 {
		return nil
	}
	handle := j.handle
	j.handle = 0
	return windows.CloseHandle(handle)
}
