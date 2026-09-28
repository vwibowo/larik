# Changelog

## Unreleased

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
