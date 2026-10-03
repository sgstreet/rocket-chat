package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/term"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/config"
)

// keyStore reads and saves backends' API keys in the config file. It
// updates cfg too, so backends opened afterwards see the new key.
type keyStore struct {
	cfg  *config.Config
	path string
}

// Names returns the backends that take an API key.
func (k *keyStore) Names() []string { return backend.KeyNames() }

// Status describes where the named backend's key comes from.
func (k *keyStore) Status(name string) string {
	info, ok := backend.Key(name)
	if !ok {
		return "takes no API key"
	}
	switch _, source := info.Resolve(k.cfg.APIKey(name)); source {
	case "":
		return "not set"
	case "config":
		return "saved in " + k.path
	default:
		return "from $" + source
	}
}

// Set saves the key, or removes it when key is empty.
func (k *keyStore) Set(name, key string) error {
	if _, ok := backend.Key(name); !ok {
		return fmt.Errorf("%s takes no API key (backends with keys: %s)", name, strings.Join(backend.KeyNames(), ", "))
	}
	if err := checkKey(key); key != "" && err != nil {
		return err
	}
	return k.cfg.SetAPIKey(k.path, name, key)
}

// Overridden returns the environment variable that takes precedence over
// the saved key, if one is set.
func (k *keyStore) Overridden(name string) string {
	info, _ := backend.Key(name)
	if _, source := info.Resolve(""); source != "" {
		return source
	}
	return ""
}

func checkKey(key string) error {
	if key == "" {
		return errors.New("the key is empty")
	}
	if strings.IndexFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return errors.New("the key contains spaces or control characters; paste only the key")
	}
	return nil
}

// setKey implements --set-key. arg is normally a backend name, and the key
// comes from piped stdin or a hidden terminal prompt. With -b naming the
// backend, arg may instead be the key itself. arg is never echoed unless it
// is a backend name, since it may be a key.
func setKey(ctx context.Context, e env, keys *keyStore, arg, backendFlag string) int {
	name, given := arg, ""
	info, ok := backend.Key(arg)
	if !ok {
		if backendFlag == "" {
			fmt.Fprintf(e.stderr, "rocket-chat: --set-key takes a backend name (%s), or the key itself together with -b <backend>\n",
				strings.Join(backend.KeyNames(), ", "))
			return exitUsage
		}
		name, given = backendFlag, arg
		if info, ok = backend.Key(name); !ok {
			fmt.Fprintf(e.stderr, "rocket-chat: %s takes no API key (backends with keys: %s)\n", name, strings.Join(backend.KeyNames(), ", "))
			return exitUsage
		}
	}
	key := given
	switch {
	case given != "":
		// The key was the flag's value.
	case e.stdinIsInput && e.stdin != nil:
		data, err := readAll(ctx, e.stdin)
		if err != nil {
			return fail(e, err)
		}
		key = strings.TrimSpace(string(data))
	case e.readSecret != nil:
		var err error
		key, err = e.readSecret(ctx, fmt.Sprintf("%s (create one at %s; input is hidden): ", info.Description, info.URL))
		if ctx.Err() != nil {
			return exitInterrupted
		}
		if err != nil {
			return fail(e, err)
		}
		key = strings.TrimSpace(key)
	default:
		fmt.Fprintf(e.stderr, "rocket-chat: --set-key reads the key from stdin or a terminal; try: rocket-chat --set-key %s < keyfile\n", name)
		return exitUsage
	}
	if err := checkKey(key); err != nil {
		return fail(e, fmt.Errorf("not saved: %w", err))
	}
	if err := keys.Set(name, key); err != nil {
		return fail(e, err)
	}
	fmt.Fprintf(e.stdout, "Saved the %s API key in %s.\n", name, keys.path)
	if given != "" {
		fmt.Fprintf(e.stderr, "rocket-chat: note: a key typed on the command line stays in your shell history; "+
			"`rocket-chat --set-key %s` asks for it without showing it\n", name)
	}
	if env := keys.Overridden(name); env != "" {
		fmt.Fprintf(e.stderr, "rocket-chat: note: %s is set and takes precedence over the saved key\n", env)
	}
	return exitOK
}

// removeKey implements --remove-key.
func removeKey(e env, keys *keyStore, name string) int {
	if _, ok := backend.Key(name); !ok {
		fmt.Fprintf(e.stderr, "rocket-chat: --remove-key takes a backend name (%s)\n", strings.Join(backend.KeyNames(), ", "))
		return exitUsage
	}
	if keys.cfg.APIKey(name) == "" {
		fmt.Fprintf(e.stdout, "No %s API key is saved in %s.\n", name, keys.path)
		return exitOK
	}
	if err := keys.Set(name, ""); err != nil {
		return fail(e, err)
	}
	fmt.Fprintf(e.stdout, "Removed the %s API key from %s.\n", name, keys.path)
	if env := keys.Overridden(name); env != "" {
		fmt.Fprintf(e.stderr, "rocket-chat: note: %s is still set and will be used\n", env)
	}
	return exitOK
}

// promptForKey asks for a backend's required API key on the terminal when
// none is set, and saves it. Without a terminal, or when the user enters
// nothing, it does nothing and the backend reports the missing key.
func promptForKey(ctx context.Context, e env, keys *keyStore, name string) error {
	info, ok := backend.Key(name)
	if !ok || !info.Required || e.readSecret == nil {
		return nil
	}
	if key, _ := info.Resolve(keys.cfg.APIKey(name)); key != "" {
		return nil
	}
	fmt.Fprintf(e.stderr, "%s needs a %s. Create one at %s\n", name, info.Description, info.URL)
	key, err := e.readSecret(ctx, "Paste it here to save it in "+keys.path+" (input is hidden): ")
	if err != nil {
		return err
	}
	if key = strings.TrimSpace(key); key == "" {
		return nil
	}
	if err := checkKey(key); err != nil {
		return fmt.Errorf("not saved: %w", err)
	}
	if err := keys.Set(name, key); err != nil {
		return err
	}
	fmt.Fprintf(e.stderr, "Saved the %s API key.\n", name)
	return nil
}

// terminalSecretReader returns a function that prompts on out and reads a
// line from in without echoing it, or nil when in is not a terminal. If ctx
// ends first, the terminal is restored so the shell gets its echo back.
func terminalSecretReader(in, out *os.File) func(ctx context.Context, prompt string) (string, error) {
	if !term.IsTerminal(in.Fd()) {
		return nil
	}
	return func(ctx context.Context, prompt string) (string, error) {
		state, err := term.GetState(in.Fd())
		if err != nil {
			return "", err
		}
		fmt.Fprint(out, prompt)
		type result struct {
			line []byte
			err  error
		}
		done := make(chan result, 1)
		go func() {
			line, err := term.ReadPassword(in.Fd())
			done <- result{line, err}
		}()
		select {
		case r := <-done:
			fmt.Fprintln(out)
			return string(r.line), r.err
		case <-ctx.Done():
			_ = term.Restore(in.Fd(), state)
			fmt.Fprintln(out)
			return "", ctx.Err()
		}
	}
}
