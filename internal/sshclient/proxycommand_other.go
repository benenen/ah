//go:build windows

package sshclient

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type proxyResources struct {
	job windows.Handle
}

func newProxyCommand(command string) *exec.Cmd {
	cmd := exec.Command("cmd.exe")
	// Pass the raw /C line via CmdLine so syscall.EscapeArg does not turn
	// quotes into \" which cmd.exe treats literally.
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: "cmd.exe /C " + command}
	return cmd
}

func isolateProxyCommand(*exec.Cmd) {}

func attachProxyProcess(cmd *exec.Cmd, res *proxyResources) error {
	if cmd.Process == nil {
		return nil
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	res.job = job
	return nil
}

func releaseProxyResources(res *proxyResources) {
	if res.job != 0 {
		_ = windows.CloseHandle(res.job)
		res.job = 0
	}
}

func killProxyCommand(cmd *exec.Cmd, res *proxyResources) {
	if res != nil && res.job != 0 {
		_ = windows.TerminateJobObject(res.job, 1)
		return
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
