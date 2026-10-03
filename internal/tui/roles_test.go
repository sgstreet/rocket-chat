package tui

import (
	"strings"
	"testing"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/backend/fake"
	"github.com/sgstreet/rocket-chat/internal/roles"
	"github.com/sgstreet/rocket-chat/internal/store"
)

func searchBackend() *fake.Backend {
	return &fake.Backend{Caps: backend.Capabilities{WebSearch: true}}
}

func TestRoleCommand(t *testing.T) {
	b := searchBackend()
	h := newHarness(t, map[string]*fake.Backend{"fake": b}, "fake")

	h.typeAndSend("/role")
	list := h.last(entryNotice).text
	for _, want := range []string{"general", "General Assistant", "technical", "Technical Adviser", "research", "Research Assistant"} {
		if !strings.Contains(list, want) {
			t.Errorf("role list missing %q:\n%s", want, list)
		}
	}

	if !strings.HasPrefix(list, "No role: no system prompt.") || !strings.Contains(list, "/role custom <text>") {
		t.Errorf("/role without a role:\n%s", list)
	}

	h.typeAndSend("/role Technical Adviser")
	tech, _ := roles.Builtin().Find("technical")
	h.typeAndSend("/role")
	if list := h.last(entryNotice).text; !strings.HasPrefix(list, "Role: Technical Adviser (technical)\n\n"+tech.Prompt) || !strings.Contains(list, "* technical") {
		t.Errorf("/role with a role:\n%s", list)
	}
	if h.m.role != "technical" || h.m.system != tech.Prompt {
		t.Fatalf("role %q system %q", h.m.role, h.m.system)
	}
	if v := h.view(); !strings.Contains(v, "role: Technical Adviser") {
		t.Errorf("status missing role\n%s", v)
	}
	h.typeAndSend("q")
	if got := b.Requests()[0].System; got != tech.Prompt {
		t.Errorf("request system = %q", got)
	}

	// Research turns search on; going back to general hands it back to
	// the backend's default.
	h.typeAndSend("/role research")
	if !h.m.searchOn() || !strings.Contains(h.last(entryNotice).text, "Web search is on") {
		t.Errorf("research did not turn search on: %q", h.last(entryNotice).text)
	}
	h.typeAndSend("/role general")
	if h.m.search != nil || h.m.searchOn() {
		t.Error("general should leave search to the backend default")
	}

	h.typeAndSend("/role lawyer")
	if !strings.Contains(h.last(entryError).text, `No role "lawyer"`) {
		t.Error("unknown role accepted")
	}

	h.typeAndSend("/role research")
	h.typeAndSend("/role custom be terse")
	h.typeAndSend("/role")
	if list := h.last(entryNotice).text; !strings.HasPrefix(list, "Role: custom\n\nbe terse") || strings.Contains(list, "*") {
		t.Errorf("/role with a custom prompt:\n%s", list)
	}
	if h.m.role != "" || h.m.system != "be terse" || !strings.Contains(h.view(), "role: custom") {
		t.Errorf("custom prompt: role %q system %q", h.m.role, h.m.system)
	}
	if h.m.search != nil {
		t.Error("research's search setting outlived the role")
	}
	h.typeAndSend("/role show")
	if !strings.HasPrefix(h.last(entryNotice).text, "Role: custom") {
		t.Error("/role show with a custom prompt")
	}
	h.typeAndSend("/role custom")
	if !strings.Contains(h.last(entryError).text, "Usage: /role custom") {
		t.Error("/role custom without text")
	}
	h.typeAndSend("/system be terse")
	if !strings.Contains(h.last(entryError).text, "/system is now part of /role") {
		t.Error("/system should point to /role")
	}
	h.typeAndSend("/role off")
	if h.m.system != "" || h.m.role != "" {
		t.Error("/role off kept a prompt")
	}
}

func TestExplicitSearchWinsOverRole(t *testing.T) {
	h := newHarness(t, map[string]*fake.Backend{"fake": searchBackend()}, "fake")
	h.typeAndSend("/search off")
	h.typeAndSend("/role research")
	if h.m.searchOn() {
		t.Error("role overrode /search off")
	}
	h.typeAndSend("/search default")
	h.typeAndSend("/role research")
	if !h.m.searchOn() {
		t.Error("after /search default the role should set search again")
	}
}

func TestStartingRole(t *testing.T) {
	research, _ := roles.Builtin().Find("research")
	h := newHarnessWith(t, map[string]*fake.Backend{"fake": searchBackend()}, Options{Backend: "fake", Role: "research", System: research.Prompt})
	if !h.m.searchOn() || !strings.Contains(h.view(), "role: Research Assistant") {
		t.Errorf("starting role not applied: search %v\n%s", h.m.searchOn(), h.view())
	}

	off := false
	h = newHarnessWith(t, map[string]*fake.Backend{"fake": searchBackend()}, Options{Backend: "fake", Role: "research", System: research.Prompt, Search: &off})
	if h.m.searchOn() {
		t.Error("--search=false should win over the role")
	}
}

func TestCustomRoleLibrary(t *testing.T) {
	lib, err := roles.Load(map[string]roles.Config{"pirate": {Name: "Pirate", Prompt: "Talk like a pirate."}}, "")
	if err != nil {
		t.Fatal(err)
	}
	h := newHarnessWith(t, map[string]*fake.Backend{"fake": {}}, Options{Backend: "fake", Roles: lib})
	h.typeAndSend("/role pirate")
	if h.m.system != "Talk like a pirate." || !strings.Contains(h.view(), "role: Pirate") {
		t.Errorf("custom role not applied: %q", h.m.system)
	}
}

func TestRoleSavedWithSession(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := newHarnessWith(t, map[string]*fake.Backend{"fake": {}}, Options{Backend: "fake", Store: st})
	h.typeAndSend("/role technical")
	h.typeAndSend("hello")
	sess, err := st.Load("last")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Role != "technical" || sess.System == "" {
		t.Errorf("saved role %q system %q", sess.Role, sess.System)
	}

	h2 := newHarnessWith(t, map[string]*fake.Backend{"fake": {}}, Options{Backend: "fake", Store: st, Resume: sess})
	if h2.m.role != "technical" || !strings.Contains(h2.view(), "role: Technical Adviser") {
		t.Errorf("resumed role %q", h2.m.role)
	}
}

func TestRoleShow(t *testing.T) {
	h := newHarness(t, map[string]*fake.Backend{"fake": searchBackend()}, "fake")
	research, _ := roles.Builtin().Find("research")

	h.typeAndSend("/role show")
	if !strings.Contains(h.last(entryNotice).text, "No role: no system prompt.") {
		t.Error("/role show without a role")
	}
	h.typeAndSend("/role show Research Assistant")
	out := h.last(entryNotice).text
	if !strings.HasPrefix(out, "Research Assistant (research) — ") || !strings.Contains(out, "Turns web search on.") ||
		!strings.HasSuffix(out, "\n\n"+research.Prompt) {
		t.Errorf("show research:\n%s", out)
	}
	if h.m.role != "" || h.m.system != "" {
		t.Error("/role show switched the role")
	}

	h.typeAndSend("/role technical")
	h.typeAndSend("/role show")
	tech, _ := roles.Builtin().Find("technical")
	if out := h.last(entryNotice).text; !strings.HasPrefix(out, "Technical Adviser (technical)") || !strings.HasSuffix(out, tech.Prompt) || strings.Contains(out, "web search") {
		t.Errorf("show current:\n%s", out)
	}
	h.typeAndSend("/role show lawyer")
	if !strings.Contains(h.last(entryError).text, `No role "lawyer"`) {
		t.Error("unknown role")
	}
}

func TestRoleCompletion(t *testing.T) {
	h := newHarness(t, map[string]*fake.Backend{"fake": {}}, "fake")
	for start, want := range map[string]string{
		"/ro":           "/role ",
		"/role cu":      "/role custom ",
		"/role show te": "/role show technical ",
	} {
		h.m.input.SetValue(start)
		h.m.compl = nil
		h.send(press("tab"))
		if got := h.m.input.Value(); got != want {
			t.Errorf("%q: %q, want %q", start, got, want)
		}
	}
	h.m.input.SetValue("/sys")
	h.m.compl = nil
	h.send(press("tab"))
	if h.m.input.Value() != "/sys" {
		t.Error("/system is still offered")
	}
}
