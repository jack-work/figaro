//go:build windows

package tool

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procAttr returns SysProcAttr for child processes. On Windows we create
// a new process group so GenerateConsoleCtrlEvent (Ctrl-C / Ctrl-Break)
// doesn't propagate from the parent's group into the child.
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// jobsMu guards the cmd→job map. Entries are added after Start and
// removed (handle closed) by killTree / signalTree.
var (
	jobsMu sync.Mutex
	jobs   = map[int]windows.Handle{}
)

func afterStart(cmd *exec.Cmd) { assignJob(cmd) }

// assignJob creates a Job Object, sets KILL_ON_JOB_CLOSE, and assigns
// the process to it. If anything fails, the caller falls back to
// Process.Kill (direct child only). Call after cmd.Start().
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

// killTree terminates the entire process tree via the Job Object (if
// one was assigned), falling back to taskkill /T for the tree.
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
	// Always also kill via Process.Kill (direct child) and taskkill /T
	// (recursive tree kill) as belt-and-suspenders. Job assignment can
	// fail silently if the process is already in another job.
	cmd.Process.Kill()
	exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprintf("%d", pid)).Run()
}

// signalTree kills the process tree. On Windows the only reliable tree
// termination is via the Job Object; the sig parameter is ignored.
func signalTree(cmd *exec.Cmd, _ syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	killTree(cmd)
	return nil
}
