// Command rocket-chat is a terminal chat client with pluggable backends.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/chat"
	"github.com/sgstreet/rocket-chat/internal/config"
	"github.com/sgstreet/rocket-chat/internal/roles"
	"github.com/sgstreet/rocket-chat/internal/store"
	"github.com/sgstreet/rocket-chat/internal/tui"

	// Backends register themselves in init.
	_ "github.com/sgstreet/rocket-chat/internal/backend/fake"
	_ "github.com/sgstreet/rocket-chat/internal/backend/gemini"
	"github.com/sgstreet/rocket-chat/internal/backend/ollama"
	_ "github.com/sgstreet/rocket-chat/internal/backend/zai"
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
	// readSecret prompts on the terminal and reads a line without echoing
	// it; nil when stdin is not a terminal.
	readSecret func(ctx context.Context, prompt string) (string, error)
	// termWidth returns stdout's width in columns, or 0 when unknown.
	termWidth func() int
	// darkBackground reports whether the terminal background is dark; it
	// is only asked when --render needs to pick a style.
	darkBackground func() bool
	// statePath is where the last role chosen with /role is kept; "" turns
	// remembering it off.
	statePath string
}

func main() {
	// A copy of rocket-chat that keeps a shared Ollama server running.
	ollama.RunSupervisorIfAsked()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	statePath, _ := store.DefaultStatePath()
	code := run(ctx, os.Args[1:], env{
		statePath:        statePath,
		stdin:            os.Stdin,
		stdout:           os.Stdout,
		stderr:           os.Stderr,
		stdinIsInput:     isInput(os.Stdin),
		stdoutIsTerminal: isTerminal(os.Stdout),
		stderrIsTerminal: isTerminal(os.Stderr),
		readSecret:       terminalSecretReader(os.Stdin, os.Stderr),
		termWidth: func() int {
			if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil {
				return w
			}
			return 0
		},
		darkBackground: func() bool {
			if isTerminal(os.Stdin) && isTerminal(os.Stdout) {
				return lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
			}
			return true
		},
	})
	stop()
	os.Exit(code)
}

type options struct {
	backend, model, prompt, system string
	configPath, resume, role       string
	setKey, removeKey              string
	search, thinking, verbose      bool
	render                         bool
	showVersion, listBackends      bool
	listModels, listRoles          bool
	showRole                       string
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
		fs.StringVar(&o.system, name, "", "your own system prompt, instead of a role (like /role custom in the chat)")
	}
	for _, name := range []string{"r", "role"} {
		fs.StringVar(&o.role, name, "", "named system prompt, e.g. technical (see --list-roles)")
	}
	for _, name := range []string{"v", "verbose"} {
		fs.BoolVar(&o.verbose, name, false, "show progress and usage on stderr")
	}
	fs.BoolVar(&o.search, "search", false, "enable or disable web search, e.g. --search=false (default from backend config)")
	fs.BoolVar(&o.thinking, "thinking", false, "show the model's reasoning on stderr")
	fs.BoolVar(&o.render, "render", false, "print the answer as rendered Markdown (styles, wrapping, code highlighting)\nonce it is complete, instead of streaming the raw text")
	fs.StringVar(&o.resume, "resume", "", `continue a saved chat: "last" or a session ID`)
	fs.StringVar(&o.configPath, "config", "", "config file (default $"+config.EnvConfigPath+" or the user config directory)")
	fs.BoolVar(&o.showVersion, "version", false, "print the version and exit")
	fs.BoolVar(&o.listBackends, "list-backends", false, "print the available backends and exit")
	fs.BoolVar(&o.listModels, "list-models", false, "print the selected backend's models and exit")
	fs.BoolVar(&o.listRoles, "list-roles", false, "print the available roles and exit")
	fs.StringVar(&o.showRole, "show-role", "", "print a role's system prompt and exit, e.g. --show-role technical")
	fs.StringVar(&o.setKey, "set-key", "", "save a backend's API key in the config file and exit: --set-key gemini reads the key\nfrom stdin or a hidden prompt; -b gemini --set-key KEY takes it directly (backends: "+strings.Join(backend.KeyNames(), ", ")+")")
	fs.StringVar(&o.removeKey, "remove-key", "", "remove a backend's saved API key from the config file and exit")
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

	cfg, cfgPath, err := loadConfig(o.configPath, e.stderr)
	if err != nil {
		return fail(e, err)
	}
	keys := &keyStore{cfg: &cfg, path: cfgPath}
	switch {
	case o.setKey != "" && o.removeKey != "":
		fmt.Fprintln(e.stderr, "rocket-chat: give either --set-key or --remove-key, not both")
		return exitUsage
	case o.setKey != "":
		return setKey(ctx, e, keys, o.setKey, o.backend)
	case o.removeKey != "":
		return removeKey(e, keys, o.removeKey)
	}
	lib, err := roles.Load(cfg.Roles, filepath.Dir(cfgPath))
	if err != nil {
		return fail(e, fmt.Errorf("%s: %w", cfgPath, err))
	}
	last := loadState(e)
	if o.listRoles {
		start, _, err := selectRole(options{}, cfg, lib, last)
		if err != nil {
			return fail(e, err)
		}
		return listRoles(e, lib, start)
	}
	if o.showRole != "" {
		r, ok := lib.Find(o.showRole)
		if !ok {
			return fail(e, fmt.Errorf("unknown role %q (available: %s)", o.showRole, strings.Join(lib.Names(), ", ")))
		}
		fmt.Fprintln(e.stdout, r.Prompt)
		return exitOK
	}
	if o.role != "" && o.system != "" {
		fmt.Fprintln(e.stderr, "rocket-chat: give either --role or -s, not both")
		return exitUsage
	}
	// Only the interactive chat starts with the role last chosen with
	// /role; one-shot answers use --role, -s or default_role, so scripts
	// do not change with what was picked in a chat.
	interactive := o.prompt == "" && len(fs.Args()) == 0 && !e.stdinIsInput && !o.listModels
	if !interactive {
		last = store.State{}
	}
	role, system, err := selectRole(o, cfg, lib, last)
	if err != nil {
		return fail(e, err)
	}

	name, fallback := startBackend(o.backend, cfg.DefaultBackend, last.Backend, backend.Names())
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

	if o.resume != "" && !interactive {
		fmt.Fprintln(e.stderr, "rocket-chat: --resume continues a chat interactively; it cannot be combined with a prompt")
		return exitUsage
	}
	if interactive {
		if !e.stdoutIsTerminal {
			fmt.Fprintln(e.stderr, "rocket-chat: no prompt given, and interactive chat needs a terminal (see -h)")
			return exitUsage
		}
		// The chat cannot start without the backend, so ask for a missing
		// key now rather than failing.
		if err := promptForKey(ctx, e, keys, name); err != nil {
			if ctx.Err() != nil {
				return exitInterrupted
			}
			return fail(e, err)
		}
		opts := tui.Options{
			Keys:    keys,
			Backend: name,
			Model:   o.model,
			System:  system,
			Search:  search,
			Roles:   lib,
			Role:    roleID(role),
			RememberRole: func(id, prompt string) error {
				return rememberRole(e, id, prompt)
			},
			Models:          last.Models,
			RememberBackend: func(name string) error { return rememberBackend(e, name) },
			RememberModel:   func(b, model string) error { return rememberModel(e, b, model) },
			Fallback:        fallback,
			Open:            open,
			Backends:        backend.Names(),
		}
		if err := sessionOptions(cfg.Sessions, o, role, &opts); err != nil {
			return fail(e, err)
		}
		if opts.Theme, err = cfg.UI.ThemeName(); err != nil {
			return fail(e, err)
		}
		opts.Mouse = cfg.UI.MouseEnabled()
		opts.PlainReplies = !cfg.UI.MarkdownEnabled()
		if opts.InputLines, err = cfg.UI.InputLinesValue(); err != nil {
			return fail(e, err)
		}
		if cfg.UI.HistoryEnabled() {
			// Without a history file the chat still has this run's inputs.
			if path, err := store.DefaultHistoryPath(); err == nil {
				if h, err := store.OpenHistory(path, 0); err != nil {
					fmt.Fprintln(e.stderr, "rocket-chat: input history:", err)
				} else {
					opts.History = h
				}
			}
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
		System:   system,
		Search:   cmp.Or(search, roleSearch(role)),
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
		render:      o.render,
	}
	if o.render {
		if shot.style, err = renderStyle(cfg.UI, e); err != nil {
			return fail(e, err)
		}
		shot.width = 80
		if e.termWidth != nil {
			if w := e.termWidth(); w > 0 {
				shot.width = w
			}
		}
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

// renderStyle picks the Markdown style for --render: ui.theme, or the
// terminal's background when the theme is auto.
func renderStyle(ui config.UI, e env) (string, error) {
	theme, err := ui.ThemeName()
	if err != nil || theme != "" {
		return theme, err
	}
	if e.darkBackground != nil && !e.darkBackground() {
		return "light", nil
	}
	return "dark", nil
}

func listModels(ctx context.Context, b backend.Backend, e env) int {
	models, err := b.Models(ctx)
	partial, isPartial := errors.AsType[*backend.PartialList](err)
	if err != nil && !isPartial {
		return fail(e, err)
	}
	if isPartial {
		// The models that could be listed are still worth showing.
		defer fmt.Fprintln(e.stderr, "rocket-chat:", partial)
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
func sessionOptions(cfg config.Sessions, o options, role *roles.Role, opts *tui.Options) error {
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
	switch {
	case o.system != "":
		sess.System, sess.Role = o.system, ""
	case o.role != "" && role != nil:
		sess.System, sess.Role = role.Prompt, role.ID
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

// loadConfig reads the config file, first creating it with the defaults
// if it does not exist. Failing to create it is only a warning.
func loadConfig(path string, stderr io.Writer) (config.Config, string, error) {
	if path == "" {
		var err error
		if path, err = config.Path(); err != nil {
			return config.Config{}, "", err
		}
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := config.Create(path); err != nil {
			fmt.Fprintf(stderr, "rocket-chat: could not create a default config file: %v\n", err)
		} else {
			fmt.Fprintf(stderr, "rocket-chat: created the config file %s\n", path)
		}
	}
	cfg, err := config.Load(path)
	return cfg, path, err
}

// selectRole picks the system prompt a run starts with: --role or -s;
// otherwise last, the role last chosen with /role (chats only pass it);
// otherwise default_role, which is the General Assistant when unset. It returns the role (nil for a
// custom prompt or none) and the prompt text.
func selectRole(o options, cfg config.Config, lib *roles.Library, last store.State) (*roles.Role, string, error) {
	find := func(name string) (*roles.Role, string, error) {
		r, ok := lib.Find(name)
		if !ok {
			return nil, "", fmt.Errorf("unknown role %q (available: %s, or off)", name, strings.Join(lib.Names(), ", "))
		}
		return &r, r.Prompt, nil
	}
	switch {
	case o.role != "" && roleOff(o.role):
		return nil, "", nil
	case o.role != "":
		return find(o.role)
	case o.system != "":
		return nil, o.system, nil
	}
	switch {
	case last.Role == store.RoleOff:
		return nil, "", nil
	case last.Role == store.RoleCustom && last.Prompt != "":
		return nil, last.Prompt, nil
	case last.Role != "":
		// A role removed from the config since is skipped.
		if r, ok := lib.Find(last.Role); ok {
			return &r, r.Prompt, nil
		}
	}
	def := cmp.Or(cfg.DefaultRole, "general")
	if roleOff(def) {
		return nil, "", nil
	}
	r, p, err := find(def)
	if err != nil {
		return nil, "", fmt.Errorf("default_role: %w", err)
	}
	return r, p, nil
}

func roleOff(name string) bool {
	return name == store.RoleOff || name == "none"
}

// loadState reads the remembered state; a broken file is reported and
// ignored.
func loadState(e env) store.State {
	if e.statePath == "" {
		return store.State{}
	}
	s, err := store.LoadState(e.statePath)
	if err != nil {
		fmt.Fprintf(e.stderr, "rocket-chat: ignoring %s: %v\n", e.statePath, err)
		return store.State{}
	}
	return s
}

// startBackend picks the backend to start with: flag (-b), else remembered
// (the one last chosen with /backend in a chat, when it still exists), else
// def. fallback is def when the remembered backend was picked, for use if
// it cannot be opened.
func startBackend(flag, def, remembered string, known []string) (name, fallback string) {
	switch {
	case flag != "":
		return flag, ""
	case remembered != "" && remembered != def && slices.Contains(known, remembered):
		return remembered, def
	}
	return def, ""
}

// updateState changes the remembered state with f and saves it.
func updateState(e env, f func(*store.State)) error {
	if e.statePath == "" {
		return nil
	}
	s, err := store.LoadState(e.statePath)
	if err != nil {
		s = store.State{} // a broken file was reported at the start
	}
	f(&s)
	return store.SaveState(e.statePath, s)
}

// rememberRole saves the role chosen with /role: id for a named role, or
// a custom prompt, or neither for no prompt.
func rememberRole(e env, id, prompt string) error {
	return updateState(e, func(s *store.State) {
		switch {
		case id != "":
			s.Role, s.Prompt = id, ""
		case prompt != "":
			s.Role, s.Prompt = store.RoleCustom, prompt
		default:
			s.Role, s.Prompt = store.RoleOff, ""
		}
	})
}

// rememberBackend saves the backend chosen with /backend.
func rememberBackend(e env, name string) error {
	return updateState(e, func(s *store.State) { s.Backend = name })
}

// rememberModel saves the model chosen with /model for backendName; "" forgets
// it, so the backend's default applies.
func rememberModel(e env, backendName, model string) error {
	return updateState(e, func(s *store.State) {
		if model == "" {
			delete(s.Models, backendName)
			return
		}
		if s.Models == nil {
			s.Models = map[string]string{}
		}
		s.Models[backendName] = model
	})
}

// listRoles prints the roles, marking the one a new chat starts with.
func listRoles(e env, lib *roles.Library, start *roles.Role) int {
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	for _, r := range lib.List() {
		marker := " "
		if start != nil && r.ID == start.ID {
			marker = "*"
		}
		fmt.Fprintf(w, "%s %s\t%s\t%s\n", marker, r.ID, r.Name, r.Description)
	}
	if err := w.Flush(); err != nil {
		return fail(e, err)
	}
	return exitOK
}

func roleID(r *roles.Role) string {
	if r == nil {
		return ""
	}
	return r.ID
}

func roleSearch(r *roles.Role) *bool {
	if r == nil {
		return nil
	}
	return r.Search
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
