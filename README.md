# rocket-chat

A terminal chat application in Go with pluggable backends:

- **Ollama**: local models, with optional Ollama web search.
- **Gemini**: answers grounded with Google Search.
- **Z.ai**: the GLM models, with optional Z.ai web search.

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

Run `rocket-chat` with no prompt for the interactive chat. The screen has the conversation at the
top and, below a rule, the composer: a status bar (backend, model, web search, role), the input box,
and a hint line with the main keys. `/lines <n>` sets how many lines the input box shows (1–20,
default 3; `ui.input_lines` in the config makes it stick).

- Enter sends, Alt+Enter (or Ctrl+J) adds a line, Esc stops an answer, PgUp/PgDn or the mouse
  wheel scroll, Ctrl+T shows or hides the model's reasoning, Ctrl+C quits.
- Replies stream in as plain text and are rendered as Markdown (headings, lists, tables, highlighted
  code) once complete. `/markdown off`, or `"ui": {"markdown": false}`, shows them exactly as the
  model wrote them. Your own messages and notices are never rendered. Saved chats, `/export` and
  `/copy` always keep the model's original text.
- Up and Down (or Ctrl+P and Ctrl+N) recall earlier inputs, messages and commands alike, from this
  and earlier chats. A recalled input can be edited before sending; edits are kept while you move
  through the list, and Down past the newest entry brings back what you were typing. In a
  multi-line input, Up and Down move between lines first.
- Tab completes commands and their arguments: `/ba` → `/backend `, `/role show te` →
  `/role show technical`, `/backend `, `/model ` (the model list is fetched on the first Tab), `/search `,
  `/key `. With several matches the bottom line lists them and Tab (Shift+Tab backwards)
  cycles through them.
- The mouse (on by default):
  - The wheel scrolls the conversation.
  - Links are underlined. Click one to open it in your browser; links in replies, Gemini sources and URLs you typed all
    work. Only `http`, `https` and `mailto` links are opened; over SSH the link is copied instead,
    since a browser would open on the remote machine.
  - To select or paste text, use your terminal's own selection and paste: most terminals do their
    own selection while Shift is held (Option on macOS), and their paste key (Ctrl+Shift+V,
    Cmd+V) works as usual. To leave the mouse to the terminal entirely, set
    `"ui": {"mouse": false}`.
- `/copy` copies the last reply or one of its code blocks. Copying uses the terminal (OSC 52, which
  also works over SSH) and the system clipboard where there is one, so it works in terminals
  without OSC 52 such as GNOME Terminal.
- Click to choose where keys go. Clicking the conversation lets Up/Down, PgUp/PgDn and Home/End
  scroll it (the bottom line says so); clicking the input box, pressing Esc or Enter, or just typing
  goes back to the input. Clicking inside the input puts the cursor there, in multi-line input too.
  Clicking a link opens it without moving the focus.
- `/help` lists the commands: `/backend`, `/model` (lists models; pick by number or name, or
  `/model default`),
  `/role`, `/search on|off|default`, `/key`, `/markdown on|off`, `/lines`, `/context`, `/compact`, `/thinking`, `/retry`, `/new`,
  `/sessions`, `/resume`, `/export`, `/copy`, `/quit`.
- `/copy` copies the last reply, `/copy code` its last code block and `/copy 2` its second one. It
  uses the terminal's OSC 52 clipboard support, so it works over SSH; in tmux, enable
  `set -g set-clipboard on`.
- `/context` shows how much of the model's context window the conversation takes: the tokens in use
  out of the window, the system prompt's share, the number of messages and the largest one. The
  count comes from the backend after each reply; before the first reply, or when a backend cuts a
  conversation that is too long, it is estimated from the text (about four characters a token) and
  marked `~`. The window comes from the backend: Gemini reports each model's limit, Ollama the
  context it runs the model with (known once the model is loaded, or `num_ctx` when set), and for
  Z.ai a built-in table or the `context_window` setting. From 80% the status line shows
  `context NN%`. Ollama's default context is small, and when a conversation outgrows it Ollama
  silently drops the start, so the model no longer sees your earlier messages; `/context` says
  when this happens. Raise `backends.ollama.num_ctx` if your GPU has room.
- `/compact` makes room in a long chat: the model writes a summary of the older messages, and from
  then on the summary is sent in their place (added to the system prompt), followed by the latest
  messages, which are kept word for word (the last 4 by default, `compact.keep`). `/compact <focus>`
  says what the summary should keep, for example `/compact keep the code and the decisions`. The
  older messages stay on screen, marked `compacted`, and in `/export`; saved chats keep the summary,
  so `/resume` carries on from it. Compacting again folds the earlier summary into the new one, and
  Esc stops a compaction in progress. A summary loses detail, so exact code or numbers from early in
  the chat may come back paraphrased. With `compact.auto` set (for example `0.85`), the chat
  compacts by itself before sending once it takes that share of the window. Gemini's answers
  grounded with Google Search may only go back to Gemini, so a summary Gemini writes of them is only
  sent to Gemini.
- The chat remembers the backend you choose with `/backend` and, for each backend, the model you
  choose with `/model`, in `~/.local/share/rocket-chat/state.json` (with the role). A new chat
  starts with `-b` and `-m` if given, otherwise the remembered backend and its remembered model,
  otherwise `default_backend` and the backend's `model` setting. `-b` and `-m` apply to that run
  only and are not remembered, and `/model default` goes back to the backend's default model.
  One-shot answers do not use the remembered choices, so scripts are not affected by what you pick
  in a chat. If the remembered backend cannot be opened (for example its key was removed), the
  chat starts with `default_backend` and says why.
- Switching from Gemini to another backend keeps the conversation, but answers grounded with
  Google Search are not sent to the other backend (Gemini API terms).
- Chats are saved after every reply, in `~/.local/share/rocket-chat/sessions/` (see
  [Saved chats](#saved-chats)).

Give a prompt for a single answer on stdout:

```sh
rocket-chat -b gemini "who won the last world cup?"
cat notes.txt | rocket-chat -p "summarize this"
git diff | rocket-chat -b ollama -m qwen3 -p "write a commit message"
```

- The answer and its sources go to stdout. Search progress goes to stderr when stderr is a
  terminal; `-v` also prints token usage and timing, and `--thinking` prints the model's reasoning.
- `--search` / `--search=false` overrides the backend's web search default.
- `--render` prints the answer as rendered Markdown (styles, wrapping to the terminal width, code
  highlighting, clickable links) once it is complete, instead of streaming the raw text. The style
  follows `ui.theme`. Piped through `--render`, the output keeps its colour codes (use `less -R`).
- Exit codes: 0 success, 1 error, 2 usage error, 130 interrupted (Ctrl+C).

## Saved chats

Interactive chats are saved after every reply, one JSON file per chat, in:

```
~/.local/share/rocket-chat/sessions/
```

That is the default on Linux, macOS and Windows (`C:\Users\<you>\.local\share\rocket-chat\sessions`).
If `$XDG_DATA_HOME` is set, the directory is `$XDG_DATA_HOME/rocket-chat/sessions/` instead, and
`sessions.dir` in the config file overrides both.

- Files are named by the time the chat started (UTC), e.g. `20261003-174512-a1b2c3.json`, and hold the
  messages, backend, model, role or system prompt, and any Gemini sources.
- The directory is readable only by you (mode 700) and each file is mode 600.
- One-shot answers (`rocket-chat "question"`) are not saved.
- `/sessions` lists saved chats, `/resume [number]` continues one, and `/export [file]` writes the
  current chat as Markdown. `rocket-chat --resume last` (or a session ID) reopens one from the
  command line; `-b`, `-m` and `-s` override its saved backend, model and system prompt.
- To delete a chat, delete its file. To stop saving, set `"sessions": {"save": false}`.

What you type in the chat (for Up/Down) is kept separately, in `~/.local/share/rocket-chat/history`
(next to `sessions/`, mode 600, the newest 1000 entries). Delete the file to clear it, or set
`"ui": {"history": false}` to keep inputs only for the current chat. A `/key` command with a key in
it is refused and never recorded.

## Roles

A role is a named system prompt. Three are built in:

| ID | Name | For |
|---|---|---|
| `general` | General Assistant | Clear answers on any topic |
| `technical` | Technical Adviser | Engineering and software questions, trade-offs, working code |
| `research` | Research Assistant | Finding and weighing sources, with citations; turns web search on |

In the chat, everything about the system prompt goes through `/role`:

| Command | Does |
|---|---|
| `/role` | Shows the prompt in use, in full, and lists the roles |
| `/role technical` | Switches role (display names such as `Technical Adviser` work too) |
| `/role custom <text>` | Uses your own prompt for this chat; the status bar shows `role: custom` |
| `/role off` | No system prompt |
| `/role show technical` | Prints a role's full prompt without switching to it |

On the command line, `--role technical` (or `-r`) picks a role, `-s "text"` gives your own prompt
instead, `--list-roles` lists them and `--show-role technical` prints one, ready to save to a file and
edit. Saved chats remember their role or custom prompt.

Which prompt a run starts with:

1. `--role <name>` or `-s "text"`, if given (`--role off` for none).
2. In the chat, otherwise the role you last chose with `/role`, including `/role custom <text>` and
   `/role off`. It is kept in `~/.local/share/rocket-chat/state.json`, and `--list-roles` marks the
   role a new chat starts with. One-shot answers skip this step, so scripts are not affected by
   what you pick in a chat.
3. Otherwise `default_role` from `config.json`: the General Assistant unless you set another role,
   or `"off"` for no system prompt.

A resumed chat keeps the role it was saved with.

Add your own, or replace a built-in by reusing its ID, in `config.json`. Long prompts can live in a
file, relative to the config file's directory:

```json
{
  "default_role": "general",
  "roles": {
    "reviewer": {
      "name": "Code Reviewer",
      "description": "Strict reviews of diffs and code",
      "file": "prompts/reviewer.md"
    },
    "brief": {
      "name": "Brief",
      "prompt": "Answer in at most three sentences.",
      "search": false
    }
  }
}
```

Each entry under `roles` is keyed by the role's ID: lowercase letters, digits and dashes, used with
`--role` and `/role`. Any other ID adds a role. Reusing a built-in's ID (`general`, `technical`,
`research`) changes that role: what you set replaces the built-in's value, and what you leave out is
kept. For example, `"technical": {"search": true}` keeps the Technical Adviser prompt and turns web
search on with it.

| Field | Meaning |
|---|---|
| `name` | Display name in lists and the status bar; defaults to the ID (or, for a built-in, its name) |
| `description` | One line shown in `/role` and `--list-roles` |
| `prompt` | The system prompt, written inline |
| `file` | A file holding the system prompt, instead of `prompt` |
| `search` | `true` or `false` turns web search on or off while the role is in use, unless `--search` or `/search` says otherwise; leave it out to keep the backend's default |

`default_role` (default `general`) is the role used until you choose one with `/role`; `"off"` means
no system prompt.

### Prompts in files

A new role needs exactly one of `prompt` or `file` (a changed built-in may leave both out to keep
its prompt); giving both is an error that names the role. Use `file` for long prompts, which are awkward as one JSON string:

- A relative path is relative to the config file's directory, so with the default config
  `prompts/technical.md` means `~/.config/rocket-chat/prompts/technical.md` on Linux. Paths starting with
  `~/` are under your home directory, and absolute paths are used as they are.
- The file's contents are the prompt, word for word, with surrounding whitespace removed.
  It is plain text: the `.md` extension is only a name, and the file is not parsed or rendered.
- The file is read when rocket-chat starts, so restart it after editing. A missing or empty file is
  an error.

To customise a built-in role, start from its current prompt:

```sh
mkdir -p ~/.config/rocket-chat/prompts
rocket-chat --show-role technical > ~/.config/rocket-chat/prompts/technical.md
```

Edit the file, point the role at it in `config.json`, and check the result with
`rocket-chat --show-role technical`. The role keeps its name, description and search setting:

```json
"roles": {
  "technical": { "file": "prompts/technical.md" }
}
```

## Releasing

Releases are built by GoReleaser in the Release workflow: Linux, macOS and Windows archives on amd64
and arm64, plus `checksums.txt`, attached to a GitHub release. Start one either way:

- Push a tag: `git tag -a v0.2.0 -m "rocket-chat v0.2.0" && git push origin v0.2.0`.
- Or run the Release workflow from the Actions tab (or the API) on `main` with a version such as
  `v0.2.0`. It checks the version, runs the tests, creates and pushes the tag, then releases.

Release notes come from `docs/release-notes/<version>.md` when that file exists on `main` (write it
before starting the release); otherwise the release lists the merged commits. Link each change to
its pull request, as in `docs/release-notes/v0.5.0.md`, and end with a compare link to the previous
release. To change the notes of
a release that is already published, edit or add its file on `main` and run the Release notes
workflow with the version.

`make snapshot` builds the same archives locally without publishing.

## API keys

Gemini needs an API key (create one at https://aistudio.google.com/apikey), and so does Z.ai
(https://z.ai/manage-apikey/apikey-list). Ollama web search can use
an ollama.com API key (https://ollama.com/settings/keys) instead of `ollama signin`. Save them in the
config file:

```sh
rocket-chat --set-key gemini            # prompts for the key; nothing is shown as you type
rocket-chat --set-key zai < key.txt     # or pipe it in
rocket-chat -b gemini --set-key AIza…   # or give it directly (it stays in your shell history)
rocket-chat --remove-key gemini
```

In the chat, `/key` shows where each key comes from, `/key gemini` asks for the key at a hidden
prompt and saves it, and `/key gemini clear` removes it. Starting a chat with Gemini when no key is
set asks for one before the chat opens; so does Z.ai.

Keys are stored as `backends.<name>.api_key` in `config.json`, which rocket-chat keeps readable only by
you (mode 0600). The environment variables `GEMINI_API_KEY` (or `GOOGLE_API_KEY`),
`ZAI_API_KEY` and `OLLAMA_API_KEY` still work and take precedence over a saved key.

## Configuration

Settings live in a JSON file: the one named by `--config` or `$ROCKET_CHAT_CONFIG`, otherwise
`rocket-chat/config.json` under the user config directory (`~/.config` on Linux). If the file does
not exist, rocket-chat creates it with the default settings (and says so on stderr), ready to edit.
`ROCKET_CHAT_BACKEND` overrides `default_backend`. Unknown settings are errors, so
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
| `default_backend` | `ollama` | Backend used when `-b` is not given and no backend was chosen with `/backend` in a chat |
| `ui.theme` | `auto` | `auto` (follow the terminal), `dark` or `light` |
| `ui.mouse` | `true` | Handle the mouse: wheel scrolls, click moves the focus and opens links |
| `ui.history` | `true` | Save typed inputs for Up/Down between chats |
| `ui.input_lines` | `3` | How many lines the input box shows, 1–20 (`/lines <n>` changes it in a chat) |
| `compact.auto` | `0` (off) | Compact a chat before sending once it takes this share of the context window, 0.5–0.95 |
| `compact.keep` | `4` | How many of the latest messages `/compact` keeps word for word |
| `ui.markdown` | `true` | Render finished replies as Markdown; `false` shows the model's text as it is (`/markdown` switches it in a chat) |
| `sessions.save` | `true` | Save interactive chats |
| `sessions.dir` | `~/.local/share/rocket-chat/sessions` (or `$XDG_DATA_HOME/rocket-chat/sessions`) | Where chats are saved; `~` is expanded |

`backends.ollama`:

| Setting | Default | Meaning |
|---|---|---|
| `host` | `$OLLAMA_HOST`, then `http://127.0.0.1:11434` | Ollama server |
| `api_key` | none | ollama.com API key for web search; `$OLLAMA_API_KEY` takes precedence (see [API keys](#api-keys)) |
| `model` | none | Model used when `-m` is not given |
| `cloud_models` | `true` | List the ollama.com cloud models in `/model` and `--list-models` (see below) |
| `temperature` | model default | Sampling temperature |
| `num_ctx` | server default | Context window in tokens; Ollama's default is small, see `/context` |
| `keep_alive` | Ollama default | How long the model stays loaded |
| `unload_on_exit` | `true` | Unload the models used when rocket-chat exits, unless another running copy uses them (see below) |
| `unload_on_switch` | `true` | Unload a model when the chat moves to another model or backend, unless another running copy uses it |
| `think` | model default | `true`/`false` turns reasoning on or off |
| `search.enabled` | `false` | Web search default; `--search` overrides |
| `search.mode` | `auto` | `auto`, `direct` or `local` (see below) |
| `search.max_results` | `5` | Results per search, 1–10 |
| `search.max_rounds` | `5` | Tool-call rounds before the model must answer |
| `search.max_result_chars` | `2000` | Each search result is cut to this; the model can fetch the page |
| `search.max_fetch_chars` | `8000` | Fetched pages are cut to this |
| `search.num_ctx` | `32768` | Minimum context window while searching |
| `search.api_url` | `https://ollama.com` | Web search API for `direct` mode |
| `serve.auto_start` | `true` | Start `ollama serve` when no server answers at a local host, and stop it when the last rocket-chat using it exits |
| `serve.command` | `ollama` | The ollama executable, found in `PATH` |
| `serve.start_timeout` | `"30s"` | How long to wait for a started server to answer |

### Starting Ollama automatically

When the Ollama host is this machine (`localhost`, `127.0.0.1` or `::1`) and nothing answers there,
rocket-chat runs `ollama serve` for that address, says so, and waits until it answers. Remote hosts
are never started. The started server's output goes to `rocket-chat/ollama-serve.log` in the user
cache directory (`~/.cache` on Linux). Set `"serve": {"auto_start": false}` to turn this off.

Copies of rocket-chat running at the same time share the server. A second copy uses the server the
first one started, and the server stops when the last copy using it exits, whichever copy that is.
It also stops if that copy crashes or its terminal is closed. On Linux and macOS, rocket-chat keeps
track of this with a small supervisor process (rocket-chat itself, running `ollama serve`) and a
registry of the copies using the server, in `$XDG_RUNTIME_DIR/rocket-chat` (or the user cache
directory). On Windows, the server stops when the copy that started it exits.

A server rocket-chat did not start, such as the `ollama` system service the Linux installer sets up,
is never stopped. It keeps a model in memory for `keep_alive` after its last request (5 minutes by
default), which can hold several GB of GPU memory, so when rocket-chat exits it asks the server to
unload the models it chatted with. Models another running copy of rocket-chat has used stay loaded.
Set `"unload_on_exit": false` to leave the models loaded, for example when `keep_alive` is set to
keep them warm. `ollama ps` shows what is loaded.

Switching model in a chat (`/model`, or `/resume` of a chat with another model) unloads the model
the chat leaves, as does switching to another backend with `/backend`, so only the model in use
holds GPU memory. A notice says when a model is unloaded. A model the chat never sent a message to,
a cloud model, and a model another running copy of rocket-chat has used are left alone. Set
`"unload_on_switch": false` to keep models loaded when switching.

### Ollama cloud models

Ollama runs some models on ollama.com instead of your GPU. `/model` and `--list-models` list them
after your local models, marked `cloud`, with the names your Ollama server knows them by, such as
`gpt-oss:120b-cloud` or `kimi-k3:cloud`. The list comes from `https://ollama.com/api/tags` and is
fetched once per session. If ollama.com cannot be reached, only the local models are listed, with a
note saying why. Set `"cloud_models": false` to list local models only.

A cloud model is used like any other (`-m gpt-oss:120b-cloud` or `/model kimi-k3:cloud`): rocket-chat
sends the request to your Ollama server, which passes it on to ollama.com. Nothing is downloaded,
but the server must be signed in to ollama.com once with `ollama signin`; rocket-chat says so if it
is not. Older Ollama versions only run cloud models that have been pulled first
(`ollama pull gpt-oss:120b-cloud`); update Ollama to use the rest. Cloud models are not unloaded on
exit, since they use no memory on your machine.

### Ollama web search

With search on, the model can call `web_search` and `web_fetch` tools. Results are numbered, the model
is asked to cite them as `[n]`, and the answer is followed by the sources it cited and the ones it only
consulted. The model must support tool calling (`ollama show <model>` lists `tools`).

Search queries go to ollama.com even though the model runs locally, which is why search is off by
default. It needs an ollama.com account, used in one of two ways:

- `direct`: save an API key (`rocket-chat --set-key ollama`) or set `OLLAMA_API_KEY`, and requests go
  straight to `https://ollama.com/api/web_search`.
- `local`: run `ollama signin`; requests go through the local Ollama server.
- `auto` (default): `direct` when an API key is set, otherwise `local`.

`rocket-chat --list-models` lists the models installed on the Ollama server.

### Gemini

`backends.gemini`:

| Setting | Default | Meaning |
|---|---|---|
| `api_key` | none | Gemini API key; `$GEMINI_API_KEY` or `$GOOGLE_API_KEY` take precedence (see [API keys](#api-keys)) |
| `model` | `gemini-flash-latest` | Model used when `-m` is not given |
| `temperature` | model default | Sampling temperature |
| `think` | off | `true` returns the model's reasoning (`--thinking` shows it) |
| `search.enabled` | `true` | Grounding with Google Search; `--search=false` turns it off |
| `search.since` | none | Only use pages from this long ago until now, e.g. `"168h"` |
| `base_url` | Gemini API | API endpoint, for proxies |

Save a key from https://aistudio.google.com/apikey with `rocket-chat --set-key gemini`, or set
`GEMINI_API_KEY` (or `GOOGLE_API_KEY`).
`rocket-chat -b gemini --list-models` lists the models your key can use.

Gemini decides for itself when a question needs a search; answers it gives without one have no
sources. Grounded answers list their sources, the searches Gemini ran, and Google's search suggestions, which
Google's terms require to be shown with grounded results. When stdout is not a terminal, the answer is
printed once complete with `[n]` citation markers after each supported passage; on a terminal it
streams as it is generated and the sources follow. In the interactive chat, source titles and
suggestions are clickable links (OSC 8), because Gemini's source URLs are long redirect links.

### Z.ai

`backends.zai` (choose it with `-b zai`, `/backend zai` or `"default_backend": "zai"`):

| Setting | Default | Meaning |
|---|---|---|
| `api_key` | none | Z.ai API key; `$ZAI_API_KEY` takes precedence (see [API keys](#api-keys)) |
| `model` | `glm-5.3` | Model used when `-m` is not given |
| `endpoint` | `auto` | `auto`, `general` (pay-as-you-go), `coding` (GLM Coding Plan), or an API URL |
| `temperature` | model default | Sampling temperature |
| `think` | model default | `true`/`false` turns reasoning on or off |
| `search.enabled` | `false` | Web search default; `--search` overrides |
| `search.engine` | `search-prime` | Z.ai search engine |
| `search.count` | `5` | Results per search, 1–50 |
| `search.recency` | no limit | `oneDay`, `oneWeek`, `oneMonth`, `oneYear` or `noLimit` |
| `context_window` | built-in table | The model's context window in tokens, for `/context` and `compact.auto`; the table knows `glm-5.3` and `glm-5.3-flash` (1,048,576) |

Save a key from https://z.ai/manage-apikey/apikey-list with `rocket-chat --set-key zai`, or set
`ZAI_API_KEY`. `rocket-chat -b zai --list-models` lists the models.

Z.ai has two endpoints: pay-as-you-go keys use the general one, GLM Coding Plan subscriptions the
coding one. With `endpoint` set to `auto`, rocket-chat starts with the general endpoint and, if Z.ai
answers that the key has no balance there, moves to the coding endpoint for the rest of the session
and says so. Set `endpoint` to `coding` (or `general`) to skip that check.

The models' reasoning streams as thinking (Ctrl+T in the chat, `--thinking` for one answer). With
search on, Z.ai searches the web for the question and the model is asked to cite the results as
`[n]`; the answer is followed by the sources it cited and the ones it only consulted. Searches count
against your Z.ai plan, so search is off unless you turn it on.
