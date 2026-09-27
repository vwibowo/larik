# How Larik compares

Every coding-agent harness has the same core: a loop that sends a conversation to a model, runs the tools it asks for, and sends back the results. They differ in what they optimize around that loop. This page describes Larik's choices and how they compare with the approaches other well-known harnesses take.

The comparisons describe publicly documented behavior at a high level. These projects move fast; check their docs for current details.

## At a glance

| | **Larik** | Claude Code | OpenAI Codex CLI | Gemini CLI | OpenCode | Aider |
|---|---|---|---|---|---|---|
| Language / distribution | Go, one static binary | TypeScript (Node) | Rust | TypeScript (Node) | TypeScript + Go TUI | Python |
| Model providers | Anthropic, OpenAI, Gemini, Ollama, any OpenAI-compatible, ChatGPT plan | Anthropic (incl. via cloud platforms) | OpenAI-first, others configurable | Gemini-first | Many | Many |
| Tool calling | Native function calling | Native | Native | Native | Native | Mostly edit formats parsed from text |
| OS sandbox for shell | Seatbelt / bubblewrap, on by default | Available (Seatbelt / bubblewrap) | Seatbelt / Landlock, central to its design | Optional containers / Seatbelt | No | No |
| Hooks, skills, subagents, `.mcp.json` format | Claude Code–compatible | Native | Own formats | Own formats | Own formats | — |
| Subagents in git worktrees | Built in, per task | Available | — | — | — | — |
| LSP diagnostics fed back after edits | Built in | Via plugins | — | — | Built in | Lint/test commands |
| Branchable sessions | Append-only JSONL, fork and rewind | Resume, rewind | Resume | Checkpoints | Sessions, share | Git commits per change |
| Headless and API | `-p` (text / JSON events), HTTP + SSE server | `-p`, SDK | `exec` mode | Non-interactive mode | Server + clients | Scripting |

## What makes Larik different

### 1. Provider-neutral, but cache- and reasoning-correct

Many multi-provider harnesses translate every conversation into a lowest-common-denominator format, which throws away provider-specific state: Anthropic thinking signatures, OpenAI encrypted reasoning, Gemini thought signatures. Losing them makes models less capable across turns, or makes the API reject the request.

Larik keeps a neutral transcript where each block remembers its producer (`Message.Model`, `Block.Provider`), plus an opaque escape hatch (`BlockOpaque` + `Raw`). At request time `ReplayableBlocks` sends each provider only what it can accept. You can switch from Claude to GPT to a local model mid-session and back, and each model gets its own reasoning replayed. See [providers](providers.md#switching-providers-mid-conversation).

The OpenAI adapter uses the Responses API statelessly (`store: false` + encrypted reasoning), so no conversation state lives on the vendor's servers, which also makes fork and resume trivially correct.

### 2. Prompt-cache stability as a design rule

Several choices exist only to keep the prompt prefix byte-identical between requests, so every request after the first reads from the provider's cache (cheaper and faster):

- The system prompt is built once; a language change waits for `/clear`.
- The tool set (including MCP tools) is fixed per context, sorted by name; late MCP servers join after `/clear`.
- The transcript is append-only: `/undo` becomes a note on the next message rather than an edit.
- Compaction is one clean cut to a single summary, done with the same prefix so the summary request is itself cached.

Most harnesses benefit from caching; Larik treats cache invalidation as a bug. See [agent loop: why](agent-loop.md#why-the-loop-looks-like-this).

### 3. The sandbox removes prompts instead of adding them

In many harnesses, safety means asking before every command. Larik runs bash in the OS sandbox by default and **auto-allows sandboxed commands**; only a command that explicitly opts out (`"sandbox": false`) asks. The sandbox also protects `.git/hooks`, `.git/config`, `.larik/`, `.claude/` and `.mcp.json` inside the writable project, closing the "plant a hook, escape later" path. Codex CLI is the closest in spirit (sandbox-first); Larik combines that with Claude Code–style rules, modes and hooks. See [security](security.md#the-os-sandbox).

### 4. One event stream, many front ends

The loop emits a single `Event` stream and needs only permission replies back. The TUI, `-p` headless mode (text or JSON lines) and the HTTP + SSE server are all thin consumers, and share the same JSON schema. Adding an editor integration or web UI means consuming events, not forking the loop. See [architecture](architecture.md#one-event-stream-three-front-ends).

### 5. Compatible with the Claude Code ecosystem

Hooks (same events, stdin payload, exit codes, JSON output; `CLAUDE_PROJECT_DIR` set), skills (`~/.claude/skills`, `.claude/skills`), subagent definitions (`.claude/agents/*.md`), `CLAUDE.md` instructions and `.mcp.json` all work unchanged. A team can adopt Larik for non-Anthropic models without rewriting its automation. Where Larik differs, it tightens: project hooks and project MCP servers need approval pinned to a content hash.

### 6. Shared config can't loosen security

Settings from the repo (`.larik/settings.json`, `.mcp.json`) can only make things stricter: switch the sandbox on, disable LSP servers or web tools. Anything that loosens (network in the sandbox, extra writable paths, new LSP commands, the search backend, approvals) is honored only from personal files. Cloning a repo can't widen what the agent may do. See [state: config layers](state-and-persistence.md#configuration-layers).

### 7. Compiler feedback in the edit result

After every `edit` or `write`, Larik syncs the file to its language server and appends new diagnostics to the tool result. The model sees the type error in the same response that confirmed its edit, without spending a turn on a build.

### 8. Parallel, isolated subagents with a review step

Subagents can run in the foreground, in parallel, in the background (with results delivered as notifications even when idle), and in their own git worktree on a `larik/task-*` branch. Worktree changes never touch your checkout: the parent gets a branch, a file list and the diff/merge commands. See [extensibility](extensibility.md#subagents).

### 9. Local models are first-class

The Ollama adapter uses the native API to request a usable context window (Ollama's OpenAI-compatible endpoint silently loads 4096 tokens), reads back the window actually loaded, warns when a request fills it, and warns when a model can't call tools. Compaction sizes itself to the real window.

## Trade-offs

Choices that cost something:

- **No text-based edit formats.** Larik requires native tool calling. Models without it (some small local ones) can't work, where Aider's diff formats would. Larik only warns when a model can't call tools.
- **Nothing is pruned.** Session files keep everything, including history from before compaction. Checkpoint snapshots are full copies of every file edited, possibly including secrets, and only `/undo` removes them. Worktrees kept by subagents stay until `/worktrees remove`. All of it accumulates under `~/.local/share/larik`.
- **`/undo` has a narrow reach.** It only restores what `write` and `edit` changed. A `sed -i` or a generator run from bash, or a file written by an MCP tool, isn't captured. It also only reaches turns since the process started: after `larik -c` or `--resume`, earlier turns can't be undone. Git remains the safety net.
- **Fixed tool set per context.** An MCP server approved mid-session needs `/clear` to appear, in exchange for cache stability.
- **Prompt changes need a restart.** The system prompt, including `AGENTS.md`/`CLAUDE.md` and the skills index, is built once at startup. `/clear` doesn't rebuild it, so edits to instruction files or new skills take effect in a new `larik` process.
- **Compaction is lossy.** One summary replaces the whole context and nothing from before it is replayed. Details the summary leaves out are gone from the model's view, though still in the session file.
- **Unix only.** Larik builds for macOS and Linux, not Windows. On Linux without `bwrap` the sandbox is off and every command asks.
- **Not yet supported:** MCP OAuth, MCP resources and prompts, prompt-type hooks, LSP pull diagnostics and code actions.
