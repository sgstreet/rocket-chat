package roles

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func ids(rs []Role) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

func TestBuiltin(t *testing.T) {
	lib := Builtin()
	if got := ids(lib.List()); !slices.Equal(got, []string{"general", "technical", "research"}) {
		t.Errorf("built-ins = %v", got)
	}
	for _, r := range lib.List() {
		if r.Name == "" || r.Description == "" || len(r.Prompt) < 100 || !r.Builtin {
			t.Errorf("incomplete role %+v", r)
		}
	}
	if r, _ := lib.Find("research"); r.Search == nil || !*r.Search {
		t.Error("research should turn search on")
	}
	if r, _ := lib.Find("general"); r.Search != nil || strings.Contains(strings.ToLower(r.Prompt), "markdown") {
		t.Error("general should leave search and formatting to the backend and the model")
	}
}

func TestFind(t *testing.T) {
	lib := Builtin()
	for _, name := range []string{"technical", "Technical", "Technical Adviser", "technical-adviser", "  technical   adviser "} {
		if r, ok := lib.Find(name); !ok || r.ID != "technical" {
			t.Errorf("Find(%q) = %v, %v", name, r.ID, ok)
		}
	}
	if _, ok := lib.Find("lawyer"); ok {
		t.Error("found a role that does not exist")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte("\nYou review code.\nBe strict.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	off := false
	lib, err := Load(map[string]Config{
		"reviewer":   {Name: "Code Reviewer", Description: "Strict reviews", File: "reviewer.md"},
		"general":    {Name: "My Assistant", Prompt: "  Be brief.  ", Search: &off},
		"a-limerick": {Prompt: "Answer in a limerick."},
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(lib.List()); !slices.Equal(got, []string{"general", "technical", "research", "a-limerick", "reviewer"}) {
		t.Errorf("order = %v", got)
	}
	g, _ := lib.Find("general")
	if g.Name != "My Assistant" || g.Prompt != "Be brief." || g.Builtin || g.Search == nil || *g.Search {
		t.Errorf("replaced general = %+v", g)
	}
	r, _ := lib.Find("Code Reviewer")
	if r.Prompt != "You review code.\nBe strict." {
		t.Errorf("file prompt = %q", r.Prompt)
	}
	if l, _ := lib.Find("a-limerick"); l.Name != "a-limerick" {
		t.Errorf("name defaults to ID, got %q", l.Name)
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "empty.md"), []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		id   string
		cfg  Config
		want string
	}{
		"no prompt":  {"x", Config{Name: "X"}, "needs a prompt or a file"},
		"both":       {"x", Config{Prompt: "p", File: "f"}, "either prompt or file"},
		"bad id":     {"My Role", Config{Prompt: "p"}, "lowercase letters"},
		"no file":    {"x", Config{File: "missing.md"}, "missing.md"},
		"empty file": {"x", Config{File: "empty.md"}, "is empty"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(map[string]Config{tt.id: tt.cfg}, dir)
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.HasPrefix(err.Error(), "roles."+tt.id) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestReplaceBuiltinPartly(t *testing.T) {
	on, off := true, false
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "general.md"), []byte("Be brief."), 0o600); err != nil {
		t.Fatal(err)
	}
	lib, err := Load(map[string]Config{
		"technical": {Search: &on},                       // only turn search on
		"research":  {Description: "My research helper"}, // only a new description
		"general":   {File: "general.md", Search: &off},  // a new prompt from a file
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	builtin := func(id string) Role { r, _ := Builtin().Find(id); return r }

	tech, _ := lib.Find("technical")
	if tech.Prompt != builtin("technical").Prompt || tech.Name != "Technical Adviser" ||
		tech.Description != builtin("technical").Description || tech.Search == nil || !*tech.Search || tech.Builtin {
		t.Errorf("technical = %+v", tech)
	}
	res, _ := lib.Find("research")
	if res.Description != "My research helper" || res.Prompt != builtin("research").Prompt || res.Search == nil || !*res.Search {
		t.Errorf("research = %+v", res)
	}
	gen, _ := lib.Find("general")
	if gen.Prompt != "Be brief." || gen.Name != "General Assistant" || gen.Search == nil || *gen.Search {
		t.Errorf("general = %+v", gen)
	}
	if ids := ids(lib.List()); !slices.Equal(ids, []string{"general", "technical", "research"}) {
		t.Errorf("order = %v", ids)
	}
}
