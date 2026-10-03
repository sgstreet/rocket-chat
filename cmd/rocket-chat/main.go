// Command rocket-chat is a terminal chat client with pluggable backends.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/config"

	// Backends register themselves in init.
	_ "github.com/sgstreet/rocket-chat/internal/backend/fake"
)

// version is set at build time with -ldflags "-X main.version=...".
var version string

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rocket-chat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print the version and exit")
	listBackends := fs.Bool("list-backends", false, "print the available backends and exit")
	configPath := fs.String("config", "", "config file (default $"+config.EnvConfigPath+" or the user config directory)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if *showVersion {
		fmt.Fprintln(stdout, "rocket-chat", buildVersion())
		return 0
	}
	if *listBackends {
		for _, name := range backend.Names() {
			fmt.Fprintln(stdout, name)
		}
		return 0
	}

	path := *configPath
	if path == "" {
		var err error
		if path, err = config.Path(); err != nil {
			fmt.Fprintln(stderr, "rocket-chat:", err)
			return 1
		}
	}
	if _, err := config.Load(path); err != nil {
		fmt.Fprintln(stderr, "rocket-chat:", err)
		return 1
	}

	fmt.Fprintln(stderr, "rocket-chat: chat is not implemented yet")
	return 1
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
