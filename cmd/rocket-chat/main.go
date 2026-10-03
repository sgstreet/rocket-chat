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
	"runtime/debug"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/chat"
	"github.com/sgstreet/rocket-chat/internal/config"

	// Backends register themselves in init.
	_ "github.com/sgstreet/rocket-chat/internal/backend/fake"
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
	stderrIsTerminal bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], env{
		stdin:            os.Stdin,
		stdout:           os.Stdout,
		stderr:           os.Stderr,
		stdinIsInput:     isInput(os.Stdin),
		stderrIsTerminal: isTerminal(os.Stderr),
	})
	stop()
	os.Exit(code)
}

type options struct {
	backend, model, prompt, system string
	configPath                     string
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
  rocket-chat [flags] [question...]
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
	b, err := backend.Open(name, cfg.Decoder(name))
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
		Messages: []chat.Message{{Role: chat.RoleUser, Text: text}},
	}
	if searchSet {
		req.Search = &o.search
	}

	shot := oneShot{
		out:         e.stdout,
		errOut:      e.stderr,
		backendName: name,
		progress:    o.verbose || e.stderrIsTerminal,
		thinking:    o.thinking,
		verbose:     o.verbose,
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
		return "", errors.New("no prompt given (interactive mode is not implemented yet; see -h)")
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
