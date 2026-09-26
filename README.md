# Larik

A terminal coding agent written in Go with a [Bubble Tea](https://github.com/charmbracelet/bubbletea) UI. It works with Anthropic, OpenAI, Google Gemini, and any OpenAI-compatible endpoint (OpenRouter, Ollama, Groq, DeepSeek, xAI, Mistral, Together, LM Studio).

## Quick start

```bash
go build -o larik ./cmd/larik
```

```bash
export ANTHROPIC_API_KEY=...   # or OPENAI_API_KEY / GEMINI_API_KEY
./larik
```

With no `--model`, Larik picks the default model of the first provider whose key is set:

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
```

## Keys and commands

`enter` sends. `shift+enter`, `alt+enter` or `ctrl+j` adds a newline. `esc` interrupts the current turn. `shift+tab` cycles the permission mode. `ctrl+c` clears the input, interrupts, or (pressed twice) quits.

| Command | What it does |
|---|---|
| `/model [provider/model]` | Show or switch model mid-session |
| `/effort [low…max\|default]` | Reasoning effort |
| `/mode [default\|accept-edits\|plan\|yolo]` | Permission mode |
| `/undo` | Revert the file changes from the last turn |
| `/compact` | Summarize the conversation to free context |
| `/clear` | Fresh context |
| `/cost` | Usage and cost |
| `/sessions` | List sessions for this directory |
| `/mcp` | MCP server status and tools |
| `/mcp approve <name>` | Allow a project-defined MCP server to start |
| `/hooks` | List configured hooks |
| `/hooks approve` | Allow the project's shared hooks to run |
| `/skills` | List skills |
| `/<skill-name> [args]` | Run a skill |

## Permissions

| Mode | Behavior |
|---|---|
| `default` | Read-only tools run freely. Edits and commands ask first, except a few side-effect-free commands like `git status` and `ls`. |
| `accept-edits` | Edits inside the working directory run without asking. Commands still ask. |
| `plan` | Only read-only tools run. |
| `yolo` | Everything runs. Deny rules still apply. |

Rules are written as `tool` or `tool(pattern)`. Bash patterns match the command, with `*` as a wildcard. File-tool patterns are globs on the path. Deny rules always win.

Answering "always allow" writes the rule to `.larik/settings.local.json`.

## Configuration

Settings are merged in this order, with later files winning:

1. `~/.config/larik/config.json`
2. `.larik/settings.json`
3. `.larik/settings.local.json`

```json
{
  "model": "anthropic/claude-opus-5",
  "effort": "high",
  "mode": "default",
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
- **Trust:** servers from shared project files (`.mcp.json`, `.larik/settings.json`) don't start until you run `/mcp approve <name>`. The approval is stored in `.larik/settings.local.json` and pinned to a hash of the server's config, so editing the command or URL requires re-approval. Servers in `~/.config/larik/config.json` or `settings.local.json` start automatically.
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
- **Progressive disclosure:** only each skill's `name: description` is in the system prompt. The model loads the full instructions with the read-only `skill` tool when a task matches, and reads bundled files with the normal tools. The index is built at startup and stays fixed for the session, so prompt caches stay valid.
- **Running a skill yourself:** type `/<skill-name> [args]`, in the TUI or with `-p`. `$ARGUMENTS` in the body is replaced with the args. Otherwise the args are appended.
- **Frontmatter flags:** `disable-model-invocation: true` keeps a skill out of the model's index, so it only runs when you invoke it. `user-invocable: false` hides it from `/`.
- Skills are instructions, like `AGENTS.md`. Anything a skill asks the model to run still goes through the normal permission checks.

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
| `Stop` | when the agent would end its turn | `decision: "block"` makes it continue with your `reason` (at most 5 times; `stop_hook_active` is set) |
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

**Trust:** hooks in the shared `.larik/settings.json` don't run until you run `/hooks approve`. The approval is pinned to the hook set's content. Hooks in `~/.config/larik/config.json` and `.larik/settings.local.json` always run.

Instructions are loaded from these files:
- `AGENTS.md` (or `CLAUDE.md`) in each directory from the repo root down to the working directory.
- `~/.config/larik/AGENTS.md`.

## Architecture

```
cmd/larik           flags, session setup, wiring
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
internal/permission rules and modes
internal/session    append-only JSONL transcripts
internal/checkpoint file snapshots for /undo
internal/headless   -p mode
internal/tui        Bubble Tea UI
```

`agent.Run` returns a channel of events, and both the TUI and headless mode consume it. A future RPC or server front end can plug in without changing the loop.

Transcripts are append-only. Thinking and reasoning blocks are replayed only to the model that produced them. Compaction replaces the whole history with a single summary. Together these keep provider prompt caches warm and satisfy Anthropic's thinking-block binding rules.

Data (sessions and checkpoints) lives under `~/.local/share/larik`.

## Development

```bash
go test ./...
```

The provider adapters are tested against local SSE servers (`internal/llm/llmtest`), so no API keys are needed.

**Not yet supported:** MCP OAuth, MCP resources and prompts, prompt-type hooks, subagents, LSP diagnostics, OS-level sandboxing, and in-TUI session switching (use `--resume`).
