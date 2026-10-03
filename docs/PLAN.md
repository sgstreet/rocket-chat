# rocket-chat plan

A terminal chat application in Go with pluggable backends. The first two
backends are **Ollama** (local models, optional Ollama web search) and
**Gemini** (API key, grounded with Google Search).

## Decisions

| Topic | Decision |
|---|---|
| Plugin mechanism | Backends compiled in, registered from `init()` (like `database/sql` drivers). Out-of-process plugins over JSON-RPC may come later. |
| UI | Full-screen Bubble Tea app. |
| Session storage | One JSON file per session under `~/.local/share/rocket-chat`. |
| Local Ollama server | Started on demand for local hosts when none answers, stopped on exit; a running server is never stopped. |
| System prompts | Named roles: built-in General Assistant, Technical Adviser, Research Assistant; more in `config.json` (inline or file). |
| Config file | JSON only (`config.json`), created with the defaults when missing; YAML support was removed. Backend sections are strict too. |
| API keys | `backends.<name>.api_key` in `config.json` (mode 0600), saved with `--set-key` or `/key` at a hidden prompt; environment variables take precedence. |
| Gemini auth | Gemini API key only (saved key or `GEMINI_API_KEY`). No Vertex AI in v0.1. |
| Gemini search | `GoogleSearch` tool through `Models.GenerateContentStream`. On by default. |
| Ollama search | Ollama web search API, run by our own tool-calling loop. Off by default. |
| Go version | Go 1.26 (required by the `github.com/ollama/ollama` client module). |

## Features

### Core (all backends)
- Streaming replies; Esc cancels mid-answer.
- Transcript, multi-line input, status bar (backend, model, tokens, latency, web search state).
- Markdown rendering with syntax-highlighted code blocks.
- Slash commands: `/backend`, `/model`, `/role` (named or custom system prompts), `/search on|off`, `/new`, `/retry`, `/copy`, `/save`, `/export md`, `/sessions`, `/help`.
- Collapsible "thinking" blocks for reasoning models.
- Saved sessions with resume.
- One-shot mode: `rocket-chat -b gemini "question"`, `cat f | rocket-chat -p "summarize"`.

### Ollama backend
- Model list from `/api/tags`; tools capability from `/api/show`.
- Options: host, temperature, `num_ctx`, `keep_alive`, `think`.
- Clear errors when the server is down or a model is not pulled.
- **Web search** (`web_search`, `web_fetch` tools):
  - Two routes, chosen by `search.mode = auto|direct|local`:
    - `direct`: `POST https://ollama.com/api/web_search` with `Authorization: Bearer $OLLAMA_API_KEY`.
    - `local`: `POST /api/experimental/web_search` on the local server, authenticated by `ollama signin`.
    - `auto`: `direct` if `OLLAMA_API_KEY` is set, otherwise `local`.
  - Tool loop: chat → tool calls → run them → append `tool` messages → repeat, at most N rounds.
  - Only offered when the model reports the `tools` capability.
  - Raise `num_ctx` (default 32k) while search is on; truncate `web_fetch` content.
  - Results numbered [1], [2], … per reply. The citation instruction is appended to tool results, not
    the system prompt: in the system prompt it led small models to answer with made-up `[1]` markers
    instead of searching. Sources list built from what we actually gave the model, split into
    cited / consulted.
  - Search queries leave the machine, so search is off by default and the status bar shows it.
  - Tool calls and results stay inside one reply; only the answer and its sources are kept in history,
    so old results never fill the context.

### Gemini backend
- `genai.BackendGeminiAPI` with `GEMINI_API_KEY`; models listed from the API, default model from config.
- `GoogleSearch` tool on every request while search is on; `/search since 7d` uses `TimeRangeFilter`.
- Citations from `GroundingMetadata`:
  - `GroundingSupports[].Segment` offsets are **bytes** within one `Part`; join streamed chunks first,
    verify `text[start:end] == Segment.Text`, fall back to searching for `Segment.Text`.
  - Insert `[n]` markers from the end backwards; list sources under the answer.
  - Show `WebSearchQueries` under the answer. They arrive only with the final metadata, so Gemini
    emits no "Searching:" progress events.
  - One-shot mode can't add markers to text it already streamed: when stdout is not a terminal it
    holds the answer and prints it with markers; on a terminal it streams and lists sources after.
- Search Suggestions (`SearchEntryPoint`) must be shown with grounded answers (Gemini API terms):
  render suggestion texts as terminal hyperlinks to Google Search, and `/suggestions` opens the HTML.
- Grounded text is never rewritten beyond adding citation markers.
- Grounded Gemini turns are never sent to another backend (matters once mid-chat backend switching exists).

### Later
Attachments, tool calling / MCP, edit-and-branch, mid-conversation backend switching, out-of-process
plugins, themes, `URLContext` for Gemini.

## Architecture

```
cmd/rocket-chat/            flags, config, wiring
internal/chat/              Message, Role, Grounding (no backend types)
internal/backend/           Backend interface, Event, Capabilities, registry
internal/backend/backendtest/  contract tests every backend must pass
internal/backend/fake/      scripted/echo backend for tests and development
internal/backend/ollama/    Ollama chat + web search tool loop
internal/backend/gemini/    Gemini + Google Search grounding
internal/tui/               Bubble Tea model and views
internal/render/            markdown and citation formatting
internal/config/            JSON config + env overrides
internal/store/             session persistence
```

- `Backend.Chat` returns `iter.Seq2[Event, error]` and honours `context` cancellation.
- Every backend converts its own wire format into `backend.Event`; the UI has no backend-specific code.
- Backend-only features are advertised through `Capabilities` so the UI hides what does not apply.
- Each backend decodes its own settings from its `backends.<name>` config section.

## Phases

| Phase | Deliverable | Done when |
|---|---|---|
| 0. Skeleton | Module, layout, `Backend` interface, registry, fake backend, contract tests, config loading, Makefile, CI | CI green; `rocket-chat --version` runs |
| 1. One-shot mode | `-b/-m/-p` flags, stdin, streaming to stdout | Works end to end with the fake backend |
| 2a. Ollama chat | Streaming, model list, thinking, options, errors | Fake-server tests pass; manual run against real Ollama |
| 2b. Ollama web search | Search client (both routes), tool loop, capability check, citations, limits, cancel | Scripted tool-call tests pass; live search works |
| 3. Gemini | API key, Google Search grounding, byte-offset citations, suggestions, time filter | Multi-byte citation tests pass; current-events answer shows sources |
| 4. TUI | Transcript, input, status bar, markdown, cancel, commands, model picker, search/fetch progress lines | `teatest` snapshots pass |
| 5. Persistence | Sessions, `/sessions`, resume, `/export md` | Conversation survives restart |
| 6. Polish | Thinking blocks, `/copy`, `/retry`, themes, goreleaser | v0.1.0 tagged |

## Testing

- Unit tests per backend: Ollama against `httptest` servers, Gemini against recorded responses.
- `backendtest.Run` contract: `Done` is last, cancellation stops the stream, errors end the stream.
- Live tests only when `RC_LIVE=1`.
