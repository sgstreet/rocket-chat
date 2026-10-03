package backend

import (
	"fmt"
	"slices"
	"sync"
)

// Factory creates a backend. decode fills a backend-specific settings struct
// from that backend's config section; it leaves v untouched when the section
// is absent.
type Factory func(decode func(v any) error) (Backend, error)

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register makes a backend available under name. It is meant to be called
// from a backend package's init function and panics on duplicate names.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if f == nil {
		panic("backend: Register factory is nil for " + name)
	}
	if _, dup := factories[name]; dup {
		panic("backend: Register called twice for " + name)
	}
	factories[name] = f
}

// Open creates the backend registered under name.
func Open(name string, decode func(v any) error) (Backend, error) {
	mu.RLock()
	f, ok := factories[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown backend %q (available: %v)", name, Names())
	}
	if decode == nil {
		decode = func(any) error { return nil }
	}
	b, err := f(decode)
	if err != nil {
		return nil, fmt.Errorf("backend %s: %w", name, err)
	}
	return b, nil
}

// Names returns the registered backend names in sorted order.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(factories))
	for name := range factories {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
