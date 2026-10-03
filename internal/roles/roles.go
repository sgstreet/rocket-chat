// Package roles is the library of named system prompts: built-in roles such
// as General Assistant, plus roles added or replaced in the config file.
package roles

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Role is a named system prompt.
type Role struct {
	// ID is how the role is chosen, e.g. "technical".
	ID string
	// Name is the display name, e.g. "Technical Adviser".
	Name        string
	Description string
	// Prompt is the system prompt text.
	Prompt string
	// Search turns web search on or off while the role is in use; nil
	// leaves the backend's default.
	Search *bool
	// Builtin reports whether the role ships with rocket-chat (and was not
	// replaced in the config).
	Builtin bool
}

// Config is one entry of the config file's "roles" section. Prompt and
// File are alternatives; File may be relative to the config file's
// directory and may start with ~/.
type Config struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	File        string `json:"file"`
	Search      *bool  `json:"search"`
}

var on = true

// builtin roles, in the order they are listed.
var builtin = []Role{
	{
		ID:          "general",
		Builtin:     true,
		Name:        "General Assistant",
		Description: "Helpful, clear answers on any topic",
		Prompt: "You are a helpful, knowledgeable assistant. Give clear, accurate answers and match their " +
			"length to the question: short for simple questions, more detail when it is needed. If a " +
			"request is ambiguous, say what you assumed or ask a brief clarifying question. Say so when " +
			"you are unsure rather than guessing.",
	},
	{
		ID:          "technical",
		Builtin:     true,
		Name:        "Technical Adviser",
		Description: "Engineering and software questions, with trade-offs and working code",
		Prompt: "You are a senior technical adviser with broad experience in software engineering, " +
			"systems, networking and infrastructure. Give precise, practical answers. When there are " +
			"several reasonable approaches, recommend one and explain the trade-offs briefly. Show " +
			"complete, working code or commands in fenced code blocks with the language named, and " +
			"point out pitfalls, security concerns and version-specific behaviour. Do not invent APIs, " +
			"flags or library functions; if you are not sure something exists, say so.",
	},
	{
		ID:          "research",
		Builtin:     true,
		Name:        "Research Assistant",
		Description: "Finds and weighs sources, cites them, separates fact from inference",
		Prompt: "You are a careful research assistant. Search for current, authoritative information " +
			"when the question depends on facts, figures, dates or recent events. Cite your sources " +
			"for each claim, compare them where they disagree, and say how reliable they are. Keep " +
			"established facts, reasonable inferences and open questions clearly apart. End with a " +
			"short summary of the answer and anything that remains uncertain.",
		Search: &on,
	},
}

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Library holds the available roles.
type Library struct {
	roles []Role
}

// Builtin returns a library with only the built-in roles.
func Builtin() *Library {
	return &Library{roles: slices.Clone(builtin)}
}

// Load returns the built-in roles merged with those from the config file.
// A configured role with a built-in's ID replaces it, keeping the
// built-in's name, description, prompt and search setting for anything it
// leaves out. configDir resolves relative File paths.
func Load(configured map[string]Config, configDir string) (*Library, error) {
	lib := Builtin()
	ids := make([]string, 0, len(configured))
	for id := range configured {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		i := slices.IndexFunc(lib.roles, func(b Role) bool { return b.ID == id })
		var base *Role
		if i >= 0 {
			base = &lib.roles[i]
		}
		r, err := fromConfig(id, configured[id], configDir, base)
		if err != nil {
			return nil, fmt.Errorf("roles.%s: %w", id, err)
		}
		if i >= 0 {
			lib.roles[i] = r
		} else {
			lib.roles = append(lib.roles, r)
		}
	}
	return lib, nil
}

// fromConfig builds a role from its config entry. base, when not nil, is
// the built-in being replaced, which fills in what the entry leaves out.
func fromConfig(id string, c Config, configDir string, base *Role) (Role, error) {
	if !validID.MatchString(id) {
		return Role{}, fmt.Errorf("role IDs use lowercase letters, digits and dashes, not %q", id)
	}
	prompt := strings.TrimSpace(c.Prompt)
	switch {
	case prompt != "" && c.File != "":
		return Role{}, fmt.Errorf("give either prompt or file, not both")
	case c.File != "":
		path, err := resolve(c.File, configDir)
		if err != nil {
			return Role{}, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return Role{}, err
		}
		prompt = strings.TrimSpace(string(data))
		if prompt == "" {
			return Role{}, fmt.Errorf("%s is empty", path)
		}
	case prompt == "" && base == nil:
		return Role{}, fmt.Errorf("a role needs a prompt or a file")
	}
	r := Role{
		ID:          id,
		Name:        strings.TrimSpace(c.Name),
		Description: strings.TrimSpace(c.Description),
		Prompt:      prompt,
		Search:      c.Search,
	}
	if base != nil {
		r.Name = cmp.Or(r.Name, base.Name)
		r.Description = cmp.Or(r.Description, base.Description)
		r.Prompt = cmp.Or(r.Prompt, base.Prompt)
		if r.Search == nil {
			r.Search = base.Search
		}
	}
	r.Name = cmp.Or(r.Name, id)
	return r, nil
}

func resolve(path, configDir string) (string, error) {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, rest), nil
	}
	if !filepath.IsAbs(path) && configDir != "" {
		return filepath.Join(configDir, path), nil
	}
	return path, nil
}

// List returns the roles: built-ins first in their usual order, then the
// added ones by ID.
func (l *Library) List() []Role {
	return slices.Clone(l.roles)
}

// Find looks a role up by ID or display name, ignoring case and treating
// spaces in names like dashes, so "technical", "Technical Adviser" and
// "technical-adviser" all work.
func (l *Library) Find(name string) (Role, bool) {
	key := normalize(name)
	for _, r := range l.roles {
		if r.ID == key || normalize(r.Name) == key {
			return r, true
		}
	}
	return Role{}, false
}

// Names returns the role IDs, for error messages.
func (l *Library) Names() []string {
	ids := make([]string, len(l.roles))
	for i, r := range l.roles {
		ids[i] = r.ID
	}
	return ids
}

func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), "-")
}
