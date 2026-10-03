# rocket-chat

A terminal chat application in Go with pluggable backends:

- **Ollama**: local models, with optional Ollama web search.
- **Gemini**: answers grounded with Google Search.

Status: early development. See [docs/PLAN.md](docs/PLAN.md) for the feature set and roadmap.

## Installing

Download a binary for Linux, macOS or Windows from the
[releases page](https://github.com/sgstreet/rocket-chat/releases), or build from source.

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

Run `rocket-chat` with no prompt for the interactive chat:

- Enter sends, Alt+Enter (or Ctrl+J) adds a line, Esc stops an answer, PgUp/PgDn scroll,
  Ctrl+T shows or hides the model's reasoning, Ctrl+C quits.
- `/help` lists the commands: `/backend`, `/model` (lists models; pick by number or name),
  `/system`, `/search on|off|default`, `/thinking`, `/retry`, `/new`, `/sessions`, `/resume`,
  `/export`, `/copy`, `/quit`.
- `/copy` copies the last reply, `/copy code` its last code block and `/copy 2` its second one. It
  uses the terminal's OSC 52 clipboard support, so it works over SSH; in tmux, enable
  `set -g set-clipboard on`.
- Switching from Gemini to another backend keeps the conversation, but answers grounded with
  Google Search are not sent to the other backend (Gemini API terms).
- Chats are saved after every reply. `/sessions` lists them, `/resume [number]` continues one,
  `/export [file]` writes the chat as Markdown, and `rocket-chat --resume last` (or a session ID)
  reopens one from the command line. `-b`, `-m` and `-s` override the saved backend, model and
  system prompt.

Give a prompt for a single answer on stdout:

```sh
rocket-chat -b gemini "who won the last world cup?"
cat notes.txt | rocket-chat -p "summarize this"
git diff | rocket-chat -b ollama -m qwen3 -p "write a commit message"
```

- The answer and its sources go to stdout. Search progress goes to stderr when stderr is a
  terminal; `-v` also prints token usage and timing, and `--thinking` prints the model's reasoning.
- `--search` / `--search=false` overrides the backend's web search default.
- Exit codes: 0 success, 1 error, 2 usage error, 130 interrupted (Ctrl+C).

## Configuration

The config file is `$ROCKET_CHAT_CONFIG`, or `rocket-chat/config.yaml` under the user config
directory (`~/.config` on Linux). `ROCKET_CHAT_BACKEND` overrides `default_backend`.

```yaml
ui:
  theme: auto                      # auto (follow the terminal), dark or light
sessions:
  save: true                       # save interactive chats (default true)
  dir: ~/chats                     # default: $XDG_DATA_HOME/rocket-chat/sessions
```

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
    search:
      enabled: false               # web search default; --search overrides
      mode: auto                   # auto | direct | local (see below)
      max_results: 5               # results per search, 1-10
      max_rounds: 5                # tool-call rounds before the model must answer
      max_result_chars: 2000       # each search result is cut to this; the model can web_fetch the page
      max_fetch_chars: 8000        # fetched pages are truncated to this
      num_ctx: 32768               # minimum context window while searching
```

### Ollama web search

With search on, the model can call `web_search` and `web_fetch` tools. Results are numbered, the model
is asked to cite them as `[n]`, and the answer is followed by the sources it cited and the ones it only
consulted. The model must support tool calling (`ollama show <model>` lists `tools`).

Search queries go to ollama.com even though the model runs locally, which is why search is off by
default. It needs an ollama.com account, used in one of two ways:

- `direct`: set `OLLAMA_API_KEY` and requests go straight to `https://ollama.com/api/web_search`.
- `local`: run `ollama signin`; requests go through the local Ollama server.
- `auto` (default): `direct` when `OLLAMA_API_KEY` is set, otherwise `local`.

`rocket-chat --list-models` lists the models installed on the Ollama server.

### Gemini

```yaml
backends:
  gemini:
    model: gemini-3.8-flash        # default: gemini-flash-latest
    temperature: 0.7               # optional
    think: true                    # optional; return the model's reasoning (--thinking shows it)
    search:
      enabled: true                # Grounding with Google Search; on by default
      since: 168h                  # optional; only pages from the last week
```

Set `GEMINI_API_KEY` (or `GOOGLE_API_KEY`) to a key from https://aistudio.google.com/apikey.
`rocket-chat -b gemini --list-models` lists the models your key can use.

Gemini decides for itself when a question needs a search; answers it gives without one have no
sources. Grounded answers list their sources, the searches Gemini ran, and Google's search suggestions, which
Google's terms require to be shown with grounded results. When stdout is not a terminal, the answer is
printed once complete with `[n]` citation markers after each supported passage; on a terminal it
streams as it is generated and the sources follow. In the interactive chat, source titles and
suggestions are clickable links (OSC 8), because Gemini's source URLs are long redirect links.
