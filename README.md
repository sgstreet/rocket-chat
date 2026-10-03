# rocket-chat

A terminal chat application in Go with pluggable backends:

- **Ollama**: local models, with optional Ollama web search.
- **Gemini**: answers grounded with Google Search.

Status: early development. See [docs/PLAN.md](docs/PLAN.md) for the feature set and roadmap.

## Building

Requires Go 1.26 or newer.

```sh
make build        # bin/rocket-chat
make test         # go test -race ./...
make lint         # go vet + golangci-lint
./bin/rocket-chat --version
./bin/rocket-chat --list-backends
```

## Configuration

The config file is `$ROCKET_CHAT_CONFIG`, or `rocket-chat/config.yaml` under the user config
directory (`~/.config` on Linux). `ROCKET_CHAT_BACKEND` overrides `default_backend`.

```yaml
default_backend: ollama
backends:
  fake:
    delay: 50ms
```
