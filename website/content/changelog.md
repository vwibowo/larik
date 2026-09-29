# Changelog

## Unreleased

### Added

- **Browser control.** With `"browser": {"enabled": true}` in your personal config, the model can drive a real Chrome window: open pages, read a snapshot of their text, links and fields, click, type, pick options, press keys, switch tabs, run JavaScript and read the console. Opening a page asks per domain, like `web_fetch`. Chrome keeps its own profile, so sign-ins last between sessions. See [Browser](/docs/web/#browser).
- **Footer colors.** The permission modes each have their own color (plan blue, accept edits violet, yolo red), effort is a magenta ramp with a bar that grows from `low` to `max`, and the footer now always shows whether the sandbox is on. Cost shows the session budget (`$0.68/$2.00`) and turns amber, then red, as it nears the cap; the context percentage matches its bar; subagents waiting for permission turn amber. See [Footer colors](/docs/commands/#footer-colors).
- **A teal theme with a transparent background.** The terminal UI now uses the site's teal accent in both `dark` and `light`, and no longer paints backgrounds of its own (footer chips, the task card, the composer's cursor line, Markdown headings and code): your terminal's background shows through. Diff lines keep their faint green and red tint.
- **`@` mentions.** Typing `@` opens a file picker; each `@path` in a prompt attaches the file (with line numbers, so the model can edit it straight away), `@path#L10-40` a range, `@folder/` a listing, and `@image.png` an image for vision models. Pasted or dropped file paths become mentions, and mentions work in `larik -p` and `larik serve` too. See [Composer](/docs/commands/#composer).
- **`!` shell commands** run straight from the prompt, in the sandbox, with their output sent along with your next prompt.
- **Prompt history** per project: `↑/↓` to recall, `ctrl+r` to search. `ctrl+g` edits the prompt in `$EDITOR`.
- **A session picker:** `/sessions` and `/resume` without an ID open a searchable list.
- **NVIDIA NIM** as a provider preset: `/connect nvidia-nim`, or set `NVIDIA_API_KEY`. See [Getting started](/docs/getting-started/).
- **An install script,** `scripts/install.sh`, that builds and installs `larik`.
- **Task list.** The model keeps a checklist with `todo_write` for work with several steps. The open items are pinned under the conversation during a turn, and `/todos` shows the list. See [Commands and keys](/docs/commands/).
- **Leaving plan mode.** In plan mode the model presents its plan with `exit_plan_mode`; approving it switches to the mode you pick and continues. See [Permissions](/docs/permissions/).
- **Custom commands** from `.claude/commands/*.md` and `~/.claude/commands/`, with `$ARGUMENTS`, `$1`… and `` !`cmd` ``. See [Skills](/docs/skills/#custom-commands).
- **`multi_edit`** makes several replacements in one file in a single call, all or none.
- **Image paste.** `ctrl+v` attaches an image from the system clipboard, such as a screenshot.
- **`/init`** writes an `AGENTS.md` for the project; **`/export`** saves the session as Markdown; **`/copy`** copies the last reply.
- **MCP resources, prompts and sign-in.** Servers' resources can be attached with `@server:uri` and read by the model, prompts run as `/mcp__server__prompt` commands, and `http` and `sse` servers that need OAuth sign in with `/mcp login`. See [MCP](/docs/mcp/).
- **Prompt hooks** (`"type": "prompt"`) ask a model to decide instead of running a command. See [Hooks](/docs/hooks/).
- **LSP pull diagnostics and code actions.** Servers that offer `textDocument/diagnostic` are asked for diagnostics after an edit, and the model can list and apply code actions, such as adding a missing import. See [LSP](/docs/lsp/).
- **A custom status line** from a command of your own, compatible with Claude Code's `statusLine` scripts. See [Status line](/docs/commands/#status-line).
- **Rebindable keys** with the `keybindings` setting. See [Rebinding keys](/docs/commands/#rebinding-keys).
- **Vim mode** for the prompt: `/vim`, or **Editor mode** in `/config`. See [Vim mode](/docs/commands/#vim-mode).
- **Mouse scrolling** can be switched off in `/config`, so text can be selected without a modifier.
- **Debug mode and trace viewer.** `--debug`, `LARIK_DEBUG=1` or `/debug on` records every request as sent, each response with its timing, tokens and cost, the raw HTTP exchange, tool calls, permission answers and hooks. `larik trace` or `/trace` opens it in the browser: a timeline of each agent's requests and tools, a filterable list, and an inspector that diffs each prompt against the one before. `--html` exports one page. See [Debug mode and traces](/docs/debug-mode-and-traces/).

### Changed

- The conversation scrolls above a fixed input row (Page Up/Down or the mouse wheel), and pickers and settings open just above the input with recent messages still visible.
- Edit and file-write previews use syntax highlighting, and diffs show their changed lines as one highlighted block.
- `F2` and `/info` toggle a session sidebar that stays open while you type and while a turn runs. It is split into sections and shows only what is active: the task list, connected or failing MCP servers, running language servers and the skills used this session. See [Keys and commands](/docs/commands/).
- `/model`, `/effort`, `/mode` and `shift+tab` save your choice as the default for future launches. `--model`, `--effort` and `--mode` still override it for one launch.

### Fixed

- Changing a provider's credentials takes effect straight away, without switching models.
- Interrupting a turn at the wrong moment could leave a tool call without a result, which made every later request fail. Sessions already left that way are repaired on the next prompt.
- An "always allow" rule for a command no longer matches the same command chained with others (`git status; curl … | sh`).
- Sandboxed commands can no longer redirect the project's git metadata to run code the next time `git` runs outside the sandbox.
- MCP servers using `sse` no longer disconnect right after connecting.
- `/compact` can be canceled with `esc`, and prompts typed while it runs are queued instead of racing it. `ctrl+c` stops a running `!` command.
- Pasting into a picker filter or a `/config` text field no longer lands in the hidden prompt.
- Changing routing mid-session no longer changes the tool definitions and invalidates the prompt cache.
- Tool-call IDs from OpenAI-compatible, Ollama and Gemini models no longer repeat between turns.
- ChatGPT plan sign-ins refresh reliably when several sessions share them.
- Custom providers with `type: "openai"` or `"anthropic"` keep their own names for fallbacks and reasoning replay.
- A shell command that leaves a background process running no longer hangs the turn.
- Shared project settings can no longer re-enable a language server you disabled.
- Notifications fire on terminals that don't report focus, and several smaller issues in the TUI, sessions and server were fixed.

## v0.2.0

<p class="release-meta"><time datetime="2026-09-28">September 28, 2026</time></p>

### Added

- **Cheap and strong model routing.** The main agent plans and reviews on a strong model while `worker` and `explore` subagents run on cheaper or local ones, across providers. `/routing` offers Balanced, Cheapest and Local-first presets, per-task tiers, fallbacks on rate limits and outages, and a session budget. See [Model routing](/docs/model-routing/).
- **`larik bench`** runs small self-checking coding tasks against several models and reports pass/fail, cost and time, so you can check a cheap model before trusting it with a role.
- Routing shows what it saved, and cheap subagents can use a minimal prompt (`role_options.<role>.context: "minimal"`).
- **Token saver** filters recognized successful shell output before it enters model context. `/config token_saver=true` enables it; `raw_output` retrieves the exact private session copy without rerunning a command. See [Tools and permissions](/docs/internals/tools-and-permissions/).

### Fixed

- Sandboxed `bash` can no longer write to the machine-wide `/tmp`. Each sandbox gets its own private scratch directory, exposed as `$TMPDIR`, so `mktemp` and build tools still work. See [Sandbox](/docs/sandbox/).
- Cross-platform process and file locking now allow the Windows binary to build. Windows shell commands require Bash and approval because Larik has no Windows sandbox.

## v0.1.0

<p class="release-meta"><time datetime="2026-09-27">September 27, 2026</time> · first release</p>

### Added

- **The core harness:** a Go + Bubble Tea terminal agent with Anthropic, OpenAI, Gemini and any OpenAI-compatible endpoint. See [Getting started](/docs/getting-started/).
- **ChatGPT plan sign-in** for Codex models, and native Ollama support that loads models with a usable context window and downloads models from the setup wizard.
- **Extensibility:** an [MCP](/docs/mcp/) client, Claude Code–compatible [hooks](/docs/hooks/), [Agent Skills](/docs/skills/), and [subagents](/docs/subagents/) through the `task` tool, including background subagents and git worktree isolation.
- **Language servers:** diagnostics after every edit, plus an `lsp` navigation tool. See [LSP](/docs/lsp/).
- **OS sandbox for bash:** Seatbelt on macOS, bubblewrap on Linux. See [Sandbox](/docs/sandbox/).
- **Web tools:** `web_fetch` and `web_search`. See [Web](/docs/web/).
- **Server mode:** `larik serve`, an HTTP + SSE API. See [Server mode](/docs/server-mode/).
- **Sessions:** branching with `/fork` and `/rewind`, and switching sessions inside the TUI.
- **Interface:** the provider setup wizard, model picker, command palette, mode picker, `/providers` manager, shortcuts overlay, `/config` settings screen and themes.
- **Internals docs** with diagrams, for contributors. See [Internals](/docs/internals/).

### Changed

- A redesigned banner, footer and permission prompt; thinking collapses to a one-line summary.
- Tool results show short paths.
- Thinking time is saved with the session, so resumed history shows it.
- `/clear` reloads the system prompt, undo history survives `--resume`, and compaction summaries point at the transcript file so the model can look up details.
- Undo retention is configurable in `/config`.

### Fixed

- Small local context windows are handled, with fixes from live Ollama testing.
- Inline redraw when the terminal shrinks.
- A flaky subagent test.
