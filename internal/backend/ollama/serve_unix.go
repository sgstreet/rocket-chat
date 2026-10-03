//go:build unix

package ollama

import (
	"os/exec"
	"syscall"
)

// setProcAttr puts the server in its own process group, so a Ctrl+C in the
// terminal does not reach it and it can be stopped with its model runners.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	setParentDeathSignal(cmd.SysProcAttr)
}

func terminate(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

func kill(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
