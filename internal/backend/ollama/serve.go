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
	// running, and stops it when rocket-chat exits. Default true. A server
	// that was already running is never stopped.
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

// localServer is an `ollama serve` process started by rocket-chat.
type localServer struct {
	cmd     *exec.Cmd
	logPath string
	// done is closed when the process has exited; waitErr is then set.
	done    chan struct{}
	waitErr error
}

// startServer runs `ollama serve` for host and waits until ping succeeds. It
// returns nil and no error when the process exits but a server answers
// anyway: another program started one at the same time, and it is not ours
// to stop.
func startServer(ctx context.Context, s ServeSettings, host *url.URL, ping func(context.Context) error) (*localServer, error) {
	name := cmp.Or(s.Command, "ollama")
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("ollama is not running at %s and %q was not found to start it; "+
			"install Ollama from https://ollama.com/download or set backends.ollama.serve.command", host, name)
	}

	logPath, logFile := openServeLog()
	cmd := exec.Command(path, "serve")
	cmd.Env = append(os.Environ(), "OLLAMA_HOST="+host.String())
	cmd.Stdout, cmd.Stderr = logFile, logFile
	setProcAttr(cmd)
	err = cmd.Start()
	_ = logFile.Close() // the child has its own copy
	if err != nil {
		return nil, fmt.Errorf("starting %s serve: %w", path, err)
	}

	srv := &localServer{cmd: cmd, logPath: logPath, done: make(chan struct{})}
	go func() {
		srv.waitErr = cmd.Wait()
		close(srv.done)
	}()

	deadline := time.NewTimer(s.startTimeout())
	defer deadline.Stop()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		if ping(ctx) == nil {
			return srv, nil
		}
		select {
		case <-srv.done:
			if ping(ctx) == nil {
				return nil, nil
			}
			return nil, fmt.Errorf("ollama serve exited (%w) before answering at %s%s", srv.waitErr, host, srv.logTail())
		case <-ctx.Done():
			srv.stop()
			return nil, ctx.Err()
		case <-deadline.C:
			srv.stop()
			return nil, fmt.Errorf("ollama serve did not answer at %s within %v%s", host, s.startTimeout(), srv.logTail())
		case <-tick.C:
		}
	}
}

// stop ends the server: SIGTERM to its process group (so model runners it
// started go too), then a kill if it is still running after stopTimeout.
func (s *localServer) stop() {
	select {
	case <-s.done:
		return
	default:
	}
	terminate(s.cmd)
	select {
	case <-s.done:
	case <-time.After(stopTimeout):
		kill(s.cmd)
		<-s.done
	}
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
func (s *localServer) logTail() string {
	if s.logPath == "" {
		return ""
	}
	data, err := os.ReadFile(s.logPath)
	if err != nil {
		return "; log: " + s.logPath
	}
	data = bytes.TrimSpace(data)
	if len(data) > 600 {
		data = data[len(data)-600:]
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	if len(data) == 0 {
		return "; log: " + s.logPath
	}
	return fmt.Sprintf("; log %s ends with:\n%s", s.logPath, data)
}
