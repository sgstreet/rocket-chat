package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "rocket-chat ") {
		t.Errorf("output = %q", out.String())
	}
}

func TestListBackends(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--list-backends"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "fake\n") {
		t.Errorf("output = %q, want fake listed", out.String())
	}
}

func TestBadFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--nope"}, &out, &errOut); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
}
