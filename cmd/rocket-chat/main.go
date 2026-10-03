// Command rocket-chat is a terminal chat client with pluggable backends.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/chat"
	"github.com/sgstreet/rocket-chat/internal/config"
	"github.com/sgstreet/rocket-chat/internal/store"
	"github.com/sgstreet/rocket-chat/internal/tui"

	// Backends register themselves in init.
	_ "github.com/sgstreet/rocket-chat/internal/backend/fake"
	_ "github.com/sgstreet/rocket-chat/internal/backend/gemini"
	_ "github.com/sgstreet/rocket-chat/internal/backend/ollama"
)

// version is set at build time with -ldflags "-X main.version=...".
var version string

// Exit codes.
const (
	exitOK          = 0
	exitError       = 1
	exitUsage       = 2
	exitInterrupted = 130
)

// env is the process environment run works against, so tests can replace it.
type env struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	// stdinIsInput reports whether stdin is a pipe or file to read the
	// prompt from.
	stdinIsInput     bool
	stdoutIsTerminal bool
	stderrIsTerminal bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], env{
		stdin:            os.Stdin,
		stdout:           os.Stdout,
		stderr:           os.Stderr,
		stdinIsInput:     isInput(os.Stdin),
		stdoutIsTerminal: isTerminal(os.Stdout),
		stderrIsTerminal: isTerminal(os.Stderr),
	})
	stop()
	os.Exit(code)
}

type options struct {
	backend, model, prompt, system string
	configPath, resume             string
	search, thinking, verbose      bool
	showVersion, listBackends      bool
	listModels                     bool
}

func run(ctx context.Context, args []string, e env) int {
	var o options
	fs := flag.NewFlagSet("rocket-chat", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() {
		fmt.Fprint(e.stderr, `Usage:
  rocket-chat [flags]                interactive chat
  rocket-chat [flags] [question...]  one answer to stdout
  command | rocket-chat [flags] -p "instruction"

Flags:
`)
		fs.PrintDefaults()
	}
	for _, name := range []string{"b", "backend"} {
		fs.StringVar(&o.backend, name, "", "backend to use (default from config)")
	}
	for _, name := range []string{"m", "model"} {
		fs.StringVar(&o.model, name, "", "model to use (default chosen by the backend)")
	}
	for _, name := range []string{"p", "prompt"} {
		fs.StringVar(&o.prompt, name, "", "prompt; piped stdin is appended to it")
	}
	for _, name := range []string{"s", "system"} {
		fs.StringVar(&o.system, name, "", "system prompt")
	}
	for _, name := range []string{"v", "verbose"} {
		fs.BoolVar(&o.verbose, name, false, "show progress and usage on stderr")
	}
	fs.BoolVar(&o.search, "search", false, "enable or disable web search, e.g. --search=false (default from backend config)")
	fs.BoolVar(&o.thinking, "thinking", false, "show the model's reasoning on stderr")
	fs.StringVar(&o.resume, "resume", "", `continue a saved chat: "last" or a session ID`)
	fs.StringVar(&o.configPath, "config", "", "config file (default $"+config.EnvConfigPath+" or the user config directory)")
	fs.BoolVar(&o.showVersion, "version", false, "print the version and exit")
	fs.BoolVar(&o.listBackends, "list-backends", false, "print the available backends and exit")
	fs.BoolVar(&o.listModels, "list-models", false, "print the selected backend's models and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	searchSet := false
	fs.Visit(func(f *flag.Flag) { searchSet = searchSet || f.Name == "search" })

	if o.showVersion {
		fmt.Fprintln(e.stdout, "rocket-chat", buildVersion())
		return exitOK
	}
	if o.listBackends {
		for _, name := range backend.Names() {
			fmt.Fprintln(e.stdout, name)
		}
		return exitOK
	}

	cfg, err := loadConfig(o.configPath)
	if err != nil {
		return fail(e, err)
	}

	name := o.backend
	if name == "" {
		name = cfg.DefaultBackend
	}
	// Every backend opened is closed on the way out, which stops any local
	// server it started.
	var opened []backend.Backend
	defer func() {
		for _, b := range opened {
			if err := backend.Close(b); err != nil {
				fmt.Fprintln(e.stderr, "rocket-chat:", err)
			}
		}
	}()
	open := func(name string) (backend.Backend, error) {
		b, err := backend.Open(name, cfg.Decoder(name))
		if err == nil {
			opened = append(opened, b)
		}
		return b, err
	}
	var search *bool
	if searchSet {
		search = &o.search
	}

	interactive := o.prompt == "" && len(fs.Args()) == 0 && !e.stdinIsInput && !o.listModels
	if o.resume != "" && !interactive {
		fmt.Fprintln(e.stderr, "rocket-chat: --resume continues a chat interactively; it cannot be combined with a prompt")
		return exitUsage
	}
	if interactive {
		if !e.stdoutIsTerminal {
			fmt.Fprintln(e.stderr, "rocket-chat: no prompt given, and interactive chat needs a terminal (see -h)")
			return exitUsage
		}
		opts := tui.Options{
			Backend:  name,
			Model:    o.model,
			System:   o.system,
			Search:   search,
			Open:     open,
			Backends: backend.Names(),
		}
		if err := sessionOptions(cfg.Sessions, o, &opts); err != nil {
			return fail(e, err)
		}
		if opts.Theme, err = cfg.UI.ThemeName(); err != nil {
			return fail(e, err)
		}
		err := tui.Run(ctx, opts)
		switch {
		case ctx.Err() != nil:
			return exitInterrupted
		case err != nil:
			return fail(e, err)
		}
		return exitOK
	}

	b, err := open(name)
	if err != nil {
		return fail(e, err)
	}
	if o.listModels {
		return listModels(ctx, b, e)
	}

	text, err := buildPrompt(ctx, o.prompt, fs.Args(), e)
	switch {
	case ctx.Err() != nil:
		fmt.Fprintln(e.stderr, "rocket-chat: interrupted")
		return exitInterrupted
	case err != nil:
		fmt.Fprintln(e.stderr, "rocket-chat:", err)
		return exitUsage
	}

	req := backend.Request{
		Model:    o.model,
		System:   o.system,
		Search:   search,
		Messages: []chat.Message{{Role: chat.RoleUser, Text: text}},
	}

	shot := oneShot{
		out:         e.stdout,
		errOut:      e.stderr,
		backendName: name,
		progress:    o.verbose || e.stderrIsTerminal,
		thinking:    o.thinking,
		verbose:     o.verbose,
		buffer:      b.Capabilities().InlineCitations && !e.stdoutIsTerminal,
	}
	if err := shot.run(ctx, b, req); err != nil {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			fmt.Fprintln(e.stderr, "rocket-chat: interrupted")
			return exitInterrupted
		}
		return fail(e, err)
	}
	return exitOK
}

func listModels(ctx context.Context, b backend.Backend, e env) int {
	models, err := b.Models(ctx)
	if err != nil {
		return fail(e, err)
	}
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	for _, m := range models {
		fmt.Fprintf(w, "%s\t%s\n", m.Name, m.Description)
	}
	if err := w.Flush(); err != nil {
		return fail(e, err)
	}
	return exitOK
}

func fail(e env, err error) int {
	fmt.Fprintln(e.stderr, "rocket-chat:", err)
	return exitError
}

// sessionOptions opens the session store and loads the chat to resume. A
// resumed chat keeps its backend, model and system prompt unless flags
// override them.
func sessionOptions(cfg config.Sessions, o options, opts *tui.Options) error {
	if !cfg.SaveEnabled() {
		if o.resume != "" {
			return errors.New("--resume needs saved sessions, which are turned off (sessions.save)")
		}
		return nil
	}
	dir, err := expandHome(cfg.Dir)
	if err != nil {
		return err
	}
	if dir == "" {
		if dir, err = store.DefaultDir(); err != nil {
			return err
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		return fmt.Errorf("session store: %w", err)
	}
	opts.Store = st
	if o.resume == "" {
		return nil
	}
	sess, err := st.Load(o.resume)
	if err != nil {
		return err
	}
	if o.backend == "" && sess.Backend != "" {
		opts.Backend = sess.Backend
	}
	if o.model != "" {
		sess.Model = o.model
	}
	if o.system != "" {
		sess.System = o.system
	}
	opts.Resume = sess
	return nil
}

// expandHome replaces a leading "~/" with the home directory.
func expandHome(path string) (string, error) {
	rest, ok := strings.CutPrefix(path, "~/")
	if path == "~" {
		rest, ok = "", true
	}
	if !ok {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rest), nil
}

// loadConfig reads the config file. A file named with --config must exist;
// the default location may be absent.
func loadConfig(path string) (config.Config, error) {
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			return config.Config{}, err
		}
	} else {
		var err error
		if path, err = config.Path(); err != nil {
			return config.Config{}, err
		}
	}
	return config.Load(path)
}

// buildPrompt combines the -p prompt or the positional arguments with piped
// stdin. Reading stdin stops when ctx is cancelled, so a pipe that never
// closes cannot hang the command.
func buildPrompt(ctx context.Context, prompt string, args []string, e env) (string, error) {
	if prompt != "" && len(args) > 0 {
		return "", errors.New("give the prompt either with -p or as arguments, not both")
	}
	text := prompt
	if text == "" {
		text = strings.Join(args, " ")
	}
	if e.stdinIsInput && e.stdin != nil {
		data, err := readAll(ctx, e.stdin)
		if err != nil {
			return "", err
		}
		if input := strings.TrimRight(string(data), "\n"); strings.TrimSpace(input) != "" {
			if text == "" {
				text = input
			} else {
				text += "\n\n" + input
			}
		}
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("no prompt given (see -h)")
	}
	return text, nil
}

func readAll(ctx context.Context, r io.Reader) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(r)
		done <- result{data, err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			return nil, fmt.Errorf("reading stdin: %w", res.err)
		}
		return res.data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// isInput reports whether f is a pipe or regular file. Terminals, /dev/null
// and sockets are not read as prompt input.
func isInput(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeNamedPipe != 0 || fi.Mode().IsRegular()
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}
