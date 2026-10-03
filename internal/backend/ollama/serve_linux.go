package ollama

import "syscall"

// setParentDeathSignal asks Linux to stop the server if rocket-chat dies
// without stopping it, for example when killed.
func setParentDeathSignal(attr *syscall.SysProcAttr) {
	attr.Pdeathsig = syscall.SIGTERM
}
