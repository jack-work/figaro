//go:build windows

package local

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

var (
	jobsMu sync.Mutex
	jobs   = map[int]windows.Handle{}
)

func afterStart(cmd *exec.Cmd) { assignJob(cmd) }

func afterWait(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	jobsMu.Lock()
	job, ok := jobs[pid]
	if ok {
		delete(jobs, pid)
	}
	jobsMu.Unlock()
	if ok {
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		_, _ = windows.SetInformationJobObject(
			job,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		)
		windows.CloseHandle(job)
	}
}

func assignJob(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	_, err2 := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err2 != nil {
		windows.CloseHandle(job)
		return
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return
	}
	err = windows.AssignProcessToJobObject(job, ph)
	windows.CloseHandle(ph)
	if err != nil {
		windows.CloseHandle(job)
		return
	}
	jobsMu.Lock()
	jobs[cmd.Process.Pid] = job
	jobsMu.Unlock()
}

func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	jobsMu.Lock()
	job, ok := jobs[pid]
	if ok {
		delete(jobs, pid)
	}
	jobsMu.Unlock()

	if ok {
		windows.TerminateJobObject(job, 1)
		windows.CloseHandle(job)
	}
	cmd.Process.Kill()
	exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprintf("%d", pid)).Run()
}

func signalTree(cmd *exec.Cmd, _ syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	killTree(cmd)
	return nil
}

func interruptible(f *os.File) *os.File { return f }
