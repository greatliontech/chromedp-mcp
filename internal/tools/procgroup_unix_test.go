//go:build unix

package tools

import (
	"os/exec"
	"syscall"
	"time"
)

func configureProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	// Kill the whole Chrome process group (browser + GPU/network/storage helpers).
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	_ = cmd.Wait()
	// Wait until no process remains in the process group so child helpers
	// cannot race t.TempDir()'s RemoveAll cleanup.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pgid, 0); err != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}
