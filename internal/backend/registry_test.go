package backend

import (
	"errors"
	"strings"
	"testing"
)

func TestOpenUnknown(t *testing.T) {
	_, err := Open("no-such-backend", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown backend") {
		t.Fatalf("err = %v, want unknown backend", err)
	}
}

func TestOpenFactoryError(t *testing.T) {
	boom := errors.New("boom")
	Register("test-failing", func(func(any) error) (Backend, error) { return nil, boom })
	if _, err := Open("test-failing", nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	f := func(func(any) error) (Backend, error) { return nil, nil }
	Register("test-dup", f)
	defer func() {
		if recover() == nil {
			t.Fatal("second Register did not panic")
		}
	}()
	Register("test-dup", f)
}
