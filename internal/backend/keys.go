package backend

import (
	"errors"
	"os"
	"slices"
)

// ErrNoAPIKey is wrapped by the error a backend returns when it needs an API
// key and none is set.
var ErrNoAPIKey = errors.New("no API key")

// KeyInfo describes the API key a backend reads from the api_key setting in
// its config section.
type KeyInfo struct {
	// Description says what the key is, e.g. "Gemini API key".
	Description string
	// URL is where to create a key.
	URL string
	// Env lists environment variables that override the saved key, in
	// order of preference.
	Env []string
	// Required reports whether the backend cannot work without the key.
	Required bool
}

// Resolve returns the key to use: the first environment variable in Env
// that is set, otherwise saved. source names where the key came from: the
// variable, "config", or "" when there is no key.
func (k KeyInfo) Resolve(saved string) (key, source string) {
	for _, env := range k.Env {
		if v := os.Getenv(env); v != "" {
			return v, env
		}
	}
	if saved != "" {
		return saved, "config"
	}
	return "", ""
}

var keys = map[string]KeyInfo{}

// RegisterKey records that the backend registered under name takes an API
// key. Like Register, it is meant to be called from init.
func RegisterKey(name string, k KeyInfo) {
	mu.Lock()
	defer mu.Unlock()
	keys[name] = k
}

// Key returns the API key description for the named backend; false means
// the backend takes no key.
func Key(name string) (KeyInfo, bool) {
	mu.RLock()
	defer mu.RUnlock()
	k, ok := keys[name]
	return k, ok
}

// KeyNames returns the names of the backends that take an API key, sorted.
func KeyNames() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(keys))
	for name := range keys {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
