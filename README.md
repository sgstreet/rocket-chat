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

## Releasing

Releases are built by GoReleaser in the Release workflow: Linux, macOS and Windows archives on amd64
and arm64, plus `checksums.txt`, attached to a GitHub release. Start one either way:

- Push a tag: `git tag -a v0.2.0 -m "rocket-chat v0.2.0" && git push origin v0.2.0`.
- Or run the Release workflow from the Actions tab (or the API) on `main` with a version such as
  `v0.2.0`. It checks the version, runs the tests, creates and pushes the tag, then releases.

`make snapshot` builds the same archives locally without publishing.

## Configuration

Settings live in a JSON file: the one named by `--config` or `$ROCKET_CHAT_CONFIG`, otherwise
`rocket-chat/config.json` under the user config directory (`~/.config` on Linux). Without a file the
defaults apply. `ROCKET_CHAT_BACKEND` overrides `default_backend`. Unknown settings are errors, so
typos are caught, and syntax errors give the line and column.

```json
{
  "default_backend": "ollama",
  "ui": { "theme": "auto" },
  "sessions": { "save": true, "dir": "~/chats" },
  "backends": {
    "ollama": {
      "model": "qwen3:4b",
      "keep_alive": "10m",
      "search": { "enabled": false, "max_results": 5 }
    },
    "gemini": {
      "model": "gemini-flash-lite-latest",
      "search": { "enabled": true, "since": "168h" }
    }
  }
}
```

Durations such as `keep_alive` and `since` are strings with a unit: `"30s"`, `"10m"`, `"168h"`.

| Setting | Default | Meaning |
|---|---|---|
| `default_backend` | `ollama` | Backend used when `-b` is not given |
| `ui.theme` | `auto` | `auto` (follow the terminal), `dark` or `light` |
| `sessions.save` | `true` | Save interactive chats |
| `sessions.dir` | `$XDG_DATA_HOME/rocket-chat/sessions` | Where chats are saved; `~` is expanded |

`backends.ollama`:

| Setting | Default | Meaning |
|---|---|---|
| `host` | `$OLLAMA_HOST`, then `http://127.0.0.1:11434` | Ollama server |
| `model` | none | Model used when `-m` is not given |
| `temperature` | model default | Sampling temperature |
| `num_ctx` | model default | Context window in tokens |
| `keep_alive` | Ollama default | How long the model stays loaded |
| `think` | model default | `true`/`false` turns reasoning on or off |
| `search.enabled` | `false` | Web search default; `--search` overrides |
| `search.mode` | `auto` | `auto`, `direct` or `local` (see below) |
| `search.max_results` | `5` | Results per search, 1–10 |
| `search.max_rounds` | `5` | Tool-call rounds before the model must answer |
| `search.max_result_chars` | `2000` | Each search result is cut to this; the model can fetch the page |
| `search.max_fetch_chars` | `8000` | Fetched pages are cut to this |
| `search.num_ctx` | `32768` | Minimum context window while searching |
| `search.api_url` | `https://ollama.com` | Web search API for `direct` mode |

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

`backends.gemini`:

| Setting | Default | Meaning |
|---|---|---|
| `model` | `gemini-flash-latest` | Model used when `-m` is not given |
| `temperature` | model default | Sampling temperature |
| `think` | off | `true` returns the model's reasoning (`--thinking` shows it) |
| `search.enabled` | `true` | Grounding with Google Search; `--search=false` turns it off |
| `search.since` | none | Only use pages from this long ago until now, e.g. `"168h"` |
| `base_url` | Gemini API | API endpoint, for proxies |

Set `GEMINI_API_KEY` (or `GOOGLE_API_KEY`) to a key from https://aistudio.google.com/apikey.
`rocket-chat -b gemini --list-models` lists the models your key can use.

Gemini decides for itself when a question needs a search; answers it gives without one have no
sources. Grounded answers list their sources, the searches Gemini ran, and Google's search suggestions, which
Google's terms require to be shown with grounded results. When stdout is not a terminal, the answer is
printed once complete with `[n]` citation markers after each supported passage; on a terminal it
streams as it is generated and the sources follow. In the interactive chat, source titles and
suggestions are clickable links (OSC 8), because Gemini's source URLs are long redirect links.
