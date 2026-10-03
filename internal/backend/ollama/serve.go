package ollama

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sgstreet/rocket-chat/internal/config"
)

// ServeSettings is the backends.ollama.serve config section.
type ServeSettings struct {
	// AutoStart runs `ollama serve` when the server at a local host is not
	// running, and stops it when the last rocket-chat using it exits.
	// Default true. A server rocket-chat did not start is never stopped.
	AutoStart *bool `json:"auto_start"`
	// Command is the ollama executable (default "ollama", found in PATH).
	Command string `json:"command"`
	// StartTimeout bounds the wait for a started server to answer
	// (default 30s).
	StartTimeout config.Duration `json:"start_timeout"`
}

func (s ServeSettings) autoStart() bool { return s.AutoStart == nil || *s.AutoStart }

func (s ServeSettings) startTimeout() time.Duration {
	if s.StartTimeout <= 0 {
		return 30 * time.Second
	}
	return time.Duration(s.StartTimeout)
}

// stopTimeout is how long a started server gets to exit after SIGTERM
// before it is killed.
const stopTimeout = 10 * time.Second

// supervisorStopTimeout is how long a supervisor gets to stop its server
// and exit.
const supervisorStopTimeout = stopTimeout + 5*time.Second

// pollInterval is how often a starting server is checked.
const pollInterval = 200 * time.Millisecond

// isLocal reports whether host names this machine, the only place a server
// can be started.
func isLocal(host *url.URL) bool {
	h := host.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

// isUnreachable reports whether err means nothing is listening at the host.
func isUnreachable(err error) bool {
	var opErr *net.OpError
	return errors.Is(err, syscall.ECONNREFUSED) || (errors.As(err, &opErr) && opErr.Op == "dial")
}

// errExited and errNoAnswer are how waitReady fails.
var (
	errExited   = errors.New("exited")
	errNoAnswer = errors.New("no answer")
)

// lookCommand finds the ollama executable.
func lookCommand(s ServeSettings, host *url.URL) (string, error) {
	name := cmp.Or(s.Command, "ollama")
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("ollama is not running at %s and %q was not found to start it; "+
			"install Ollama from https://ollama.com/download or set backends.ollama.serve.command", host, name)
	}
	return path, nil
}

// waitReady polls ping until the server answers. It fails with errExited
// once exited reports true, unless a server answers anyway, and with
// errNoAnswer after timeout.
func waitReady(ctx context.Context, ping func(context.Context) error, timeout time.Duration, exited func() bool) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		if ping(ctx) == nil {
			return nil
		}
		if exited() {
			if ping(ctx) == nil {
				return nil
			}
			return errExited
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errNoAnswer
		case <-tick.C:
		}
	}
}

// proc is a started process.
type proc struct {
	cmd *exec.Cmd
	// done is closed when the process has exited; err is then set.
	done chan struct{}
	err  error
}

func startProc(cmd *exec.Cmd) (*proc, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &proc{cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

func (p *proc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// stop ends the process and its process group: SIGTERM, then a kill if it
// is still running after timeout.
func (p *proc) stop(timeout time.Duration) {
	if p.exited() {
		return
	}
	terminate(p.cmd)
	select {
	case <-p.done:
	case <-time.After(timeout):
		kill(p.cmd)
		<-p.done
	}
}

// startError explains why a started server did not come up.
func startError(err error, exitErr error, host *url.URL, s ServeSettings, logPath string) error {
	switch {
	case errors.Is(err, errExited) && exitErr != nil:
		return fmt.Errorf("ollama serve exited (%w) before answering at %s%s", exitErr, host, logTail(logPath))
	case errors.Is(err, errExited):
		return fmt.Errorf("ollama serve exited before answering at %s%s", host, logTail(logPath))
	case errors.Is(err, errNoAnswer):
		return fmt.Errorf("ollama serve did not answer at %s within %v%s", host, s.startTimeout(), logTail(logPath))
	}
	return err
}

// openServeLog opens the server's log file, truncated, falling back to
// discarding the output when no log file can be created.
func openServeLog() (string, *os.File) {
	if dir, err := os.UserCacheDir(); err == nil {
		dir = filepath.Join(dir, "rocket-chat")
		if err := os.MkdirAll(dir, 0o700); err == nil {
			path := filepath.Join(dir, "ollama-serve.log")
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
				return path, f
			}
		}
	}
	f, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	return "", f
}

// logTail returns the end of the server log for error messages.
func logTail(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "; log: " + path
	}
	data = bytes.TrimSpace(data)
	if len(data) > 600 {
		data = data[len(data)-600:]
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	if len(data) == 0 {
		return "; log: " + path
	}
	return fmt.Sprintf("; log %s ends with:\n%s", path, data)
}
