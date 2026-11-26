//go:build windows

// Package winproc launches interactive (ConPTY) and non-terminal Windows
// processes atomically inside kill-on-close Job Objects.
package winproc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const procThreadAttributeJobList = 0x0002000d

// JobObject owns one kill-on-close Windows Job Object handle.
type JobObject struct {
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

// NewJob creates a Windows Job Object configured to kill all assigned processes
// when the last handle closes.
func NewJob() (*JobObject, error) {
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
	return &JobObject{handle: handle}, nil
}

func (j *JobObject) Terminate(exitCode uint32) error {
	if j == nil || j.handle == 0 {
		return nil
	}
	return windows.TerminateJobObject(j.handle, exitCode)
}

// WaitEmpty waits until every process in the Job has exited. Waiting only for
// the root process is insufficient because descendants may outlive it.
func (j *JobObject) WaitEmpty(ctx context.Context) error {
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
func (j *JobObject) TerminateAndWait(ctx context.Context, exitCode uint32) error {
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

func (j *JobObject) processHandles() ([]windows.Handle, error) {
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
					// Job accounting remains the authoritative drain fence.
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
func (j *JobObject) Handle() uintptr {
	if j == nil {
		return 0
	}
	return uintptr(j.handle)
}

func (j *JobObject) Close() error {
	if j == nil || j.handle == 0 {
		return nil
	}
	handle := j.handle
	j.handle = 0
	return windows.CloseHandle(handle)
}

func resolveApplicationPath(application string, owner string) (string, error) {
	application = strings.TrimSpace(application)
	if application == "" {
		return "", fmt.Errorf("%s: application is required", owner)
	}
	resolved, err := exec.LookPath(application)
	if err != nil {
		return "", fmt.Errorf("%s: resolve application %q: %w", owner, application, err)
	}
	if !filepath.IsAbs(resolved) {
		resolved, err = filepath.Abs(resolved)
		if err != nil {
			return "", fmt.Errorf("%s: make application path absolute: %w", owner, err)
		}
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("%s: inspect application %q: %w", owner, resolved, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s: application %q is a directory", owner, resolved)
	}
	return resolved, nil
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

func handleValuePointer(handle windows.Handle) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&handle))
}
