//go:build unix

package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Copies of rocket-chat share a local server. Each records itself, and the
// models it has used, in a registry file for the server's port. A server
// rocket-chat starts runs under a supervisor (rocket-chat itself, started
// again in its own session) which stops the server once no live copy of
// rocket-chat uses it. So the server goes when the last copy using it
// exits, even if that copy crashed or its terminal was closed, and a copy
// never stops a server another copy still uses.

const (
	// superviseEnv holds the registry path when rocket-chat runs as a
	// supervisor.
	superviseEnv = "ROCKET_CHAT_OLLAMA_SUPERVISE"
	// commandEnv holds the ollama executable a supervisor runs.
	commandEnv = "ROCKET_CHAT_OLLAMA_COMMAND"
)

// superviseInterval is how often a supervisor checks for users.
const superviseInterval = 500 * time.Millisecond

// registry is what the registry file holds.
type registry struct {
	// Supervisor is the pid of the supervisor of a server rocket-chat
	// started, or 0 when rocket-chat did not start the server.
	Supervisor int `json:"supervisor,omitempty"`
	// Users maps each user, "<pid>-<n>", to the models it has used.
	Users map[string][]string `json:"users"`
}

// shareSeq tells apart backends in one process.
var shareSeq atomic.Int64

// share is this backend's use of a local server.
type share struct {
	host *url.URL
	// path is the registry file, "" when it cannot be created.
	path   string
	id     string
	joined bool
	// sup is the supervisor this process started, if any.
	sup *proc
}

func newShare(host *url.URL) *share {
	s := &share{host: host, id: fmt.Sprintf("%d-%d", os.Getpid(), shareSeq.Add(1))}
	s.path, _ = registryPath(host)
	return s
}

// registryPath returns the registry file for host's port, in the runtime
// directory when there is one.
func registryPath(host *url.URL) (string, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		var err error
		if dir, err = os.UserCacheDir(); err != nil {
			dir = filepath.Join(os.TempDir(), "rocket-chat-"+strconv.Itoa(os.Getuid()))
		}
	}
	dir = filepath.Join(dir, "rocket-chat")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	port := host.Port()
	if port == "" {
		port = "80"
		if host.Scheme == "https" {
			port = "443"
		}
	}
	return filepath.Join(dir, "ollama-"+port+".json"), nil
}

// updateRegistry runs f on the registry at path with it locked, dropping
// users and a supervisor that are no longer running first, then saves it.
func updateRegistry(path string, f func(*registry)) error {
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }() // releases the lock
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}

	var r registry
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &r)
	}
	if r.Users == nil {
		r.Users = map[string][]string{}
	}
	for id := range r.Users {
		pid, _ := strconv.Atoi(strings.SplitN(id, "-", 2)[0])
		if !alive(pid) {
			delete(r.Users, id)
		}
	}
	if !alive(r.Supervisor) {
		r.Supervisor = 0
	}
	f(&r)

	if len(r.Users) == 0 && r.Supervisor == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// alive reports whether process pid is running.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (s *share) update(f func(*registry)) error {
	if s.path == "" {
		return errors.New("no registry for the local Ollama server")
	}
	err := updateRegistry(s.path, func(r *registry) {
		// A supervisor this process started has exited once Wait returns,
		// though until then its pid answers.
		if s.sup != nil && r.Supervisor == s.sup.cmd.Process.Pid && s.sup.exited() {
			r.Supervisor = 0
		}
		f(r)
	})
	if err == nil {
		s.joined = true
	}
	return err
}

// join records this backend as a user of the running server.
func (s *share) join() {
	_ = s.update(func(r *registry) {
		if _, ok := r.Users[s.id]; !ok {
			r.Users[s.id] = []string{}
		}
	})
}

// use records that this backend chatted with model.
func (s *share) use(model string) {
	_ = s.update(func(r *registry) {
		if !slices.Contains(r.Users[s.id], model) {
			r.Users[s.id] = append(r.Users[s.id], model)
		}
	})
}

// forget records that this backend no longer uses model, and returns the
// models the other users have used.
func (s *share) forget(model string) []string {
	var others []string
	_ = s.update(func(r *registry) {
		if models, ok := r.Users[s.id]; ok {
			r.Users[s.id] = slices.DeleteFunc(models, func(m string) bool { return m == model })
		}
		for id, models := range r.Users {
			if id != s.id {
				others = append(others, models...)
			}
		}
	})
	return others
}

// start starts a supervised server, or waits for one another copy of
// rocket-chat is starting, until it answers. It reports whether this
// backend started the server that answers.
func (s *share) start(ctx context.Context, settings ServeSettings, ping func(context.Context) error) (bool, error) {
	command, err := lookCommand(settings, s.host)
	if err != nil {
		return false, err
	}
	exe, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("starting ollama serve: %w", err)
	}
	for attempt := 0; ; attempt++ {
		var (
			sup      *proc
			other    int
			logPath  string
			startErr error
		)
		err := s.update(func(r *registry) {
			if _, ok := r.Users[s.id]; !ok {
				r.Users[s.id] = []string{}
			}
			if r.Supervisor != 0 {
				other = r.Supervisor
				return
			}
			var logFile *os.File
			logPath, logFile = openServeLog()
			cmd := exec.Command(exe)
			cmd.Env = append(os.Environ(),
				"OLLAMA_HOST="+s.host.String(), superviseEnv+"="+s.path, commandEnv+"="+command)
			cmd.Stdout, cmd.Stderr = logFile, logFile
			// Its own session: the terminal's signals do not reach it.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			sup, startErr = startProc(cmd)
			_ = logFile.Close()
			if startErr == nil {
				r.Supervisor = sup.cmd.Process.Pid
			}
		})
		switch {
		case err != nil:
			return false, fmt.Errorf("starting ollama serve: %w", err)
		case startErr != nil:
			return false, fmt.Errorf("starting ollama serve: %w", startErr)
		}

		if other != 0 {
			// Another copy is starting the server: wait for it, and start
			// one if it gives up.
			err := waitReady(ctx, ping, settings.startTimeout(), func() bool { return !alive(other) })
			if errors.Is(err, errExited) && attempt < 2 {
				continue
			}
			return false, startError(err, nil, s.host, settings, "")
		}

		s.sup = sup
		err = waitReady(ctx, ping, settings.startTimeout(), sup.exited)
		switch {
		case err == nil && sup.exited():
			return false, nil // another server took the port
		case err == nil:
			return true, nil
		}
		sup.stop(supervisorStopTimeout)
		return false, startError(err, sup.err, s.host, settings, logPath)
	}
}

// leave removes this backend from the users. When it was the last user of
// a server rocket-chat started, it stops the server and reports true.
// Otherwise it returns the models the other users have used.
func (s *share) leave() (bool, []string) {
	if !s.joined {
		return false, nil
	}
	var (
		others []string
		stop   int
	)
	err := s.update(func(r *registry) {
		delete(r.Users, s.id)
		for _, models := range r.Users {
			others = append(others, models...)
		}
		if len(r.Users) == 0 && r.Supervisor != 0 {
			stop, r.Supervisor = r.Supervisor, 0
		}
	})
	s.joined = false
	if err != nil {
		// Without the registry, other users are unknown; stop only a
		// server this process started.
		if s.sup == nil || s.sup.exited() {
			return false, nil
		}
		stop = s.sup.cmd.Process.Pid
	}
	if stop == 0 {
		return false, others
	}
	if s.sup != nil && s.sup.cmd.Process.Pid == stop {
		s.sup.stop(supervisorStopTimeout)
	} else {
		stopPid(stop, supervisorStopTimeout)
	}
	s.sup = nil
	return true, nil
}

// stopPid stops a process this one did not start: SIGTERM, then a kill if
// it is still running after timeout.
func stopPid(pid int, timeout time.Duration) {
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(timeout)
	for alive(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			return
		}
		time.Sleep(pollInterval / 4)
	}
}

// RunSupervisorIfAsked runs a supervisor, and exits, when rocket-chat was
// started as one. main calls it first.
func RunSupervisorIfAsked() {
	path := os.Getenv(superviseEnv)
	if path == "" {
		return
	}
	os.Exit(supervise(path, os.Getenv(commandEnv)))
}

// supervise runs `ollama serve` until no live copy of rocket-chat uses it,
// or until it is told to stop with SIGTERM.
func supervise(path, command string) int {
	signal.Ignore(syscall.SIGHUP, syscall.SIGINT)
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)

	cmd := exec.Command(command, "serve")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, superviseEnv+"=") && !strings.HasPrefix(kv, commandEnv+"=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	setProcAttr(cmd)
	server, err := startProc(cmd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rocket-chat: starting ollama serve:", err)
		return 1
	}

	me := os.Getpid()
	tick := time.NewTicker(superviseInterval)
	defer tick.Stop()
	for {
		select {
		case <-server.done:
			_ = updateRegistry(path, func(r *registry) {
				if r.Supervisor == me {
					r.Supervisor = 0
				}
			})
			if code := server.cmd.ProcessState.ExitCode(); code >= 0 {
				return code
			}
			return 1
		case <-term:
			server.stop(stopTimeout)
			return 0
		case <-tick.C:
			idle := true
			err := updateRegistry(path, func(r *registry) {
				if r.Supervisor == me && len(r.Users) > 0 {
					idle = false
					return
				}
				if r.Supervisor == me {
					r.Supervisor = 0
				}
			})
			if err != nil || idle {
				server.stop(stopTimeout)
				return 0
			}
		}
	}
}
