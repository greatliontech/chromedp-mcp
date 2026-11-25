//go:build !unix

package tools

import "os/exec"

func configureProcGroup(cmd *exec.Cmd) {}

func killProcGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
