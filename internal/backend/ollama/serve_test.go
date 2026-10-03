//go:build unix

package ollama

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ollama/ollama/api"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/config"
)

// The test binary doubles as a fake `ollama serve` when this variable is
// set: "serve" answers like Ollama, "fail" exits at once, "hang" never
// listens.
const fakeModeEnv = "ROCKET_CHAT_FAKE_OLLAMA"

// fakeStateEnv names a directory where the fake records its pid and, when
// stopped with SIGTERM, a "stopped" file.
const fakeStateEnv = "ROCKET_CHAT_FAKE_OLLAMA_STATE"

func TestMain(m *testing.M) {
	// The test binary is also the supervisor rocket-chat starts.
	RunSupervisorIfAsked()
	if mode := os.Getenv(fakeModeEnv); mode != "" {
		runFakeOllama(mode)
		return
	}
	os.Exit(m.Run())
}

func runFakeOllama(mode string) {
	state := os.Getenv(fakeStateEnv)
	_ = os.WriteFile(filepath.Join(state, "pid"), []byte(fmt.Sprint(os.Getpid())), 0o600)
	switch mode {
	case "fail":
		fmt.Fprintln(os.Stderr, "Error: listen tcp 127.0.0.1:11434: bind: address already in use")
		os.Exit(1)
	case "hang":
		select {}
	}

	u, err := url.Parse(os.Getenv("OLLAMA_HOST"))
	if err != nil {
		os.Exit(2)
	}
	ln, err := net.Listen("tcp", u.Host)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("Ollama is running")) })
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(api.ListResponse{Models: []api.ListModelResponse{{Name: "fake:1b"}}})
	})
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, _ *http.Request) {
		enc := json.NewEncoder(w)
		_ = enc.Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "hello from fake"}})
		_ = enc.Encode(api.ChatResponse{Done: true})
	})
	go func() { _ = http.Serve(ln, mux) }()
	fmt.Println("fake ollama listening on", u.Host)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	<-sig
	_ = os.WriteFile(filepath.Join(state, "stopped"), nil, 0o600)
	os.Exit(0)
}

// freeHost returns a local URL with a port nothing listens on.
func freeHost(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return "http://" + addr
}

// fakeServe returns settings that start the fake in the given mode, and
// the fake's state directory.
func fakeServe(t *testing.T, mode string) (ServeSettings, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	t.Setenv(fakeModeEnv, mode)
	t.Setenv(fakeStateEnv, state)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())  // the server log goes here
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // and the registry here
	return ServeSettings{Command: exe, StartTimeout: config.Duration(10 * time.Second)}, state
}

func fakePid(t *testing.T, state string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(state, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	_, _ = fmt.Sscan(string(data), &pid)
	return pid
}

func processGone(pid int) bool {
	return syscall.Kill(pid, 0) == syscall.ESRCH
}

func collectChat(t *testing.T, b *Backend) (text string, notices []string, err error) {
	t.Helper()
	for ev, e := range b.Chat(t.Context(), backend.Request{Model: "fake:1b", Messages: helloReq.Messages}) {
		if e != nil {
			return text, notices, e
		}
		switch ev.Kind {
		case backend.EventTextDelta:
			text += ev.Text
		case backend.EventNotice:
			notices = append(notices, ev.Text)
		}
	}
	return text, notices, nil
}

func TestAutoStartAndStop(t *testing.T) {
	serve, state := fakeServe(t, "serve")
	host := freeHost(t)
	b := newBackend(t, Settings{Host: host, Serve: serve})

	// Listing models starts the server too.
	models, err := b.Models(t.Context())
	if err != nil || len(models) != 1 || models[0].Name != "fake:1b" {
		t.Fatalf("models = %+v, %v", models, err)
	}
	pid := fakePid(t, state)

	text, notices, err := collectChat(t, b)
	if err != nil || text != "hello from fake" {
		t.Fatalf("chat = %q, %v", text, err)
	}
	if len(notices) != 0 {
		t.Errorf("server started twice? notices %v", notices)
	}
	if got := fakePid(t, state); got != pid {
		t.Errorf("second request started another server (pid %d, then %d)", pid, got)
	}

	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "stopped")); err != nil {
		t.Error("server was not stopped with SIGTERM")
	}
	if !processGone(pid) {
		t.Errorf("server process %d still running", pid)
	}
	if _, err := net.DialTimeout("tcp", strings.TrimPrefix(host, "http://"), time.Second); err == nil {
		t.Error("port still answers after Close")
	}
}

func TestAutoStartNotice(t *testing.T) {
	serve, _ := fakeServe(t, "serve")
	host := freeHost(t)
	b := newBackend(t, Settings{Host: host, Serve: serve})
	defer b.Close()
	text, notices, err := collectChat(t, b)
	if err != nil || text != "hello from fake" {
		t.Fatalf("chat = %q, %v", text, err)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "Started a local Ollama server at "+host) {
		t.Errorf("notices = %v", notices)
	}
}

func TestRunningServerIsLeftAlone(t *testing.T) {
	fs := newFakeServer(t, standardChunks...)
	serve, state := fakeServe(t, "serve")
	b := newBackend(t, Settings{Host: fs.URL, Serve: serve})
	if _, notices, err := collectChat(t, b); err != nil || len(notices) != 0 {
		t.Fatalf("notices %v, err %v", notices, err)
	}
	if _, err := os.Stat(filepath.Join(state, "pid")); err == nil {
		t.Error("started a server although one was running")
	}
	_ = b.Close()
	if _, err := http.Get(fs.URL + "/api/tags"); err != nil {
		t.Errorf("running server stopped by Close: %v", err)
	}
}

func TestRestartAfterServerDies(t *testing.T) {
	serve, state := fakeServe(t, "serve")
	b := newBackend(t, Settings{Host: freeHost(t), Serve: serve})
	defer b.Close()
	if _, _, err := collectChat(t, b); err != nil {
		t.Fatal(err)
	}
	first := fakePid(t, state)
	_ = syscall.Kill(first, syscall.SIGKILL)
	for i := 0; i < 50 && !processGone(first); i++ {
		time.Sleep(20 * time.Millisecond)
	}

	// The request that finds it gone fails; the next one starts it again.
	if _, _, err := collectChat(t, b); err == nil || !strings.Contains(err.Error(), "cannot reach Ollama") {
		t.Fatalf("err = %v, want cannot reach", err)
	}
	text, notices, err := collectChat(t, b)
	if err != nil || text != "hello from fake" || len(notices) != 1 {
		t.Fatalf("after restart: %q %v %v", text, notices, err)
	}
	if second := fakePid(t, state); second == first {
		t.Error("server was not restarted")
	}
}

func TestServerExitsEarly(t *testing.T) {
	serve, _ := fakeServe(t, "fail")
	b := newBackend(t, Settings{Host: freeHost(t), Serve: serve})
	_, _, err := collectChat(t, b)
	if err == nil || !strings.Contains(err.Error(), "ollama serve exited") || !strings.Contains(err.Error(), "address already in use") {
		t.Errorf("err = %v, want exit and log tail", err)
	}
}

func TestStartTimeout(t *testing.T) {
	serve, state := fakeServe(t, "hang")
	serve.StartTimeout = config.Duration(500 * time.Millisecond)
	b := newBackend(t, Settings{Host: freeHost(t), Serve: serve})
	_, _, err := collectChat(t, b)
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("err = %v", err)
	}
	if pid := fakePid(t, state); !processGone(pid) {
		t.Errorf("hung server %d was not stopped", pid)
	}
}

func TestMissingCommand(t *testing.T) {
	b := newBackend(t, Settings{Host: freeHost(t), Serve: ServeSettings{Command: "no-such-ollama-command"}})
	_, _, err := collectChat(t, b)
	if err == nil || !strings.Contains(err.Error(), `"no-such-ollama-command" was not found`) {
		t.Errorf("err = %v", err)
	}
}

func TestIsLocal(t *testing.T) {
	for raw, want := range map[string]bool{
		"http://localhost:11434":     true,
		"http://127.0.0.1:11434":     true,
		"http://[::1]:11434":         true,
		"http://0.0.0.0:11434":       true,
		"http://192.168.1.10:11434":  false,
		"https://ollama.example.com": false,
	} {
		u, _ := url.Parse(raw)
		if got := isLocal(u); got != want {
			t.Errorf("isLocal(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestSharedServerStopsWithLastUser(t *testing.T) {
	serve, state := fakeServe(t, "serve")
	host := freeHost(t)
	first := newBackend(t, Settings{Host: host, Serve: serve})
	if _, notices, err := collectChat(t, first); err != nil || len(notices) != 1 {
		t.Fatalf("first: notices %v, err %v", notices, err)
	}
	pid := fakePid(t, state)

	// A second copy uses the running server rather than starting one.
	second := newBackend(t, Settings{Host: host, Serve: serve})
	if _, notices, err := collectChat(t, second); err != nil || len(notices) != 0 {
		t.Fatalf("second: notices %v, err %v", notices, err)
	}
	if got := fakePid(t, state); got != pid {
		t.Fatalf("second copy started another server (pid %d, then %d)", pid, got)
	}

	// The copy that started the server exits; the other still uses it.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * superviseInterval)
	if processGone(pid) {
		t.Fatal("server stopped while another copy uses it")
	}
	if text, _, err := collectChat(t, second); err != nil || text != "hello from fake" {
		t.Fatalf("second after first exits: %q, %v", text, err)
	}

	// The last copy out stops it.
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if !processGone(pid) {
		t.Errorf("server %d still running after the last copy exits", pid)
	}
	if _, err := os.Stat(filepath.Join(state, "stopped")); err != nil {
		t.Error("server was not stopped with SIGTERM")
	}
}

func TestServerStopsWhenItsUserDies(t *testing.T) {
	serve, state := fakeServe(t, "serve")
	host := freeHost(t)
	b := newBackend(t, Settings{Host: host, Serve: serve})
	if _, _, err := collectChat(t, b); err != nil {
		t.Fatal(err)
	}
	pid := fakePid(t, state)

	// Stand in for a copy that crashed: its pid no longer runs.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Skip("cannot run true:", err)
	}
	u, _ := url.Parse(host)
	path, err := registryPath(u)
	if err != nil {
		t.Fatal(err)
	}
	err = updateRegistry(path, func(r *registry) {
		r.Users = map[string][]string{fmt.Sprintf("%d-1", dead.Process.Pid): {"fake:1b"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100 && !processGone(pid); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if !processGone(pid) {
		t.Fatalf("server %d still running with no live users", pid)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("registry left behind: %v", err)
	}
}

func TestUnloadLeavesModelsOthersUse(t *testing.T) {
	fs := newFakeServer(t, standardChunks...)
	a := newBackend(t, Settings{Host: fs.URL})
	b := newBackend(t, Settings{Host: fs.URL})
	chat := func(be *Backend, model string) {
		for range be.Chat(t.Context(), backend.Request{Model: model, Messages: helloReq.Messages}) {
		}
	}
	chat(a, "qwen3")
	chat(b, "qwen3")
	chat(b, "llama3")

	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"llama3"}; !slices.Equal(fs.unloaded, want) {
		t.Errorf("unloaded %q while another copy uses qwen3, want %q", fs.unloaded, want)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"llama3", "qwen3"}; !slices.Equal(fs.unloaded, want) {
		t.Errorf("unloaded %q, want %q", fs.unloaded, want)
	}
}
