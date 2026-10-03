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

## Usage

```sh
rocket-chat -b gemini "who won the last world cup?"
cat notes.txt | rocket-chat -p "summarize this"
git diff | rocket-chat -b ollama -m qwen3 -p "write a commit message"
```

- The answer and its sources go to stdout. Search progress goes to stderr when stderr is a
  terminal; `-v` also prints token usage and timing, and `--thinking` prints the model's reasoning.
- `--search` / `--search=false` overrides the backend's web search default.
- Exit codes: 0 success, 1 error, 2 usage error, 130 interrupted (Ctrl+C).
- Interactive mode is not implemented yet.

## Configuration

The config file is `$ROCKET_CHAT_CONFIG`, or `rocket-chat/config.yaml` under the user config
directory (`~/.config` on Linux). `ROCKET_CHAT_BACKEND` overrides `default_backend`.

```yaml
default_backend: ollama
backends:
  ollama:
    host: http://127.0.0.1:11434   # default: $OLLAMA_HOST, then 127.0.0.1:11434
    model: qwen3:4b                # used when -m is not given
    temperature: 0.7               # optional; model default otherwise
    num_ctx: 32768                 # optional context window
    keep_alive: 10m                # optional; how long the model stays loaded
    think: false                   # optional; omit to use the model's default
```

`rocket-chat --list-models` lists the models installed on the Ollama server.
