//go:build unix && !linux

package ollama

import "syscall"

func setParentDeathSignal(*syscall.SysProcAttr) {}
