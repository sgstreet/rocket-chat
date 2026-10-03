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

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/chat"
	"github.com/sgstreet/rocket-chat/internal/config"

	// Backends register themselves in init.
	_ "github.com/sgstreet/rocket-chat/internal/backend/fake"
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
	stdin            io.Reader
	stdout, stderr   io.Writer
	stdinIsTerminal  bool
	stderrIsTerminal bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], env{
		stdin:            os.Stdin,
		stdout:           os.Stdout,
		stderr:           os.Stderr,
		stdinIsTerminal:  isTerminal(os.Stdin),
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

	text, err := buildPrompt(o.prompt, fs.Args(), e)
	if err != nil {
		fmt.Fprintln(e.stderr, "rocket-chat:", err)
		return exitUsage
	}

	name := o.backend
	if name == "" {
		name = cfg.DefaultBackend
	}
	b, err := backend.Open(name, cfg.Decoder(name))
	if err != nil {
		return fail(e, err)
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

func fail(e env, err error) int {
	fmt.Fprintln(e.stderr, "rocket-chat:", err)
	return exitError
}

func loadConfig(path string) (config.Config, error) {
	if path == "" {
		var err error
		if path, err = config.Path(); err != nil {
			return config.Config{}, err
		}
	}
	return config.Load(path)
}

// buildPrompt combines the -p prompt or the positional arguments with piped
// stdin.
func buildPrompt(prompt string, args []string, e env) (string, error) {
	if prompt != "" && len(args) > 0 {
		return "", errors.New("give the prompt either with -p or as arguments, not both")
	}
	text := prompt
	if text == "" {
		text = strings.Join(args, " ")
	}
	if !e.stdinIsTerminal && e.stdin != nil {
		data, err := io.ReadAll(e.stdin)
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
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
