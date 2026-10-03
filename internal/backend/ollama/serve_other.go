//go:build !unix

package ollama

import "os/exec"

func setProcAttr(*exec.Cmd) {}

func terminate(cmd *exec.Cmd) { _ = cmd.Process.Kill() }

func kill(cmd *exec.Cmd) { _ = cmd.Process.Kill() }
