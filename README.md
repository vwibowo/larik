# Larik

A terminal coding agent written in Go with a [Bubble Tea](https://github.com/charmbracelet/bubbletea) UI. It works with Anthropic, OpenAI, Google Gemini, and OpenAI-compatible endpoints including NVIDIA NIM, OpenRouter, Ollama, Groq, DeepSeek, xAI, Mistral, Together, and LM Studio.

## Quick start

To build and install `larik` into `/usr/local/bin`:

```bash
./install.sh
```

The installer uses `sudo` when needed. Set `PREFIX` or `BINDIR` to install elsewhere, for example:

```bash
PREFIX="$HOME/.local" ./install.sh
```

To build without installing:

```bash
go build -o larik ./cmd/larik
```

```bash
export ANTHROPIC_API_KEY=...   # or OPENAI_API_KEY / GEMINI_API_KEY
./larik
```

After installation, run `larik` from any directory.

The first time you run `larik` in a terminal with nothing configured, a setup wizard walks through connecting a model. It finds a running Ollama or LM Studio server and any API keys in your environment, tests the connection, lists the provider's models (with Ollama, it can also download a recommended model, or any model you name, with a progress bar), and saves your choice to `~/.config/larik/config.json` or private project settings under `~/.config/larik/projects/`. Run `/connect` at any time to add another provider.

With no `--model` and no saved default, Larik picks the default model of the first provider whose key is set:

| Key | Default model |
|---|---|
| `ANTHROPIC_API_KEY` | `anthropic/claude-opus-5` |
| `OPENAI_API_KEY` | `openai/gpt-5.5` |
| `GEMINI_API_KEY` | `gemini/gemini-3.8-flash` |

```bash
./larik --model openai/gpt-5.5
./larik --model ollama/qwen3-coder
./larik --model openrouter/anthropic/claude-sonnet-5
./larik -p "summarize this repo"                  # headless, text output
./larik -p --output json --mode yolo "run tests"  # one JSON event per line
./larik -c                                        # continue the last session here
./larik --resume 20260926-2358                    # resume by id prefix
./larik -c --fork                                 # branch the last session instead of appending
./larik serve                                     # HTTP + SSE API (see Server mode)
./larik bench --models worker,anthropic/claude-haiku-4-5  # compare models on self-checking tasks (see Mixing cheap and strong models)
```

### ChatGPT plan (Codex models)

To use Codex models on your ChatGPT plan instead of an API key, run `/connect codex` (or pick "ChatGPT (Codex)" in the first-run wizard). larik opens the ChatGPT sign-in page in your browser and receives the result on `localhost:1455`, the same OAuth flow the Codex CLI uses. It keeps its own sign-in in `~/.config/larik/chatgpt-auth.json`, readable only by you, refreshes it before it expires, and never touches the Codex CLI's login. Sign out from `/providers` (select codex, press `d` twice).

For NVIDIA's hosted model catalog, run `/connect nvidia-nim` and enter a key, or set `NVIDIA_API_KEY`; for example, select an available `nvidia-nim/<model-id>` after connection. [NVIDIA's developer access](https://docs.api.nvidia.com/nim/docs/product) is for prototyping, research, development, and testing. Self-hosted NIM can use a custom OpenAI-compatible provider instead. Gemini and Claude in Larik use API keys, not Gemini CLI or Claude Code subscription credentials.

```bash
./larik --model codex/gpt-6-luna   # the fast, affordable one; good for testing
```

### Local models with Ollama

Ollama loads models with a small context window by default (4096 tokens), and Larik's system prompt and tool definitions alone use most of that. Larik talks to Ollama through its native chat API and asks for a 32768-token window (`num_ctx`), or the model's own maximum if that is smaller, so no server setting is needed.

A larger window uses more memory for the model's KV cache. To change it, set `context_length` for the provider in `~/.config/larik/config.json`:

```json
{ "providers": { "ollama": { "context_length": 65536 } } }
```

Larik reads back the window Ollama actually loaded, uses it for the context percentage and compaction, and warns when a request fills it. It also warns if the chosen model can't call tools. Small models such as `qwen3:4b` handle simple read/edit/test loops but can be unreliable with delegation.

### Mixing cheap and strong models

An agent spends most of its tokens reading: searching files, re-reading context, running routine edits. Those don't need your best model. Larik lets the main agent plan and review on a strong model while subagents do the volume work on cheap ones, across providers.

Run `/routing` to set it up. The wizard lists the models of every provider you've connected, with prices, and offers presets:

- **Balanced:** your current model stays in charge; the cheapest models it finds become `worker` and `explore`.
- **Cheapest:** the lowest-priced capable model for every subagent.
- **Local and plan first:** Ollama, LM Studio or your ChatGPT plan before paid APIs.

Then you can adjust each role, add fallbacks and set a budget. The result is saved as plain settings, for example for a student with a small Anthropic budget, a free Gemini key and Ollama:

```json
{
  "model": "anthropic/claude-sonnet-5",
  "roles": {
    "worker": "ollama/qwen3-coder",
    "explore": "gemini/gemini-3.8-flash"
  },
  "fallbacks": {
    "worker": ["gemini/gemini-3.8-flash"],
    "explore": ["anthropic/claude-haiku-4-5"]
  },
  "role_options": {
    "worker": { "isolation": "worktree", "max_turns": 40 },
    "explore": { "max_turns": 30 }
  },
  "budget": { "session_usd": 2.00, "warn_at": 0.8 }
}
```

- **Roles:** `worker` runs the built-in `general-purpose` subagent, `explore` the read-only `explore` subagent. `smart` is a strong model the main agent can hand hard subproblems to, and `compact` summarizes the conversation when the context fills. An unset role uses the main model, so nothing changes until you set one. You can add roles of your own and use them in agent definitions (`model: reviewer`), in `--model` and in `/model`.
- **Per task:** once a role has a model, the `task` tool gets a `model` input listing the roles with their prices. The main agent is told to send well-specified, mechanical work to cheap roles and keep design decisions, ambiguous debugging and final review for itself. Subagent rows show the model they ran on.
- **Fallbacks:** when a model fails before answering with a rate limit, an exhausted quota, an auth problem or an outage, Larik switches to the next model in its list and says so. It never switches once output has started. A model that failed is left alone for a minute (rate limits, server errors) or ten (a missing model, a bad key, no credit, a stopped server), so later requests don't pay for the same failure. Useful with free tiers that run out mid-session. Fallbacks can be keyed by role or by `provider/model`.
- **Keeping cheap models on a leash:** small and mid-size models sometimes lose the thread. They create stray files, "clean up" by deleting things, or repeat one command forever. Three safeguards cover this, and the presets turn them on:
  - `role_options.<role>.isolation: "worktree"` runs that role's subagents in their own git worktree (see [Subagents](#subagents)). Their edits come back as a branch for the main agent to review and merge, so a bad run never touches your checkout. It applies unless the task or agent definition chooses otherwise; outside a git repository the subagent works in place, with a notice.
  - `role_options.<role>.max_turns` caps the role's subagents (default 100).
  - Every subagent is stopped when it repeats the same tool call with the same result 4 times within 8 turns. The main agent is told the subagent got stuck and to check its changes. The main agent itself isn't stopped this way, since you can see it and interrupt it.

  In the wizard, `w` toggles the worktree, `c` toggles a minimal prompt (below) and `+`/`-` change the turn cap. From the command line: `/routing worker.isolation=worktree worker.max_turns=30`.
- **A smaller prompt for cheap models:** `role_options.<role>.context: "minimal"` drops your global instruction file (`~/.config/larik/AGENTS.md`) and the skills index from that role's subagents, keeping only this project's own `AGENTS.md`/`CLAUDE.md`. The presets set it for `worker` and `explore`: it's a smaller prompt with less to misread, and it stops a small model from loading a skill that has nothing to do with the task. From the command line: `/routing worker.context=minimal` (or `=` empty for the full prompt).
- **Budget:** Larik warns once at `warn_at` (default 80%) and stops before the next request once the session, subagents included, has spent `session_usd`. Raise it with `/routing budget=5`. Local and plan-included models count as free.
- **Comparing models before you trust one:** `larik bench --models worker,anthropic/claude-haiku-4-5,ollama/qwen3-coder` runs a few small, self-checking coding tasks (fix a failing test, implement a stub, rename a symbol across files) against each model in its own throwaway directory, then reports pass/fail, cost and time — no separate judge model, `go test` is the check. Use it to see whether a role you're about to add is actually good enough, not just cheap. See `larik bench -h`.
- **Compaction:** the `compact` role is usually best left unset. With prompt caching, the main model re-reads the conversation at the cache price (for Opus, $0.50 per million tokens), which can cost less than a cheap model reading it all uncached.
- **Quick edits:** `/routing worker=groq/llama-4-scout`, `/routing explore=` (back to the main model), `/routing budget=` (no cap).

## Keys and commands

The input row fills the terminal width, stays at the bottom, and grows up to ten lines as you type. The conversation scrolls above it with Page Up/Down, the mouse wheel, or Ctrl+Home/End on an empty input. Larik uses the terminal's alternate screen and captures the mouse, so to select text hold Shift while dragging (Option in iTerm2 and Terminal.app). On exit it prints the session ID to resume with `larik --resume <id>`. In-session pickers and settings panels open just above the input and leave recent messages visible; use Ctrl+Page Up/Down or the mouse wheel over a panel to scroll panel content when it does not fit. Edit and file-write previews use syntax highlighting when the file type is recognized.

`enter` sends. `shift+enter`, `alt+enter` or `ctrl+j` adds a newline. `esc` interrupts the current turn. `shift+tab` cycles and saves the permission mode. `alt+p` opens the model picker. `ctrl+o` switches thinking between a one-line summary ("Thought for 14s") and the full text. `?` on an empty prompt shows every shortcut. Typing `/` opens the command palette: keep typing to filter, `↑/↓` to choose, `tab` to complete, `enter` to run, `esc` to close. `ctrl+c` clears the input, interrupts, or (pressed twice) quits.

### Composer

- **`@` mentions.** Typing `@` opens a file picker over the project (it follows `.gitignore` in a git repository). `↑/↓` chooses, `tab` or `enter` inserts, and picking a folder lets you go into it. When you send the prompt, each `@path` is attached. A file is attached with line numbers and counts as read, so the model can edit it straight away. `@path#L10-40` attaches only those lines, `@folder/` attaches a listing, and `@image.png` (also `.jpg`, `.gif`, `.webp`, up to 5 MB) attaches the image for vision models. Quote paths with spaces: `@"my notes.md"`. A mention that isn't a real path stays as text, deny rules for `read` still apply, and `/rewind` and the session list show the prompt as you typed it. Mentions also work in `larik -p` and `larik serve` prompts.
- **Dropped files.** Pasting or dragging a file's absolute path into the terminal inserts it as an `@` mention.
- **`!` shell commands.** A prompt starting with `!` runs the command directly and shows its output. The input border turns yellow while you type one. The command runs in the bash sandbox when one is available and doesn't ask for permission, but `bash` deny rules still apply. Its output is sent to the model with your next prompt. `esc` stops it.
- **History.** `↑` on the first line recalls earlier prompts for this project, and `↓` goes back toward your draft. `ctrl+r` searches them. History is kept in the data directory under `history/`, up to 500 entries per project.
- **`ctrl+g`** opens the prompt in `$VISUAL` or `$EDITOR` (falling back to `vi`). The text you save becomes the prompt.

| Command | What it does |
|---|---|
| `/model [provider/model]` | Pick or switch models and reasoning effort (←/→); saves them as defaults for future launches |
| `/connect [provider]` | Setup wizard: choose a provider, connect it, pick a model, save |
| `/providers` | Connected or detected providers with status, plus a NVIDIA NIM connect shortcut; `enter` edit/connect, `t` test, `d` remove, `a` add |
| `/routing [role=provider/model]` | Setup wizard for cheaper subagent models, fallbacks and a session budget; `/routing show` lists them |
| `/keys` | Keyboard shortcuts (also `?` on an empty prompt) |
| `/config [key=value]` | Settings: theme, verbose output, spinner tips, auto-compact, token saver, notifications, response language, undo history, default mode, effort and model. Changes apply now and are saved to `~/.config/larik/config.json`; `/config token_saver=true` enables filtering |
| `/theme [auto\|dark\|light]` | Color theme; `auto` follows the terminal's background. Moving through the list previews each one |
| `/effort [low…max\|default]` | Set and save the default reasoning effort |
| `/mode [default\|accept-edits\|plan\|yolo]` | Pick and save the default permission mode from a list (`1`–`4`), or set it directly |
| `/undo` | Revert checkpointed file changes from the last turn; `bash` changes need explicit `checkpoint_paths`, and MCP side effects are not covered |
| `/compact` | Summarize the conversation to free context |
| `/clear` | Fresh context; also reloads `AGENTS.md`/`CLAUDE.md`, skills and newly approved MCP servers |
| `/cost` | Usage and cost, split by model when more than one was used |
| `/sessions` | Choose a session from a searchable, scrolling list (`✓ current`, `⑂` branch) |
| `/resume [id]` | Open the session picker, or switch by ID (a unique prefix is enough) |
| `/new` | Start a new session |
| `/fork` | Branch the conversation into a new session and continue there |
| `/rewind [n]` | List prompts, or branch off just before prompt `n` with it back in the input to edit |
| `/mcp` | MCP server status and tools |
| `/mcp approve <name>` | Allow a project-defined MCP server to start |
| `/hooks` | List configured hooks |
| `/hooks approve` | Allow the project's shared hooks to run |
| `/skills` | List skills |
| `/agents` | List subagents |
| `/lsp` | Language servers and status |
| `/tasks` / `/tasks stop <id>` | Background subagent tasks |
| `/worktrees` / `/worktrees remove <branch\|all>` | Git worktrees kept by isolated subagents |
| `/sandbox` | Sandbox status |
| `/<skill-name> [args]` | Run a skill |

CLI flags such as `--model`, `--effort`, and `--mode` override saved defaults for that launch without changing the config file.

### Branches

A branch is a new session file that starts with a copy of another session's messages and records which session it came from (`fork_of`). The original is never modified, so you can go back to it with `/resume`. Branches can only start before a prompt or at the end, never in the middle of a tool call. Each branch reports only its own token spend. `/rewind` changes only the conversation; use `/undo` to revert files.

From the command line, `larik -c --fork` or `larik --resume <id> --fork` continues in a new branch instead of appending to the old session.

## Permissions

| Mode | Behavior |
|---|---|
| `default` | Read-only tools and sandboxed `bash` commands run freely. Edits, and commands run outside the sandbox, ask first (except a few side-effect-free commands like `git status` and `ls`). |
| `accept-edits` | Edits inside the working directory run without asking. Commands still ask. |
| `plan` | Only read-only tools run. |
| `yolo` | Tools run without prompts. Deny rules and the file-tool project boundary still apply. |

Rules are written as `tool` or `tool(pattern)`. Bash patterns match the command, with `*` as a wildcard. File-tool patterns are globs on the path. Deny rules always win.

Answering "always allow" writes the rule to private project settings under `~/.config/larik/projects/` (or `$XDG_CONFIG_HOME/larik/projects/`). If saving fails, Larik reports the error and the rule applies only for the current session.

`write` and `edit` operate only inside the working directory. They reject symlink paths and protected project files (`.git/hooks`, `.git/config`, `.larik`, `.claude`, and `.mcp.json`) in every mode. If a checkpoint cannot be saved, the write stops.

## Web

**`web_fetch`** downloads a page and returns its main content as Markdown:
- Scripts, navigation, headers and footers, and decorative images are stripped, and links are resolved against the page URL.
- Text and JSON responses are returned as-is.
- Long pages come back in chunks (`start` / `max_length`).
- It asks per domain. "Always allow" saves a rule such as `web_fetch(domain:go.dev)`, which also covers subdomains.
- A redirect to a different host is reported rather than followed, so it can't sidestep a domain rule.
- Link-local and cloud-metadata addresses (`169.254.169.254` and similar) are blocked.
- Responses are capped at 5 MB and 30 s, and cached for 15 minutes.

**`web_search`** appears when a search backend is configured. Larik detects one from the environment:

| Backend | Setting |
|---|---|
| [Brave Search](https://brave.com/search/api/) | `BRAVE_API_KEY` |
| [Tavily](https://tavily.com) | `TAVILY_API_KEY` |
| [SearXNG](https://docs.searxng.org) (self-hosted; enable the JSON format) | `SEARXNG_URL` |

You can also set it explicitly:

```json
{ "web": { "search": { "provider": "brave", "api_key_env": "MY_BRAVE_KEY" } } }
```

**Trust and permissions:**
- Search settings are honored only from personal files, since a shared `.larik/settings.json` could otherwise send your queries to its own server. Shared files can only disable web tools (`"web": {"fetch_disabled": true}` or `{"search": {"disabled": true}}`).
- Both tools ask before running (`web_search` can be always-allowed).
- Plan mode asks for them rather than blocking them, because research is part of planning.
- Web content is marked as untrusted data for the model.

## Sandbox

`bash` commands run in the operating system's sandbox: Seatbelt (`sandbox-exec`) on macOS, and [bubblewrap](https://github.com/containers/bubblewrap) (`bwrap`) on Linux when installed.

On Windows, install Bash (for example, Git Bash) to use the `bash` tool. Larik has no Windows sandbox, so shell commands ask for approval.

**Inside the sandbox, a command:**
- **Can** read everything.
- **Can** write only to the project (the git root), its own private temp directory, and common build caches (Go, npm, Cargo, `~/.cache`, on macOS also `~/Library/Caches` and the per-user temp root). The literal `/tmp`, shared by every program on the machine, is never writable.
- **Can't** write to `.git/hooks`, `.git/config`, `.larik/`, `.claude/` or `.mcp.json`, even inside the project, because changing them would let a later command or hook escape the sandbox.
- **Has no network access** except localhost, so tests that start local servers still work.
- **Can't** reach other apps on macOS: LaunchServices and Apple Events are blocked, so `open` and `osascript` can't be used to escape.

**How it changes permissions:**
- In `default` and `accept-edits` mode, sandboxed commands run **without asking**.
- If a command needs more (installing packages, network access, writing elsewhere), the model re-runs it with `"sandbox": false`. That asks you first, and the prompt says it runs outside the sandbox.
- Deny rules still apply, and plan mode still blocks `bash`.
- Without a sandbox (for example on Linux without `bwrap`), every command asks as before, and Larik says so at startup.

```jsonc
{ "sandbox": { "network": true, "writable": ["~/datasets"] } }   // personal config only
{ "sandbox": { "enabled": false } }                               // turn it off
```

Loosening the sandbox (enabling network, adding writable paths, disabling it) is honored only from personal files: `~/.config/larik/config.json` and private project settings under `~/.config/larik/projects/`. A shared `.larik/settings.json` can only switch the sandbox on. `/sandbox` shows the current settings.

## Server mode

`larik serve` exposes the current directory's agent over a local HTTP API. Each session streams its events over Server-Sent Events (SSE), so editors, web UIs and scripts can drive Larik. One server can run several sessions at once. They share MCP connections, language servers and the sandbox.

```bash
./larik serve                          # 127.0.0.1:4096
./larik serve --addr 127.0.0.1:0       # pick a free port
./larik serve --model ollama/qwen3-coder --mode accept-edits   # defaults for new sessions
```

On startup the server prints one JSON line to stdout, `{"url": "...", "token": "..."}`, for programs that launch it.

**Auth and safety:**
- Every request except `GET /v1/health` needs `Authorization: Bearer <token>`.
- The token comes from `--token`, then `$LARIK_SERVER_TOKEN`, and otherwise is generated at random.
- `GET …/events` also accepts `?token=`, because browser `EventSource` can't set headers.
- The server only listens on loopback. Requests whose `Host` isn't a loopback name are rejected, which blocks DNS rebinding.
- `--allow-remote` lifts both restrictions. Anyone who can reach the port and has the token can then run commands.

```bash
T=<token>; U=http://127.0.0.1:4096
ID=$(curl -s -XPOST $U/v1/sessions -H "Authorization: Bearer $T" -d '{}' | jq -r .id)
curl -N "$U/v1/sessions/$ID/events?token=$T" &                 # live events
curl -s -XPOST $U/v1/sessions/$ID/prompt -H "Authorization: Bearer $T" \
  -d '{"text":"summarize this repo","wait":true}'              # blocks, returns the answer
```

| Endpoint | Purpose |
|---|---|
| `GET /v1/health` | Liveness check (no auth) |
| `GET /v1/info` | Version, cwd, providers, sandbox |
| `GET /v1/sessions` | Sessions in this directory, with `loaded`/`busy` flags |
| `POST /v1/sessions` | New session `{model, effort, mode}`, or load one with `{resume: id}` / `{continue: true}` |
| `GET /v1/sessions/{id}` | Model, mode, busy, usage, pending permissions, running tasks |
| `PATCH /v1/sessions/{id}` | Change `model`, `effort` or `mode` |
| `DELETE /v1/sessions/{id}` | Stop and unload (the transcript stays on disk) |
| `GET /v1/sessions/{id}/messages` | Full transcript (works for unloaded sessions too) |
| `POST /v1/sessions/{id}/fork` | Branch into a new loaded session. `{at: i}` keeps the messages before index `i` (which must be a prompt) and returns that prompt's text; with no `at`, everything is kept |
| `GET /v1/sessions/{id}/events` | SSE event stream |
| `POST /v1/sessions/{id}/prompt` | `{text}` starts a run and returns 202 (409 if busy); `{text, wait: true}` returns the final answer |
| `POST /v1/sessions/{id}/cancel` | Interrupt the current run |
| `GET /v1/sessions/{id}/permissions` | Pending permission requests |
| `POST /v1/sessions/{id}/permissions/{request_id}` | Answer: `{allow, always, reason}`. An `always` answer returns `{allowed, persisted}` and an `error` if saving failed; other answers return 204. |
| `POST /v1/sessions/{id}/compact` · `/undo` · `/clear` | Same as the TUI commands |
| `GET /v1/sessions/{id}/tasks` · `DELETE …/tasks/{task_id}` | List or stop background tasks |

**The event stream:**
- Each SSE message has `id: <seq>` and `event: <type>`. Its `data` is JSON: the agent event (the same schema as `-p --output json`) plus `seq`, `session`, and `request_id` on permission requests.
- The server adds four event types:
  - `status` (`busy` true/false)
  - `user_message`
  - `permission_resolved` (`allowed`, `denied`, or `expired` when the run was cancelled)
  - `session_closed`
- Reconnect with `Last-Event-ID` (or `?after=<seq>`) to replay missed events from a 4096-event buffer. Without it, a stream starts with new events only.
- A run's permission requests wait until some client answers them or the run is cancelled.
- When background tasks finish while a session is idle, the server starts a turn by itself to hand their results to the model.

## Configuration

Settings are read in this order. Later **personal** files override model, provider, permission mode, and allow-rule settings; shared project files can only tighten security:

1. `~/.config/larik/config.json`
2. `.larik/settings.json`
3. `~/.config/larik/projects/<project-id>.json` (private settings for this project)

Put provider definitions, model selection, roles, fallbacks, permission mode, and allow rules in `~/.config/larik/config.json` or private project settings. Larik ignores those fields in the shared `.larik/settings.json`. Shared deny rules remain active. Larik never loads `.larik/settings.local.json` from the repository.

```json
{
  "model": "anthropic/claude-opus-5",
  "effort": "high",
  "mode": "default",
  "theme": "auto",
  "verbose": false,
  "spinner_tips": true,
  "auto_compact": true,
  "notifications": "off",
  "language": "",
  "permissions": {
    "allow": ["bash(go test*)", "bash(git diff*)", "edit(docs/**)"],
    "deny": ["bash(rm -rf*)", "read(.env)"]
  },
  "providers": {
    "work-gateway": { "type": "openai-compatible", "base_url": "https://llm.example.com/v1", "api_key_env": "WORK_LLM_KEY" }
  },
  "models": {
    "gpt-5.5": { "provider": "openai", "context_window": 400000, "max_output": 128000, "input_price": 0, "output_price": 0 }
  }
}
```

Personal settings, all editable from `/config` (which changes only the key you edit):

- `theme`: `auto` (follow the terminal's background, the default), `dark` or `light`.
- `verbose`: show tool output (up to 40 lines) and thinking in full. `ctrl+o` still toggles thinking.
- `spinner_tips`: a one-line tip under the spinner during a turn.
- `auto_compact`: summarize the conversation when the context is 80% full. `/compact` works either way.
- `token_saver`: opt-in command-output filtering (`/config token_saver=true`). Recognized Git, search/listing, Go, Cargo, Node package-manager, and pytest output is shortened before entering model context. Unrecognized output and failures pass through. A `bash` call can set `raw_output: true`; `raw_output` retrieves the exact captured stdout/stderr by tool-call ID without rerunning the command. Raw output stays in private session files. The displayed byte savings are estimates for command output; use `/cost` to inspect actual session token usage and cost.
- `notifications`: `off`, `bell`, or `desktop` (OSC 9: iTerm2, Ghostty, kitty, WezTerm; other terminals get the bell). Sent only while the terminal is unfocused, when larik asks for permission or a turn of 10s or more ends. Notification hooks are separate.
- `language`: what the model replies in, e.g. `"Indonesian"`. A change applies after `/clear` or in a new session, so the cached prompt stays valid.
- `roles`, `fallbacks`, `budget` ("Model routing" and "Session budget" in `/config`): see [Mixing cheap and strong models](#mixing-cheap-and-strong-models).
- `checkpoint_retention_days` ("Undo history" in `/config`): how long `/undo` snapshots are kept, so `/undo` still works after resuming a session. Default 7; a negative value (`forever` in `/config`) keeps them forever. Old snapshots are deleted at startup, for every project, so this is honored only from personal settings.

`models` entries override the built-in catalog. The catalog drives context percentage, compaction, and cost.

## MCP servers

Larik connects to [Model Context Protocol](https://modelcontextprotocol.io) servers over stdio, streamable HTTP, or SSE. It reads servers from `mcp_servers` in any Larik settings file, and from a project `.mcp.json` in the common format, so configs shared with other tools work unchanged:

```json
{
  "mcpServers": {
    "github": { "type": "http", "url": "https://api.githubcopilot.com/mcp/", "headers": { "Authorization": "Bearer ${GITHUB_TOKEN}" } },
    "fs":     { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "${HOME}/notes"] }
  }
}
```

- Server tools appear as `mcp__<server>__<tool>`. In rules, `mcp__github` covers every tool on that server.
- `${VAR}` and `${VAR:-default}` are expanded in the command, args, env, URL, and headers.
- **Trust:** servers from shared project files (`.mcp.json`, `.larik/settings.json`) don't start until you run `/mcp approve <name>`. The approval is stored in private project settings and pinned to a hash of the server's config, so editing the command or URL requires re-approval. Servers in personal settings start automatically.
- **Permissions:** MCP tools ask before running. The exception is tools that declare both `readOnlyHint: true` and `openWorldHint: false`.
- **Stable tool set:** servers start in the background at launch, and their tools are loaded before the first request. The tool set then stays fixed for that context so prompt caches stay valid. Newly approved or restarted servers join after `/clear` or in a new session.
- Stdio server stderr is written to `~/.local/share/larik/logs/mcp-<name>.log`.
- Text results are passed to the model. Images and binary resources are summarized, not sent.
- OAuth is not supported yet. For token-based remote servers, use `headers`.

## Skills

Larik supports [Agent Skills](https://agentskills.io): folders containing a `SKILL.md` with YAML frontmatter, plus optional scripts and resources.

```markdown
---
name: release-notes
description: Write release notes from git history. Use when asked for a changelog or release notes.
---
Collect commits since the last tag with `git log $(git describe --tags --abbrev=0)..HEAD` ...
```

- **Discovery**, lowest to highest precedence:
  1. `~/.agents/skills`, `~/.claude/skills`, `~/.config/larik/skills`
  2. `.agents/skills`, `.claude/skills`, `.larik/skills` in each directory from the repo root down to the working directory

  Existing Claude Code skills work as-is, symlinked skill folders are followed, and a higher-precedence skill with the same name overrides the lower one (`/skills` shows what was shadowed).
- **Progressive disclosure:** only each skill's `name: description` is in the system prompt. The model loads the full instructions with the read-only `skill` tool when a task matches, and reads bundled files with the normal tools. The index is rebuilt at each fresh context (a new session or `/clear`) and stays fixed within it, so prompt caches stay valid. Add or edit a skill, then `/clear` to use it.
- **Running a skill yourself:** type `/<skill-name> [args]`, in the TUI or with `-p`. `$ARGUMENTS` in the body is replaced with the args. Otherwise the args are appended.
- **Frontmatter flags:** `disable-model-invocation: true` keeps a skill out of the model's index, so it only runs when you invoke it. `user-invocable: false` hides it from `/`.
- Skills are instructions, like `AGENTS.md`. Anything a skill asks the model to run still goes through the normal permission checks.

## Subagents

The model can delegate work to subagents with the `task` tool. Each subagent gets a fresh context window, its own system prompt and a restricted tool set. Only its final message comes back, so broad searches and self-contained changes don't fill the main context. Several `task` calls in one turn run in parallel.

Built-in agents:
- **`general-purpose`:** all tools. Runs on the `worker` role.
- **`explore`:** read-only search with `read`, `grep` and `glob`. Runs on the `explore` role.

Both use the main model until those roles are set (see [Mixing cheap and strong models](#mixing-cheap-and-strong-models)).

To add your own, write `<name>.md` in `.larik/agents/` or `.claude/agents/`, or in `~/.config/larik/agents/` or `~/.claude/agents/`. The format is the same as Claude Code's, and a definition overrides a built-in with the same name:

```markdown
---
name: reviewer
description: Reviews a diff for bugs and missing tests. Use after making changes.
tools: Read, Grep, Glob, Bash      # optional; omit for all tools. mcp__<server> allows a whole server
model: worker                      # optional: inherit (default), a role (worker, explore, smart, opus/sonnet/haiku, your own), or provider/model
isolation: worktree                # optional: always run in its own git worktree
---
You are a meticulous code reviewer. ...
```

**What subagents share with the main agent:**
- **Permissions:** the same rules and mode. Plan mode keeps subagents read-only too, and a subagent's permission prompts appear in your UI, labelled with the subagent and queued when several ask at once.
- **Hooks:** the same hooks, with `SubagentStop` in place of `Stop`.
- **Checkpoints:** the same store, so `/undo` reverts subagent edits along with the turn. Worktree subagents are the exception (see below).

**Other behavior:**
- Subagent tool calls are shown nested under their task. Their cost is included in the session totals, and each subagent's transcript is saved next to the session file.
- Subagents can't start further subagents.

**Worktree isolation:** inside a git repository, `task` accepts `isolation: "worktree"`, or an agent definition can set it. The subagent then works in its own git worktree on a new branch `larik/task-xxxxxx`, so parallel agents never overwrite each other's edits or yours.
- **Where it runs:** the worktree is created under `~/.local/share/larik/worktrees/` from the current `HEAD` commit. Uncommitted changes in your checkout are not included. The subagent's working directory, path permissions and relative paths all point into the worktree.
- **Confinement:** file writes outside the worktree ask for permission, as for any path outside the working directory. When the sandbox is on, `bash` may write only to the worktree and to the repository's `.git`, so commits work. `.git/hooks` and `.git/config` stay read-only.
- **Finishing:** when the subagent ends, leftover changes are committed on its branch.
  - If nothing changed, the worktree and branch are deleted.
  - Otherwise both are kept, and the result tells the main agent the branch name, the changed files, and how to review (`git diff base...branch`), merge and clean up. Nothing reaches your working tree until someone merges.
- **Not shared:** worktree subagents skip `/undo` checkpoints (the branch is the undo) and don't use the `lsp` tool, because the language servers index your checkout.
- **Cleanup:** `/worktrees` lists kept worktrees and how many commits each has that aren't in `HEAD`. `/worktrees remove <branch|all>` deletes a worktree and its branch.

**Background subagents:**
- **Starting one:** with `run_in_background: true`, `task` returns an ID (`bg-1`) immediately and the main agent keeps working.
- **Delivery:** when the subagent finishes, its result reaches the model as a `<task-notification>`. If the agent is mid-turn, the notification rides along with its next request. If the agent is idle, Larik starts a short turn automatically so the model can act on it.
- **Tools for the model:** `task_wait` blocks on specific tasks (or all of them) and returns their results. `task_stop` cancels one.
- **Lifetime:** background tasks survive Esc on the foreground turn. `/tasks` lists them, `/tasks stop <id>` cancels one, and quitting stops them all.
- **Visibility:** the status bar shows how many are running, and their permission prompts appear even while the agent is idle.
- **Limits:** at most 8 run at once. In `-p` mode Larik exits only after every background task has finished and been delivered.
- The `task` call itself never asks for permission; each tool call inside the subagent is checked on its own.
- `/agents` lists the available agents.

## Language servers (LSP)

Larik runs language servers so the model gets compiler feedback on its own edits and can navigate code semantically.

- **Diagnostics after edits:** when `edit` or `write` changes a file, Larik syncs it to the file's language server and appends new errors and warnings to the tool result, for example `ERROR 5:19 undefined: gret (compiler)`. Errors the change caused in other files are summarized. Reading a file warms up the server in the background.
- **The `lsp` tool** (read-only) offers `definition`, `references`, `hover`, `symbols`, `workspace_symbols` and `diagnostics`, using the 1-based line and column that `read` shows.
- **Built-in servers** are enabled automatically when their binary is on your `PATH`: gopls, typescript-language-server, pyright-langserver, rust-analyzer and clangd. Each starts on the first matching file, once per project root (found from markers like `go.mod`, `package.json` or `Cargo.toml`). Servers shut down when Larik exits, and their logs go to `~/.local/share/larik/logs/lsp-<name>.log`.
- **Configuration:**

  ```json
  {
    "lsp": {
      "gopls": { "initialization_options": { "staticcheck": true } },
      "clangd": { "disabled": true },
      "zls": { "command": ["zls"], "extensions": [".zig"], "root_markers": ["build.zig"] }
    }
  }
  ```

  New server commands are honored only from personal files (`~/.config/larik/config.json`, private project settings). A shared `.larik/settings.json` can only disable servers.
- **Timing:** edits wait for fresh diagnostics for up to 3s (15s while a server is still loading the project). Servers that stay silent on clean files get a shorter wait.
- `/lsp` shows servers and their status.

## Hooks

Hooks are shell commands that run at points in the agent lifecycle. The format matches Claude Code's, so existing hook scripts work. Matchers are case-insensitive, so `Bash` matches Larik's `bash`.

```json
{
  "hooks": {
    "PreToolUse":  [{ "matcher": "bash", "hooks": [{ "type": "command", "command": "./scripts/guard.sh", "timeout": 10 }] }],
    "PostToolUse": [{ "matcher": "write|edit", "hooks": [{ "type": "command", "command": "gofmt -l . >&2 && exit 2 || true" }] }],
    "Stop":        [{ "hooks": [{ "type": "command", "command": "./scripts/require-tests.sh" }] }]
  }
}
```

| Event | Fires | Can do |
|---|---|---|
| `SessionStart` | first turn of each fresh context (matcher: `startup`, `resume`, `clear`) | Add context. Stdout goes to the model. |
| `UserPromptSubmit` | before a prompt is sent | Block it, or add context (stdout) |
| `PreToolUse` | before the permission check (matcher: tool name) | `allow` (skips the prompt), `deny`, `ask`, or rewrite the input with `updatedInput` |
| `PostToolUse` | after a tool runs | Send feedback to the model |
| `Stop` / `SubagentStop` | when the agent (or a subagent) would end its turn | `decision: "block"` makes it continue with your `reason` (at most 5 times; `stop_hook_active` is set) |
| `PreCompact`, `Notification`, `SessionEnd` | compaction, permission prompts, exit | Observe only |

**How a hook's result is read:**
- **Stdin** is a JSON payload with `session_id`, `transcript_path`, `cwd`, `hook_event_name`, `tool_name`, `tool_input`, `tool_response`, and so on.
- **Exit 0:** success. Stdout may be JSON: `decision`/`reason`, `continue: false` with `stopReason` (ends the turn), `systemMessage`, and `hookSpecificOutput` (`permissionDecision`, `updatedInput`, `additionalContext`).
- **Exit 2:** blocks. Stderr is the reason, and it goes to the model.
- **Other exit codes:** errors that don't block. They're shown to you.

**Environment and timeouts:**
- `LARIK_PROJECT_DIR` is set, and so is `CLAUDE_PROJECT_DIR` for compatibility.
- The default timeout is 60s.
- Matching hooks run in parallel.

**Precedence:** a hook `deny` beats everything. Permission deny rules beat a hook `allow`.

**Trust:** hooks in the shared `.larik/settings.json` don't run until you run `/hooks approve`. The approval is pinned to the hook set's content. Hooks in personal settings always run.

Instructions are loaded from these files:
- `AGENTS.md` (or `CLAUDE.md`) in each directory from the repo root down to the working directory.
- `~/.config/larik/AGENTS.md`.

They are read at the start of each fresh context, so after editing one, `/clear` picks up the change.

## Architecture

For a deep dive with diagrams (the agent loop, permissions, providers, persistence, security, and how Larik compares with other harnesses), see [docs/](docs/README.md).

```
cmd/larik           flags, `serve` and `bench` subcommands
internal/app        shared setup: config, tools, MCP, LSP, sandbox; opens sessions
internal/llm        provider-neutral types, catalog, retry
  anthropic/        Messages API: adaptive thinking, effort, prompt caching, eager tool streaming
  openai/           Responses API: stateless, encrypted reasoning replay
  gemini/           go-genai: thought signatures, function calls
  openaicompat/     Chat Completions + presets
internal/providers  "provider/model" resolution
internal/agent      loop, events, compaction, system prompt
internal/tools      read, write, edit, bash, grep, glob
internal/mcp        MCP client: server lifecycle, approval, tool adapter
internal/hooks      lifecycle hook runner (Claude Code-compatible format)
internal/skills     Agent Skills discovery, index, skill tool, /name expansion
internal/subagent   subagent definitions and the task tool
internal/worktree   git worktrees for isolated subagents
internal/lsp        language server client, edit diagnostics, lsp tool
internal/sandbox    Seatbelt / bubblewrap confinement for bash
internal/web        web_fetch (HTML to Markdown) and web_search backends
internal/permission rules and modes
internal/session    append-only JSONL transcripts
internal/checkpoint file snapshots for /undo
internal/headless   -p mode
internal/server     HTTP + SSE API (larik serve)
internal/bench      self-checking tasks for comparing models (larik bench)
internal/tui        Bubble Tea UI
```

`agent.Run` returns a channel of events. The TUI, headless mode and the HTTP server all consume it, so none of them needs changes to the loop.

Transcripts are append-only. Thinking and reasoning blocks are replayed only to the model that produced them. Compaction replaces the whole history with a single summary that names the transcript file, so the model can grep it for details the summary left out. Together these keep provider prompt caches warm and satisfy Anthropic's thinking-block binding rules.

Data (sessions and checkpoints) lives under `~/.local/share/larik`.

## Development

```bash
go test ./...
```

The provider adapters are tested against local SSE servers (`internal/llm/llmtest`), so no API keys are needed.

The website in `website/` is generated from this README and `docs/`. `cd website && go run . -serve :8080` builds and serves it, rebuilding on every page load; `go run .` writes the static site to `website/dist/`. The build fails on any broken link.

To prepare the next GitHub release locally, see the [release steps](docs/contributing-guide.md#prepare-a-github-release). The packaging script checks tests and website links, builds cross-platform archives, and writes SHA-256 checksums; tagging, pushing, and publishing remain manual.

**Not yet supported:** MCP OAuth, MCP resources and prompts, prompt-type hooks, LSP pull diagnostics and code actions.
