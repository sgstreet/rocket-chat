//go:build !unix

package ollama

import (
	"context"
	"net/url"
	"os"
	"os/exec"
)

// share is this backend's use of a local server. Without Unix process
// control, a server rocket-chat starts runs as its child and stops when it
// exits, even if another rocket-chat is using it.
type share struct {
	host   *url.URL
	server *proc
}

func newShare(host *url.URL) *share { return &share{host: host} }

// RunSupervisorIfAsked does nothing: supervisors are Unix only.
func RunSupervisorIfAsked() {}

func (s *share) join() {}

func (s *share) use(string) {}

// start runs `ollama serve` and waits until it answers. It reports false
// when the server that answers is not the one it started.
func (s *share) start(ctx context.Context, settings ServeSettings, ping func(context.Context) error) (bool, error) {
	path, err := lookCommand(settings, s.host)
	if err != nil {
		return false, err
	}
	logPath, logFile := openServeLog()
	cmd := exec.Command(path, "serve")
	cmd.Env = append(os.Environ(), "OLLAMA_HOST="+s.host.String())
	cmd.Stdout, cmd.Stderr = logFile, logFile
	setProcAttr(cmd)
	p, err := startProc(cmd)
	_ = logFile.Close()
	if err != nil {
		return false, err
	}
	err = waitReady(ctx, ping, settings.startTimeout(), p.exited)
	switch {
	case err == nil && p.exited():
		return false, nil // another server took the port
	case err == nil:
		s.server = p
		return true, nil
	}
	p.stop(stopTimeout)
	return false, startError(err, p.err, s.host, settings, logPath)
}

// leave stops the server this backend started. It reports whether it did,
// and the models other users still need (none are known here).
func (s *share) leave() (bool, []string) {
	if s.server == nil {
		return false, nil
	}
	s.server.stop(stopTimeout)
	s.server = nil
	return true, nil
}
