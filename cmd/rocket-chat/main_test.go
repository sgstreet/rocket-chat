package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/backend/fake"
	"github.com/sgstreet/rocket-chat/internal/chat"
	"github.com/sgstreet/rocket-chat/internal/config"
	"github.com/sgstreet/rocket-chat/internal/store"
	"github.com/sgstreet/rocket-chat/internal/tui"
)

// recorder is the backend behind "test-record"; tests reset it before use.
var recorder *fake.Backend

func init() {
	backend.Register("test-record", func(func(any) error) (backend.Backend, error) {
		return recorder, nil
	})
	backend.Register("test-grounded", func(func(any) error) (backend.Backend, error) {
		return &fake.Backend{Script: []backend.Event{
			{Kind: backend.EventThinkingDelta, Text: "let me look"},
			{Kind: backend.EventSearchStarted, Query: "euro 2024 winner"},
			{Kind: backend.EventFetchStarted, URL: "https://uefa.example/final"},
			{Kind: backend.EventTextDelta, Text: "Spain won"},
			{Kind: backend.EventTextDelta, Text: " Euro 2024 [1]."},
			{Kind: backend.EventGrounding, Grounding: &chat.Grounding{
				Sources: []chat.Source{{Title: "Final report", URL: "https://uefa.example/final", Cited: true}},
				Queries: []string{"euro 2024 winner"},
			}},
			{Kind: backend.EventUsage, Usage: &backend.Usage{InputTokens: 7, OutputTokens: 5, SearchQueries: 1}},
			{Kind: backend.EventDone},
		}}, nil
	})
	backend.Register("test-cited", func(func(any) error) (backend.Backend, error) {
		return &fake.Backend{
			Caps: backend.Capabilities{InlineCitations: true},
			Script: []backend.Event{
				{Kind: backend.EventTextDelta, Text: "Spain won."},
				{Kind: backend.EventTextDelta, Text: " It was 2–1."},
				{Kind: backend.EventGrounding, Grounding: &chat.Grounding{
					Sources: []chat.Source{{Title: "uefa.com", URL: "https://r/1", Cited: true}, {Title: "wiki", URL: "https://r/2", Cited: true}},
					Spans:   []chat.Span{{Start: 0, End: 10, SourceIndexes: []int{0, 1}}, {Start: 11, End: 24, SourceIndexes: []int{1}}},
				}},
				{Kind: backend.EventDone},
			},
		}, nil
	})
	backend.Register("test-fail", func(func(any) error) (backend.Backend, error) {
		return &fake.Backend{
			Script: []backend.Event{{Kind: backend.EventTextDelta, Text: "partial"}},
			Err:    errors.New("model exploded"),
		}, nil
	})
}

type result struct {
	code        int
	out, errOut string
}

// cli runs the command with an isolated config. A nil stdin means stdin is
// not a pipe or file.
func cli(t *testing.T, ctx context.Context, stdin *string, args ...string) result {
	t.Helper()
	t.Setenv(config.EnvConfigPath, filepath.Join(t.TempDir(), "absent.json"))
	t.Setenv(config.EnvBackend, "")
	var out, errOut bytes.Buffer
	e := env{stdout: &out, stderr: &errOut}
	if stdin != nil {
		e.stdin = strings.NewReader(*stdin)
		e.stdinIsInput = true
	}
	code := run(ctx, args, e)
	return result{code: code, out: out.String(), errOut: errOut.String()}
}

func ptr[T any](v T) *T { return &v }

func TestVersion(t *testing.T) {
	r := cli(t, t.Context(), nil, "--version")
	if r.code != exitOK || !strings.HasPrefix(r.out, "rocket-chat ") {
		t.Errorf("got %+v", r)
	}
}

func TestListBackends(t *testing.T) {
	r := cli(t, t.Context(), nil, "--list-backends")
	if r.code != exitOK || !strings.Contains(r.out, "fake\n") {
		t.Errorf("got %+v", r)
	}
}

func TestListModels(t *testing.T) {
	recorder = &fake.Backend{ModelList: []backend.ModelInfo{
		{Name: "qwen3:4b", Description: "qwen3 4.0B Q4_K_M"},
		{Name: "llama3.2", Description: "llama 3.2B"},
	}}
	r := cli(t, t.Context(), nil, "-b", "test-record", "--list-models")
	want := "qwen3:4b  qwen3 4.0B Q4_K_M\nllama3.2  llama 3.2B\n"
	if r.code != exitOK || r.out != want {
		t.Errorf("got %+v, want stdout %q", r, want)
	}
}

func TestBadFlag(t *testing.T) {
	if r := cli(t, t.Context(), nil, "--nope"); r.code != exitUsage {
		t.Errorf("exit %d, want %d", r.code, exitUsage)
	}
}

func TestHelp(t *testing.T) {
	r := cli(t, t.Context(), nil, "-h")
	if r.code != exitOK || !strings.Contains(r.errOut, "Usage:") {
		t.Errorf("got %+v", r)
	}
}

func TestEchoFromArgs(t *testing.T) {
	r := cli(t, t.Context(), nil, "-b", "fake", "hello", "there")
	if r.code != exitOK || r.out != "hello there\n" {
		t.Errorf("got %+v", r)
	}
}

func TestPromptFlagWithStdin(t *testing.T) {
	recorder = &fake.Backend{}
	r := cli(t, t.Context(), ptr("line one\nline two\n"), "--backend", "test-record", "-p", "summarize", "-m", "m1", "-s", "be brief")
	if r.code != exitOK {
		t.Fatalf("got %+v", r)
	}
	reqs := recorder.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	req := reqs[0]
	if got, want := req.Messages[0].Text, "summarize\n\nline one\nline two"; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
	if req.Model != "m1" || req.System != "be brief" {
		t.Errorf("model %q system %q", req.Model, req.System)
	}
	if req.Search != nil {
		t.Errorf("Search = %v, want nil when --search not given", *req.Search)
	}
}

func TestStdinOnly(t *testing.T) {
	r := cli(t, t.Context(), ptr("piped question\n"), "-b", "fake")
	if r.code != exitOK || r.out != "piped question\n" {
		t.Errorf("got %+v", r)
	}
}

func TestSearchFlag(t *testing.T) {
	for _, tt := range []struct {
		arg  string
		want bool
	}{{"--search", true}, {"--search=false", false}} {
		recorder = &fake.Backend{}
		if r := cli(t, t.Context(), nil, "-b", "test-record", tt.arg, "q"); r.code != exitOK {
			t.Fatalf("%s: got %+v", tt.arg, r)
		}
		s := recorder.Requests()[0].Search
		if s == nil || *s != tt.want {
			t.Errorf("%s: Search = %v, want %v", tt.arg, s, tt.want)
		}
	}
}

func TestPromptErrors(t *testing.T) {
	for _, tt := range []struct {
		name  string
		stdin *string
		args  []string
	}{
		{"no prompt", nil, []string{"-b", "fake"}},
		{"empty stdin", ptr("  \n"), []string{"-b", "fake"}},
		{"both -p and args", nil, []string{"-b", "fake", "-p", "x", "y"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if r := cli(t, t.Context(), tt.stdin, tt.args...); r.code != exitUsage {
				t.Errorf("got %+v, want exit %d", r, exitUsage)
			}
		})
	}
}

func TestGroundedOutput(t *testing.T) {
	r := cli(t, t.Context(), nil, "-b", "test-grounded", "-v", "--thinking", "who won euro 2024")
	if r.code != exitOK {
		t.Fatalf("got %+v", r)
	}
	wantOut := `Spain won Euro 2024 [1].

Sources:
  [1] Final report
      https://uefa.example/final
Searched: "euro 2024 winner"
`
	if r.out != wantOut {
		t.Errorf("stdout =\n%s\nwant\n%s", r.out, wantOut)
	}
	for _, want := range []string{
		"let me look\nSearching: euro 2024 winner\n",
		"Reading: https://uefa.example/final\n",
		"[test-grounded · 7 in / 5 out tokens · 1 searches · ",
	} {
		if !strings.Contains(r.errOut, want) {
			t.Errorf("stderr %q missing %q", r.errOut, want)
		}
	}
}

func TestQuietByDefault(t *testing.T) {
	// stderr is not a terminal and -v is not given: no progress or reasoning.
	r := cli(t, t.Context(), nil, "-b", "test-grounded", "q")
	if r.code != exitOK || r.errOut != "" {
		t.Errorf("got %+v, want empty stderr", r)
	}
}

func TestInlineCitationsWhenPiped(t *testing.T) {
	// stdout is a buffer, not a terminal: the answer is held back and
	// printed with citation markers.
	r := cli(t, t.Context(), nil, "-b", "test-cited", "q")
	want := "Spain won.[1][2] It was 2–1.[2]\n\nSources:\n  [1] uefa.com\n      https://r/1\n  [2] wiki\n      https://r/2\n"
	if r.code != exitOK || r.out != want {
		t.Errorf("stdout = %q, want %q", r.out, want)
	}
}

func TestInlineCitationsOnTerminalStream(t *testing.T) {
	t.Setenv(config.EnvConfigPath, filepath.Join(t.TempDir(), "absent.json"))
	var out, errOut bytes.Buffer
	code := run(t.Context(), []string{"-b", "test-cited", "q"}, env{stdout: &out, stderr: &errOut, stdoutIsTerminal: true})
	if code != exitOK || !strings.HasPrefix(out.String(), "Spain won. It was 2–1.\n\nSources:") {
		t.Errorf("stdout = %q, want streamed text without markers", out.String())
	}
}

func TestBackendError(t *testing.T) {
	r := cli(t, t.Context(), nil, "-b", "test-fail", "q")
	if r.code != exitError || r.out != "partial\n" || !strings.Contains(r.errOut, "model exploded") {
		t.Errorf("got %+v", r)
	}
}

func TestUnknownBackend(t *testing.T) {
	r := cli(t, t.Context(), nil, "-b", "nope", "q")
	if r.code != exitError || !strings.Contains(r.errOut, `unknown backend "nope"`) {
		t.Errorf("got %+v", r)
	}
}

func TestInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := cli(t, ctx, nil, "-b", "fake", "q")
	if r.code != exitInterrupted || !strings.Contains(r.errOut, "interrupted") {
		t.Errorf("got %+v", r)
	}
}

func TestStdinThatNeverClosesIsInterruptible(t *testing.T) {
	t.Setenv(config.EnvConfigPath, filepath.Join(t.TempDir(), "absent.json"))
	pr, pw := io.Pipe()
	defer pw.Close()
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	var out, errOut bytes.Buffer
	code := run(ctx, []string{"-b", "fake", "question"}, env{stdin: pr, stdinIsInput: true, stdout: &out, stderr: &errOut})
	if code != exitInterrupted {
		t.Errorf("exit %d, stderr %q; want %d", code, errOut.String(), exitInterrupted)
	}
}

func TestIsInput(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	if isInput(devNull) {
		t.Error("/dev/null treated as input")
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()
	if !isInput(pr) {
		t.Error("pipe not treated as input")
	}
	f, err := os.CreateTemp(t.TempDir(), "in")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !isInput(f) {
		t.Error("regular file not treated as input")
	}
}

func TestInteractiveNeedsTerminal(t *testing.T) {
	r := cli(t, t.Context(), nil, "-b", "fake")
	if r.code != exitUsage || !strings.Contains(r.errOut, "interactive chat needs a terminal") {
		t.Errorf("got %+v", r)
	}
}

func TestResumeUsage(t *testing.T) {
	r := cli(t, t.Context(), nil, "-b", "fake", "--resume", "last", "a question")
	if r.code != exitUsage || !strings.Contains(r.errOut, "cannot be combined with a prompt") {
		t.Errorf("got %+v", r)
	}
}

func TestSessionOptions(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	saved := &store.Session{ID: "s1", Backend: "gemini", Model: "old-model", System: "old system",
		Messages: []chat.Message{{Role: chat.RoleUser, Text: "hi"}}}
	if err := st.Save(saved); err != nil {
		t.Fatal(err)
	}

	var opts tui.Options
	if err := sessionOptions(config.Sessions{Dir: dir}, options{resume: "last", model: "new-model"}, &opts); err != nil {
		t.Fatal(err)
	}
	if opts.Store == nil || opts.Resume == nil || opts.Backend != "gemini" {
		t.Fatalf("opts = %+v", opts)
	}
	if opts.Resume.Model != "new-model" || opts.Resume.System != "old system" {
		t.Errorf("flags should override only what they set: %+v", opts.Resume)
	}

	opts = tui.Options{Backend: "ollama"}
	if err := sessionOptions(config.Sessions{Dir: dir}, options{resume: "s1", backend: "ollama"}, &opts); err != nil {
		t.Fatal(err)
	}
	if opts.Backend != "ollama" {
		t.Errorf("-b should win over the saved backend, got %q", opts.Backend)
	}

	off := false
	opts = tui.Options{}
	if err := sessionOptions(config.Sessions{Save: &off}, options{}, &opts); err != nil || opts.Store != nil {
		t.Errorf("save off: store %v, err %v", opts.Store, err)
	}
	if err := sessionOptions(config.Sessions{Save: &off}, options{resume: "last"}, &opts); err == nil {
		t.Error("--resume with saving off should fail")
	}
	if err := sessionOptions(config.Sessions{Dir: dir}, options{resume: "missing"}, &opts); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing session: %v", err)
	}
}

func TestExpandHome(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	for in, want := range map[string]string{"~/chats": "/home/u/chats", "~": "/home/u", "/abs": "/abs", "": "", "a~/b": "a~/b"} {
		if got, err := expandHome(in); err != nil || got != want {
			t.Errorf("expandHome(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestMissingConfigFlagIsAnError(t *testing.T) {
	r := cli(t, t.Context(), nil, "--config", filepath.Join(t.TempDir(), "missing.json"), "-b", "fake", "q")
	if r.code != exitError || !strings.Contains(r.errOut, "missing.json") {
		t.Errorf("got %+v", r)
	}
}

func TestJSONConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"default_backend": "fake", "backends": {"fake": {"delay": "1ms"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := cli(t, t.Context(), nil, "--config", path, "from", "json")
	if r.code != exitOK || r.out != "from json\n" {
		t.Errorf("got %+v", r)
	}
}

func TestDefaultBackendFromConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"default_backend": "fake"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := cli(t, t.Context(), nil, "--config", path, "from", "config")
	if r.code != exitOK || r.out != "from config\n" {
		t.Errorf("got %+v", r)
	}
}
