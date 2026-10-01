# Larik

A terminal coding agent written in Go with a [Bubble Tea](https://github.com/charmbracelet/bubbletea) UI. It works with Anthropic, OpenAI, Google Gemini, and OpenAI-compatible endpoints including NVIDIA NIM, OpenRouter, Ollama, Groq, DeepSeek, xAI, Mistral, Together, and LM Studio.

## Quick start

On macOS or Linux, download a release, verify its checksum and install `larik` into `~/.local/bin`:

```bash
curl -fsSL https://raw.githubusercontent.com/vwibowo/larik/main/scripts/get-larik.sh | sh
```

Set `LARIK_VERSION` (for example `v0.4.0`) to pin a release, or `LARIK_INSTALL_DIR` to install elsewhere. On Windows, download the zip from the releases page.

To build from a checkout and install `larik` into `/usr/local/bin`:

```bash
./scripts/install.sh
```

The installer uses `sudo` when needed. Set `PREFIX` or `BINDIR` to install elsewhere, for example:

```bash
PREFIX="$HOME/.local" ./scripts/install.sh
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

| Key                 | Default model             |
| ------------------- | ------------------------- |
| `ANTHROPIC_API_KEY` | `anthropic/claude-opus-5` |
| `OPENAI_API_KEY`    | `openai/gpt-5.5`          |
| `GEMINI_API_KEY`    | `gemini/gemini-3.8-flash` |

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

To use a Claude subscription through the official CLI, [install Claude Code](https://code.claude.com/docs/en/getting-started) and sign in with `claude auth login`, then run `/connect claude-code-cli`. Larik offers Claude Code's rolling `sonnet`, `opus`, and `fable` aliases (which follow each family's latest model), plus effort levels supported by its CLI; you can also enter a full model ID such as `claude-code-cli/sonnet`. Claude Code does not expose a supported non-interactive model-list command, so it remains the authority on whether a model is available to your account. Larik checks the CLI version, required flags, and first-party sign-in. It leaves authentication with Claude Code and removes API-key and cloud-provider routing variables from inference child processes. Context compaction is managed by Claude Code, so Larik's `/compact` command is unavailable for this provider. `anthropic/<model>` remains the API-key provider; `claude-code-cli/<model>` runs Claude Code as the agent and routes tool calls through Larik's permission and hook checks.

For NVIDIA's hosted model catalog, run `/connect nvidia-nim` and enter a key, or set `NVIDIA_API_KEY`; for example, select an available `nvidia-nim/<model-id>` after connection. [NVIDIA's developer access](https://docs.api.nvidia.com/nim/docs/product) is for prototyping, research, development, and testing. Self-hosted NIM can use a custom OpenAI-compatible provider instead. Gemini and `anthropic/<model>` use API keys.

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
  "budget": { "session_usd": 2.0, "warn_at": 0.8 }
}
```

- **Roles:** `worker` runs the built-in `general-purpose` subagent, `explore` the read-only `explore` subagent. `smart` is a strong model the main agent can hand hard subproblems to, and `compact` summarizes the conversation when the context fills. An unset role uses the main model, so nothing changes until you set one. You can add roles of your own and use them in agent definitions (`model: reviewer`), in `--model` and in `/model`.
- **Per task:** once a role has a model, the `task` tool gets a `model` input listing the roles with their prices. The main agent is told to send well-specified, mechanical work to cheap roles and keep design decisions, ambiguous debugging and final review for itself. Subagent rows show the model they ran on.
- **Fallbacks:** when a model fails before answering with a rate limit, an exhausted quota, an auth problem, an outage or no response at all (see `stall_timeout`), Larik switches to the next model in its list and says so. It never switches once output has started. A model that failed is left alone for a minute (rate limits, server errors) or ten (a missing model, a bad key, no credit, a stopped server), so later requests don't pay for the same failure. Useful with free tiers that run out mid-session. Fallbacks can be keyed by role or by `provider/model`.
- **Keeping cheap models on a leash:** small and mid-size models sometimes lose the thread. They create stray files, "clean up" by deleting things, or repeat one command forever. Three safeguards cover this, and the presets turn them on:
  - `role_options.<role>.isolation: "worktree"` runs that role's subagents in their own git worktree (see [Subagents](#subagents)). Their edits come back as a branch for the main agent to review and merge, so a bad run never touches your checkout. It applies unless the task or agent definition chooses otherwise; outside a git repository the subagent works in place, with a notice.
  - `role_options.<role>.max_turns` caps the role's subagents (default 100).
  - Every subagent is stopped when it repeats the same tool call with the same result 4 times within 8 turns. The main agent is told the subagent got stuck and to check its changes. The main agent itself isn't stopped this way, since you can see it and interrupt it.

  In the wizard, `w` toggles the worktree, `c` toggles a minimal prompt (below) and `+`/`-` change the turn cap. From the command line: `/routing worker.isolation=worktree worker.max_turns=30`.

- **A smaller prompt for cheap models:** `role_options.<role>.context: "minimal"` drops your global instruction file (`~/.config/larik/AGENTS.md`) and the skills index from that role's subagents, keeping only this project's own `AGENTS.md`/`CLAUDE.md`. The presets set it for `worker` and `explore`: it's a smaller prompt with less to misread, and it stops a small model from loading a skill that has nothing to do with the task. From the command line: `/routing worker.context=minimal` (or `=` empty for the full prompt).
- **Budget:** Larik warns once at `warn_at` (default 80%) and stops before the next request once the session, subagents included, has spent `session_usd`. Raise it with `/routing budget=5`. Local and plan-included models count as free.
- **Comparing models before you trust one:** `larik bench --models worker,anthropic/claude-haiku-4-5,ollama/qwen3-coder` runs a few small, self-checking coding tasks (fix a failing test, implement a stub, rename a symbol across files) against each model in its own throwaway directory, then reports pass/fail, cost, time, tokens and peak context — no separate judge model, `go test` (or an exact expected file) is the check. `--execution tools,hybrid,code` runs each setting side by side, `--runs 3` repeats each task and adds a table of medians and ranges, and `--keep-failed` keeps a failed run's directory with its transcript and every tool call (scripts' included) so you can see why. Use it to see whether a role you're about to add is actually good enough, not just cheap. See `larik bench -h`.
- **Compaction:** the `compact` role is usually best left unset. With prompt caching, the main model re-reads the conversation at the cache price (for Opus, $0.50 per million tokens), which can cost less than a cheap model reading it all uncached.
- **Quick edits:** `/routing worker=groq/llama-4-scout`, `/routing explore=` (back to the main model), `/routing budget=` (no cap).

## Keys and commands

The input row fills the terminal width, stays at the bottom, and grows up to ten lines as you type. The conversation scrolls above it with Page Up/Down, the mouse wheel, or Ctrl+Home/End on an empty input. Larik uses the terminal's alternate screen and captures the mouse for wheel scrolling, so to select text hold Shift while dragging (Option in iTerm2 and Terminal.app), or turn **Mouse scrolling** off in `/config` to select normally and scroll with Page Up/Down. `/copy` copies the last reply, as Markdown, to the clipboard. On exit it prints the session ID to resume with `larik --resume <id>`. In-session pickers and settings panels open just above the input and leave recent messages visible; use Ctrl+Page Up/Down or the mouse wheel over a panel to scroll panel content when it does not fit. Edit and file-write previews use syntax highlighting when the file type is recognized.

`enter` sends. `shift+enter`, `alt+enter` or `ctrl+j` adds a newline. `esc` interrupts the current turn. `F2` or `/info` toggles the session sidebar: the task list, MCP servers that are connected or need attention, language servers that are running or failed, and the skills used this session, each in its own section. It stays open while you type and while a turn runs, and becomes a popup on narrow terminals. The model, effort and permission mode stay in the footer. While a multi-step task is in progress, its checklist floats at the top right (or moves into the sidebar); the completed checklist is added to the conversation. `shift+tab` cycles and saves the permission mode. `alt+p` opens the model picker. `ctrl+o` switches thinking between a one-line summary ("Thought for 14s") and the full text. `?` on an empty prompt shows every shortcut. Typing `/` opens the command palette: keep typing to filter, `↑/↓` to choose, `tab` to complete, `enter` to run, `esc` to close. `ctrl+c` clears the input, interrupts, or (pressed twice) quits.

### Vim mode

`/vim` (or **Editor mode** in `/config`, saved as `"editor_mode": "vim"`) edits the prompt vim-style. Each prompt starts in insert mode, where typing works as usual; `esc` switches to normal mode, and the footer shows which one you're in. While a turn runs, `esc` on an empty prompt still interrupts.

Normal mode supports:

- **Moving:** `h j k l`, `w b e` and `W B E`, `0 ^ $`, `gg G` (with a count, a line number), `f F t T` with `;` and `,`. `k` on the first line and `j` on the last recall prompt history, like the arrow keys.
- **Operators:** `d`, `c` and `y` with any motion, doubled for whole lines (`dd`, `cc`, `yy`), or with a text object: `iw aw`, `iW aW`, quotes (`i" a" i' a'`) and brackets (`i( a( ib`, `i[`, `i{ iB`, `i<`).
- **Editing:** `x X D C s S r ~ J`, `p P` to put what was deleted or yanked, `u` to undo, `.` to repeat the last change, including text typed after it. Counts work: `3w`, `2dd`, `d2w`.
- **Entering insert mode:** `i a I A o O`.

`enter` sends the prompt from either mode, and `ctrl+` shortcuts keep working. Searching (`/`, `?`), visual mode, marks, macros and named registers aren't supported.

### Rebinding keys

`keybindings` in `~/.config/larik/config.json` (or private project settings) maps an action to a key or a list of keys, replacing that action's defaults. An empty list unbinds it:

```json
{
  "keybindings": {
    "external_editor": "ctrl+e",
    "toggle_thinking": ["ctrl+t", "ctrl+o"],
    "paste_image": []
  }
}
```

Actions and their defaults: `submit` (enter), `newline` (shift+enter, alt+enter, ctrl+j), `interrupt` (esc), `quit` (ctrl+d on an empty input), `history_search` (ctrl+r), `external_editor` (ctrl+g), `paste_image` (ctrl+v), `cycle_mode` (shift+tab), `toggle_thinking` (ctrl+o), `model_picker` (alt+p), `shortcuts` (?), `scroll_up` (pgup), `scroll_down` (pgdown), `scroll_top` (ctrl+home), `scroll_bottom` (ctrl+end). Keys are written as Bubble Tea names them: `ctrl+`, `alt+`, `shift+` and `super+` in front of a character or `enter`, `tab`, `esc`, `space`, `up`, `pgup`, `f5` and so on. A key you give one action is taken from whichever action had it by default. `ctrl+c` can't be rebound, and a plain character can't be bound (it would stop you typing it) except to `shortcuts`. Keys inside pickers and permission prompts stay as they are. The banner lists entries that couldn't apply, and `?` and the hints across the interface show the keys as bound. Shared `.larik/settings.json` files can't rebind keys.

### Footer colors

Each thing you should recognize at a glance has its own color in the footer:

| Item | Color |
|---|---|
| Mode `default` | plain text |
| Mode `plan` (read-only) | blue |
| Mode `accept edits` | violet |
| Mode `auto` | green |
| Mode `yolo` | red |
| Effort `low` … `max` | one magenta hue that gets brighter, with a bar (`▂ ▃ ▅ ▆ █`) that grows |
| `◈ sandbox` / `⚠ no sandbox` | green when the sandbox is on; amber when it is off (red if you are also in `yolo`) |
| Cost | plain; with a session budget it reads `$0.68/$2.00`, amber at the warning threshold and red at the cap |
| Context | the bar and the percentage turn amber at 70% and red at 90% |

Teal is kept for Larik's own chrome: the model marker, the spinner, headings and borders. A subagent that is waiting for a permission answer is shown in amber.

### Status line

`status_line` replaces the footer's model, context and cost with a command's output. The permission mode stays on the left:

```json
{ "status_line": { "type": "command", "command": "~/.config/larik/status.sh" } }
```

The command gets the session's state as JSON on stdin, in the form Claude Code gives its `statusLine` command, so the same scripts work: `session_id`, `transcript_path`, `cwd`, `model.id`, `model.display_name`, `workspace.current_dir`, `version`, `cost.total_cost_usd`, and `context_window` (`context_window_size`, `used_tokens`, `used_percentage`). Larik's own fields are under `larik`: `provider`, `effort`, `permission_mode`, `background_tasks` and `turn_running`. Claude Code's `statusLine` key is read too. Up to three lines of output are shown, with their ANSI colors. The command runs in the project directory when that state changes, at most every 300 ms, and is stopped after 5 seconds. If it fails, the default footer stays and the error is shown once. It runs a command, so it's honored only from personal settings. For example:

```sh
#!/bin/sh
in=$(cat)
branch=$(git branch --show-current 2>/dev/null)
echo "$(echo "$in" | jq -r .model.id) · ${branch:-no git} · $(echo "$in" | jq -r .context_window.used_percentage)% ctx"
```

### Composer

- **`@` mentions.** Typing `@` opens a file picker over the project (it follows `.gitignore` in a git repository). `↑/↓` chooses, `tab` or `enter` inserts, and picking a folder lets you go into it. When you send the prompt, each `@path` is attached. A file is attached with line numbers and counts as read, so the model can edit it straight away. `@path#L10-40` attaches only those lines, `@folder/` attaches a listing, and `@image.png` (also `.jpg`, `.gif`, `.webp`, up to 5 MB) attaches the image for vision models. Quote paths with spaces: `@"my notes.md"`. A mention that isn't a real path stays as text, deny rules for `read` still apply, and `/rewind` and the session list show the prompt as you typed it. Mentions also work in `larik -p` and `larik serve` prompts.
- **Dropped files.** Pasting or dragging a file's absolute path into the terminal inserts it as an `@` mention.
- **Pasted images.** A terminal can only paste text, so `ctrl+v` reads an image from the system clipboard instead, such as a screenshot. Larik saves it under the data directory (`pastes/`, readable only by you, deleted after 7 days) and inserts it as an `@` mention, so it is attached like any `@image.png`. Images over 5 MB, or longer than 2000 pixels, are scaled down and sent as JPEG. On macOS this needs nothing extra; on Linux it uses `wl-paste` (Wayland) or `xclip` (X11).
- **`!` shell commands.** A prompt starting with `!` runs the command directly and shows its output. The input border turns yellow while you type one. The command runs in the bash sandbox when one is available and doesn't ask for permission, but `bash` deny rules still apply. Its output is sent to the model with your next prompt. `esc` or `ctrl+c` stops it.
- **History.** `↑` on the first line recalls earlier prompts for this project, and `↓` goes back toward your draft. `ctrl+r` searches them. History is kept in the data directory under `history/`, up to 500 entries per project.
- **`ctrl+g`** opens the prompt in `$VISUAL` or `$EDITOR` (falling back to `vi`). The text you save becomes the prompt.

**Task list.** For work with several steps, the model keeps a checklist with the `todo_write` tool. While a turn runs, the open items are pinned under the conversation and the status line names the item in progress. Each update is also printed in the conversation. `/todos` shows the current list. It is restored when you resume a session, and it survives compaction.

| Command                                          | What it does                                                                                                                                                                                                                                                             |
| ------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `/model [provider/model]`                        | Pick or switch models and reasoning effort (←/→); saves them as defaults for future launches                                                                                                                                                                             |
| `/connect [provider]`                            | Setup wizard: choose a provider, connect it, pick a model, save                                                                                                                                                                                                          |
| `/providers`                                     | Connected or detected providers with status, plus a NVIDIA NIM connect shortcut; `enter` edit/connect, `t` test, `d` remove, `a` add                                                                                                                                     |
| `/routing [role=provider/model]`                 | Setup wizard for cheaper subagent models, fallbacks and a session budget; `/routing show` lists them                                                                                                                                                                     |
| `/keys`                                          | Keyboard shortcuts (also `?` on an empty prompt)                                                                                                                                                                                                                         |
| `/info`                                          | Session sidebar: tasks, active MCP servers, running language servers and skills used (also F2)                                                                                                                                                                            |
| `/config [key=value]`                            | Settings: theme, verbose output, spinner tips, mouse scrolling, auto-compact, token saver, execution, notifications, response language, undo history, default mode, effort and model. Changes apply now and are saved to `~/.config/larik/config.json`; `/config token_saver=true` enables filtering |
| `/theme [auto\|dark\|light]`                     | Color theme; `auto` follows the terminal's background. Moving through the list previews each one                                                                                                                                                                         |
| `/effort [low…max\|default]`                     | Set and save the default reasoning effort                                                                                                                                                                                                                                |
| `/mode [default\|accept-edits\|plan\|auto\|yolo]` | Pick and save the default permission mode from a list (`1`–`5`), or set it directly                                                                                                                                                                                     |
| `/execution [tools\|hybrid\|code]`               | How the model acts: `tools` (a tool call per step, the default), `hybrid` (also `run_code` scripts when a task has many steps) or `code` (scripts only). See [Execution](#execution)                                                                                   |
| `/undo`                                          | Revert the file changes of the last turn that made any: edits by the file tools, and in a git repository what `bash` commands changed (files git ignores aren't covered, nor are MCP side effects). See [Undo](#undo)                                                  |
| `/compact`                                       | Summarize the conversation to free context                                                                                                                                                                                                                               |
| `/clear`                                         | Fresh context; also reloads `AGENTS.md`/`CLAUDE.md`, skills and newly approved MCP servers                                                                                                                                                                               |
| `/todos`                                         | The model's task list, in full                                                                                                                                                                                                                                           |
| `/init [focus]`                                  | Study the project and write `AGENTS.md`, or improve the `AGENTS.md` or `CLAUDE.md` it has; works with `-p` too. Applies from the next fresh context                                                                                                                      |
| `/export [file]`                                 | Save the whole session as Markdown (prompts, replies, tool calls and capped results; no thinking or attached file contents). Default `larik-<session>.md` here; never overwrites                                                                                         |
| `/copy`                                          | Copy the last reply, as Markdown, to the clipboard (natively and through the terminal, so it works over SSH)                                                                                                                                                             |
| `/review [PR\|branch\|range] [focus]`             | Review code changes for bugs and report findings, without editing. With no argument: your uncommitted changes, or this branch's commits when the tree is clean. See [Reviewing changes](#reviewing-changes)                                                              |
| `/security-review [PR\|branch\|range] [focus]`    | The same, looking for security vulnerabilities the changes introduce                                                                                                                                                                                                     |
| `/vim`                                           | Switch vim editing of the prompt on or off; see [Vim mode](#vim-mode)                                                                                                                                                                                                    |
| `/debug [on\|off]`                               | Record this session for review: requests as sent, responses, raw HTTP, tools and timing; see [Debug mode and traces](#debug-mode-and-traces)                                                                                                                             |
| `/trace`                                         | Open the recorded trace in the browser: a timeline, every request as sent, and the raw HTTP exchanges                                                                                                                                                                    |
| `/cost`                                          | Usage and cost, split by model when more than one was used                                                                                                                                                                                                               |
| `/sessions`                                      | Choose a session from a searchable, scrolling list (`✓ current`, `⑂` branch)                                                                                                                                                                                             |
| `/resume [id]`                                   | Open the session picker, or switch by ID (a unique prefix is enough)                                                                                                                                                                                                     |
| `/new`                                           | Start a new session                                                                                                                                                                                                                                                      |
| `/fork`                                          | Branch the conversation into a new session and continue there                                                                                                                                                                                                            |
| `/rewind [n]`                                    | List prompts, or branch off just before prompt `n` with it back in the input to edit                                                                                                                                                                                     |
| `/mcp`                                           | MCP server status and tools                                                                                                                                                                                                                                              |
| `/mcp approve <name>`                            | Allow a project-defined MCP server to start                                                                                                                                                                                                                              |
| `/hooks`                                         | List configured hooks                                                                                                                                                                                                                                                    |
| `/hooks approve`                                 | Allow the project's shared hooks to run                                                                                                                                                                                                                                  |
| `/skills`                                        | List skills                                                                                                                                                                                                                                                              |
| `/memory [add <text> \| show <name> \| delete <name>]` | List the notes Larik remembers across sessions, or add, read or delete one. See [Memory](#memory)                                                                                                                                                                  |
| `/agents`                                        | List subagents                                                                                                                                                                                                                                                           |
| `/lsp`                                           | Language servers and status                                                                                                                                                                                                                                              |
| `/tasks` / `/tasks stop <id>`                    | Background subagent tasks                                                                                                                                                                                                                                                |
| `/worktrees` / `/worktrees remove <branch\|all>` | Git worktrees kept by isolated subagents                                                                                                                                                                                                                                 |
| `/sandbox`                                       | Sandbox status                                                                                                                                                                                                                                                           |
| `/<skill-name> [args]`                           | Run a skill                                                                                                                                                                                                                                                              |

CLI flags such as `--model`, `--effort`, and `--mode` override saved defaults for that launch without changing the config file.

### Branches

A branch is a new session file that starts with a copy of another session's messages and records which session it came from (`fork_of`). The original is never modified, so you can go back to it with `/resume`. Branches can only start before a prompt or at the end, never in the middle of a tool call. Each branch reports only its own token spend. `/rewind` changes only the conversation; use `/undo` to revert files.

From the command line, `larik -c --fork` or `larik --resume <id> --fork` continues in a new branch instead of appending to the old session.

### Undo

`/undo` puts files back as they were before the last turn that changed any, and tells the model to re-read them.

- **File tools:** every file `write`, `edit` and `multi_edit` touched.
- **Shell commands, in a git repository:** files a `bash` command modified, deleted or created, whether tracked or untracked. A file you had already changed goes back to how you had it, not to the last commit. Larik compares git's view of the working tree before and after each command; it doesn't commit, stash or stage anything.
- **Not covered:** files git ignores (build output, `node_modules`), changes a command makes by moving `HEAD` (`git checkout`, `reset`, `pull`; undo those with git), files over 2 MB, anything outside a repository, and what MCP tools change. For an ignored file the model can name it in the command's `checkpoint_paths`.
- Edits you make yourself in an editor while a command runs count as that command's, so an `/undo` right after would revert them too.
- Snapshots are kept for 7 days by default (`checkpoint_retention_days`), so `/undo` still works after resuming a session.

## Permissions

| Mode           | Behavior                                                                                                                                                                              |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `default`      | Read-only tools and sandboxed `bash` commands run freely. Edits, and commands run outside the sandbox, ask first (except a few side-effect-free commands like `git status` and `ls`). |
| `accept-edits` | Edits inside the working directory run without asking. Commands still ask.                                                                                                            |
| `plan`         | Only read-only tools run. When the plan is ready, the model presents it with `exit_plan_mode`; approving it switches to `accept-edits` or `default`.                                  |
| `auto`         | Edits inside the working directory run without asking. Anything else that would ask is first checked by a model: what it judges safe runs, and the rest asks you, with its reason.    |
| `yolo`         | Tools run without prompts. Deny rules and the file-tool project boundary still apply.                                                                                                 |

**Auto mode.** `auto` sits between `accept-edits` and `yolo`: you are asked only about the calls that matter.

- **What the check sees:** the tool call, the working directory, and your last few prompts. Tool output, file contents and the model's own text are not shown to it as instructions, so text planted in a web page or a file can't talk it into approving something.
- **What it allows:** ordinary steps of the work you asked for whose effects stay in the project: builds, tests, the project's package manager, local git, reading documentation.
- **What it sends to you:** deleting or rewriting things that are hard to get back (`git reset --hard`, deleting source or data), anything others see (`git push`, publishing, deploying, opening issues or pull requests), changes outside the project, handling secrets, downloading and running code, `sudo`, and anything it can't assess. If your prompt explicitly asks for one of these ("commit and push"), that counts as your approval of that action.
- **When it asks,** the prompt shows its reason. If the check fails or times out, you are asked. A call it approved is not checked again in the same session.
- **What stays the same:** deny rules, allow rules, the sandbox (sandboxed commands still run without any check) and the project boundary for file tools. A plan is always yours to approve. In `larik -p`, a call the check doesn't approve is denied, as any prompt is.
- **Which model:** the session's model, at low reasoning effort; each check adds a second or two and a small cost. To use another, set `"auto_mode": {"model": "anthropic/claude-haiku-4-5"}` (a `provider/model` or a routing role). Only personal config files can set this, or select `auto` as the mode: a repository can't choose what approves its own commands.

The check is a model's judgment, not a guarantee. It reduces how often you're asked; it doesn't replace the sandbox or deny rules for things that must never happen.

**Leaving plan mode.** In plan mode the model is told it is planning, and when the plan is ready it calls `exit_plan_mode`. Larik prints the plan in the conversation and asks: **Yes, and accept edits**, **Yes, but ask before each edit**, or **No, keep planning**, which lets you say what to change. Approving switches the mode for this session only; your saved default stays as it is. In `larik -p` plan mode can't end, so the model gives the plan as its answer.

Rules are written as `tool` or `tool(pattern)`. Bash patterns match the command, with `*` as a wildcard. File-tool patterns are globs on the path. Deny rules always win.

Answering "always allow" writes the rule to private project settings under `~/.config/larik/projects/` (or `$XDG_CONFIG_HOME/larik/projects/`). If saving fails, Larik reports the error and the rule applies only for the current session.

`write`, `edit` and `multi_edit` operate only inside the working directory. They reject symlink paths and protected project files (`.git` itself and the parts of it that decide what code git runs, such as `hooks`, `config` and `commondir`, plus `.larik`, `.claude`, and `.mcp.json`) in every mode. If a checkpoint cannot be saved, the write stops.

## Execution

The permission mode decides what Larik may do without asking. The execution setting decides *how* the model does it, and the two combine freely (plan mode with scripts is a good pair for exploring).

| Setting  | What the model gets                                                                                                                       |
| -------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| `tools`  | The tools, one call per step. The default.                                                                                                |
| `hybrid` | The tools plus `run_code`, and it picks per step: scripts for loops, chained lookups and filtering large output, direct calls otherwise.  |
| `code`   | `run_code` only; every tool is reached from scripts.                                                                                      |

`run_code` runs JavaScript in an embedded interpreter with no file, network or process access of its own: a script can only call tools (`tools.read({path})`, `tools.call(name, args)`), and gets their text back. Only what the script prints returns to the model, so reading forty files to count something costs the context one summary instead of forty results.

Every call a script makes goes through the same checks as a direct one: permission rules and mode, hooks, auto mode, checkpoints for `/undo`. A script in plan mode can read but not write; in `default` mode you are asked about an edit in the middle of a script. Each script is limited to 200 tool calls and a timeout (120 seconds by default, up to 600). Subagents follow the session's setting.

Set it with `/execution hybrid` or `"execution": "hybrid"` in config. Compare the settings on your own models with `larik bench --execution tools,hybrid,code --runs 3`.

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

| Backend                                                                   | Setting          |
| ------------------------------------------------------------------------- | ---------------- |
| [Brave Search](https://brave.com/search/api/)                             | `BRAVE_API_KEY`  |
| [Tavily](https://tavily.com)                                              | `TAVILY_API_KEY` |
| [SearXNG](https://docs.searxng.org) (self-hosted; enable the JSON format) | `SEARXNG_URL`    |

You can also set it explicitly:

```json
{ "web": { "search": { "provider": "brave", "api_key_env": "MY_BRAVE_KEY" } } }
```

**Trust and permissions:**

- Search settings are honored only from personal files, since a shared `.larik/settings.json` could otherwise send your queries to its own server. Shared files can only disable web tools (`"web": {"fetch_disabled": true}` or `{"search": {"disabled": true}}`).
- Both tools ask before running (`web_search` can be always-allowed).
- Plan mode asks for them rather than blocking them, because research is part of planning.
- Web content is marked as untrusted data for the model.

### Browser

For pages that need JavaScript, a sign-in or clicking through, Larik can drive a real Chrome window. It's off by default; turn it on in your personal config:

```json
{ "browser": { "enabled": true } }
```

Chrome (or Chromium) must be installed; set `"chrome_path"` if Larik can't find it, and `"headless": true` to hide the window. Chrome starts on the first browser call and keeps its own profile under `~/.local/share/larik/browser-profile`, so sign-ins you make in that window last between sessions. If another larik session already has that profile open, the browser starts with a temporary one instead, and the model is told its sign-ins aren't there.

The model gets these tools:

| Tool | Does |
| --- | --- |
| `browser_navigate` | Opens a URL and returns a snapshot of the page |
| `browser_snapshot` | The page's text, plus its links, buttons and fields, each with a ref such as `e12`. A long page comes in parts (`start`), and `ref` reads just one form, table or frame |
| `browser_screenshot` | An image of the viewport, the full page or one element; `labels` draws each ref on it |
| `browser_click` | Clicks (or hovers) an element by ref with a real mouse event |
| `browser_type` | Types into a field by ref, key by key; optionally presses Enter |
| `browser_select` | Picks options in a `<select>` |
| `browser_press_key` | Presses Enter, Escape, Tab, arrow keys, PageDown… |
| `browser_upload` | Chooses files (inside the working directory) for a file input, or for the chooser a button opens |
| `browser_wait_for` | Waits for text to appear or disappear, or for a few seconds |
| `browser_history` | Back and forward |
| `browser_tabs` | Lists, opens, selects and closes tabs; tabs a page opens become active |
| `browser_eval` | Runs a JavaScript expression in the page |
| `browser_console` | Console messages, exceptions and dialogs since the last call |
| `browser_network` | The requests the page made: method, URL, status and time, with failures marked; and a response's body |

- `browser_navigate` asks per domain like `web_fetch` ("always allow" saves `browser_navigate(domain:github.com)`) and only opens http(s). Every request the page makes (images, scripts, redirects, links clicked) is checked against link-local and cloud-metadata addresses and blocked, with a note in `browser_console`. Clicks and typing ask per call unless you allow the tool.
- Plan mode asks before opening a page and blocks everything that could change something on a site. `browser_snapshot`, `browser_screenshot`, `browser_wait_for`, `browser_console` and `browser_network` only read, so they never ask.
- A click fails, naming what's in the way, when something such as a cookie banner covers the element, instead of clicking the banner.
- Downloads are saved to `~/.local/share/larik/browser-downloads`, and the model is told each file's path. File inputs never open a system dialog; the model uses `browser_upload`.
- **Frames:** the contents of an iframe from the same site are part of the snapshot, and you can click and type inside it. A frame from another site is listed with its URL only; the browser doesn't let a page read it.
- **Subagents** have their own tabs in the same window, so several can browse at once without navigating each other's pages. Their tabs close when they finish. To keep the browser from a subagent, list its tools without `browser`.
- Screenshots need a model that can see images. They're JPEGs at one pixel per CSS pixel (a 1280×900 viewport is about 1,500 tokens on Claude) and stay in the conversation until it's compacted.
- Only personal files can enable the browser or choose its binary; a shared `.larik/settings.json` can only switch it off.
- Alerts and "leave this page?" prompts are accepted, and confirm or prompt dialogs dismissed, automatically; each is noted in `browser_console`.

## Sandbox

`bash` commands run in the operating system's sandbox: Seatbelt (`sandbox-exec`) on macOS, and [bubblewrap](https://github.com/containers/bubblewrap) (`bwrap`) on Linux when installed.

On Windows, install Bash (for example, Git Bash) to use the `bash` tool. Larik has no Windows sandbox, so shell commands ask for approval.

**Inside the sandbox, a command:**

- **Can** read everything.
- **Can** write only to the project (the git root), its own private temp directory, and common build caches (Go, npm, Cargo, `~/.cache`, on macOS also `~/Library/Caches` and the per-user temp root). The literal `/tmp`, shared by every program on the machine, is never writable.
- **Can't** write to `.git/hooks`, `.git/config`, the other git files that point git at a config or hooks elsewhere (`commondir`, `config.worktree`, `info/`, `modules/`, `worktrees/`), `.larik/`, `.claude/` or `.mcp.json`, even inside the project, and can't move or replace `.git` itself, because any of these would let a later command or hook escape the sandbox. Commits still work.
- **Has no network access** except localhost, so tests that start local servers still work, unless you allow some domains (below).
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

**Allowing only some domains.** Between no network and all of it, list the domains sandboxed commands may reach:

```json
{ "sandbox": { "allowed_domains": ["golang.org", "npmjs.org", "pypi.org", "files.pythonhosted.org", "github.com"] } }
```

- Each entry allows that domain and its subdomains (`golang.org` covers `proxy.golang.org`); an IP address must be listed as is.
- Larik runs a small proxy on localhost and sets `HTTP_PROXY`, `HTTPS_PROXY` and `ALL_PROXY` (and `NODE_USE_ENV_PROXY=1`) for sandboxed commands. `go`, `git` over https, `curl`, `npm`, `pip` and `cargo` use it. HTTPS is tunneled end to end; the proxy only sees the host name.
- Everything else is refused, including direct connections and tools that ignore the proxy variables (`git` over SSH, for example). When the proxy refuses a host, the command's result says which, so the model can ask you to add it or re-run the command outside the sandbox.
- The proxy still refuses link-local and cloud-metadata addresses, whatever an allowed name resolves to.
- It applies only while `network` is off. On Linux a helper (`larik __sandbox-bridge`) carries the connection into bubblewrap's network namespace.

Loosening the sandbox (enabling network, allowing domains, adding writable paths, disabling it) is honored only from personal files: `~/.config/larik/config.json` and private project settings under `~/.config/larik/projects/`. A shared `.larik/settings.json` can only switch the sandbox on. `/sandbox` shows the current settings.

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

| Endpoint                                                   | Purpose                                                                                                                                                                   |
| ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `GET /v1/health`                                           | Liveness check (no auth)                                                                                                                                                  |
| `GET /v1/info`                                             | Version, cwd, providers, sandbox                                                                                                                                          |
| `GET /v1/sessions`                                         | Sessions in this directory, with `loaded`/`busy` flags                                                                                                                    |
| `POST /v1/sessions`                                        | New session `{model, effort, mode}`, or load one with `{resume: id}` / `{continue: true}`                                                                                 |
| `GET /v1/sessions/{id}`                                    | Model, mode, busy, usage, pending permissions, running tasks                                                                                                              |
| `PATCH /v1/sessions/{id}`                                  | Change `model`, `effort` or `mode`                                                                                                                                        |
| `DELETE /v1/sessions/{id}`                                 | Stop and unload (the transcript stays on disk)                                                                                                                            |
| `GET /v1/sessions/{id}/messages`                           | Full transcript (works for unloaded sessions too)                                                                                                                         |
| `POST /v1/sessions/{id}/fork`                              | Branch into a new loaded session. `{at: i}` keeps the messages before index `i` (which must be a prompt) and returns that prompt's text; with no `at`, everything is kept |
| `GET /v1/sessions/{id}/events`                             | SSE event stream                                                                                                                                                          |
| `POST /v1/sessions/{id}/prompt`                            | `{text}` starts a run and returns 202 (409 if busy); `{text, wait: true}` returns the final answer                                                                        |
| `POST /v1/sessions/{id}/cancel`                            | Interrupt the current run                                                                                                                                                 |
| `GET /v1/sessions/{id}/permissions`                        | Pending permission requests                                                                                                                                               |
| `POST /v1/sessions/{id}/permissions/{request_id}`          | Answer: `{allow, always, reason}`. An `always` answer returns `{allowed, persisted}` and an `error` if saving failed; other answers return 204.                           |
| `POST /v1/sessions/{id}/compact` · `/undo` · `/clear`      | Same as the TUI commands                                                                                                                                                  |
| `GET /v1/sessions/{id}/tasks` · `DELETE …/tasks/{task_id}` | List or stop background tasks                                                                                                                                             |

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

## Debug mode and traces

Debug mode records what the harness does in a session, so you can review it afterwards or watch it live: each prompt, every model request as it was sent (system prompt, tools, messages, parameters), each response with its timing, token use and cost, the raw HTTP exchange with the provider, tool calls with their input and output, permission answers and how long they waited, hooks, notices and compactions. Subagents are recorded in their own lanes.

Turn it on with `larik --debug` (also `larik -p --debug` and `larik serve --debug`), `LARIK_DEBUG=1`, `"debug": true` in personal settings, or `/debug on` in a session (`/debug off` stops it, `/debug` shows where it's recording). While it records, the footer shows `● rec`.

Review a trace with `/trace` in the TUI, or `larik trace` from a shell, which opens the latest traced session in this directory (`larik trace <session-id>` for another). It serves a page on `127.0.0.1` behind a random token and opens your browser:

- **Timeline:** one lane per agent. Each request is split into waiting for the first output and generating; tool calls, permission waits and hooks sit beside them, with prompts marked. Long idle stretches are collapsed. Scroll to zoom, drag to pan, shift+drag to filter the list to a time range, double-click to reset. It follows a live session as it grows.
- **List:** every record in order, grouped by prompt, filterable by kind, agent and text.
- **Inspector:** for a request, its overview (model, timing, tokens per second, usage, cost), the prompt as sent with the system prompt and tool list diffed against the previous request and new messages marked, the response, and the raw HTTP request and streamed response. For a tool call, its input, output and permission answer.

`larik trace --html trace.html` writes one self-contained page instead, to keep or share.

Traces are stored next to the session, in `<session>.trace/` under the data directory (owner-only), and deleted after 14 days (`debug_retention_days`; negative keeps them). Only new messages are written with each request, so a trace grows with the conversation rather than with its square; the raw HTTP bodies, though, hold each full request. API keys and other credential headers are redacted; prompts, file contents and tool output are not, so treat a trace like the session itself.

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
    "work-gateway": {
      "type": "openai-compatible",
      "base_url": "https://llm.example.com/v1",
      "api_key_env": "WORK_LLM_KEY"
    }
  },
  "models": {
    "gpt-5.5": {
      "provider": "openai",
      "context_window": 400000,
      "max_output": 128000,
      "input_price": 0,
      "output_price": 0
    }
  }
}
```

Personal settings, all editable from `/config` (which changes only the key you edit):

- `theme`: `auto` (follow the terminal's background, the default), `dark` or `light`. Both themes use a teal accent and leave the background transparent, so your terminal's own background shows through; only diff lines keep a faint green or red tint.
- `verbose`: show tool output (up to 40 lines) and thinking in full. `ctrl+o` still toggles thinking.
- `spinner_tips`: a one-line tip under the spinner during a turn.
- `debug` and `debug_retention_days` (not in `/config`): see [Debug mode and traces](#debug-mode-and-traces). Honored only from personal settings.
- `editor_mode`: `normal` or `vim`. See [Vim mode](#vim-mode).
- `keybindings` and `status_line` (not in `/config`): see [Rebinding keys](#rebinding-keys) and [Status line](#status-line).
- `mouse`: wheel scrolling in the TUI (default on). Off leaves the mouse to the terminal, so text can be selected without a modifier.
- `auto_compact`: summarize the conversation when the context is 80% full. `/compact` works either way.
- `token_saver`: opt-in command-output filtering (`/config token_saver=true`). Recognized Git, search/listing, Go, Cargo, Node package-manager, and pytest output is shortened before entering model context. Unrecognized output and failures pass through. A `bash` call can set `raw_output: true`; `raw_output` retrieves the exact captured stdout/stderr by tool-call ID without rerunning the command. Raw output stays in private session files. The displayed byte savings are estimates for command output; use `/cost` to inspect actual session token usage and cost.
- `notifications`: `off`, `bell`, or `desktop` (OSC 9: iTerm2, Ghostty, kitty, WezTerm; other terminals get the bell). Sent only while the terminal is unfocused, when larik asks for permission or a turn of 10s or more ends. Notification hooks are separate.
- `language`: what the model replies in, e.g. `"Indonesian"`. A change applies after `/clear` or in a new session, so the cached prompt stays valid.
- `roles`, `fallbacks`, `budget` ("Model routing" and "Session budget" in `/config`): see [Mixing cheap and strong models](#mixing-cheap-and-strong-models).
- `stall_timeout`: how many seconds a model server may send nothing before Larik stops the request, reports it, and switches to the fallback model if there is one. Default 300, or 900 for local servers (Ollama, LM Studio, `openai-compatible`), which may still be loading the model. Set it per provider with `"providers": {"ollama": {"stall_timeout": 1800}}`; a negative value waits forever. Keep-alives from the server count as activity, so a model that is thinking isn't cut off.
- `checkpoint_retention_days` ("Undo history" in `/config`): how long `/undo` snapshots are kept, so `/undo` still works after resuming a session. Default 7; a negative value (`forever` in `/config`) keeps them forever. Old snapshots are deleted at startup, for every project, so this is honored only from personal settings.

`models` entries override the built-in catalog. The catalog drives context percentage, compaction, and cost.

## MCP servers

Larik connects to [Model Context Protocol](https://modelcontextprotocol.io) servers over stdio, streamable HTTP, or SSE. It reads servers from `mcp_servers` in any Larik settings file, and from a project `.mcp.json` in the common format, so configs shared with other tools work unchanged:

```json
{
  "mcpServers": {
    "github": {
      "type": "http",
      "url": "https://api.githubcopilot.com/mcp/",
      "headers": { "Authorization": "Bearer ${GITHUB_TOKEN}" }
    },
    "fs": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "${HOME}/notes"]
    }
  }
}
```

- Server tools appear as `mcp__<server>__<tool>`. In rules, `mcp__github` covers every tool on that server.
- `${VAR}` and `${VAR:-default}` are expanded in the command, args, env, URL, and headers.
- **Trust:** servers from shared project files (`.mcp.json`, `.larik/settings.json`) don't start until you run `/mcp approve <name>`. The approval is stored in private project settings and pinned to a hash of the server's config, so editing the command or URL requires re-approval. Servers in personal settings start automatically.
- **Permissions:** MCP tools ask before running. The exception is tools that declare both `readOnlyHint: true` and `openWorldHint: false`.
- **Stable tool set:** servers start in the background at launch, and their tools are loaded before the first request. The tool set then stays fixed for that context so prompt caches stay valid. Newly approved or restarted servers join after `/clear` or in a new session.
- Stdio server stderr is written to `~/.local/share/larik/logs/mcp-<name>.log`.
- Text and images in a tool's result are passed to the model. Audio and binary resources are summarized, not sent.
- **Many tools:** when the connected servers offer more than 30 tools, or their definitions add up to more than about 6,000 tokens, Larik stops sending every definition with every request. The model gets two tools instead: `tool_search`, which lists the available tools and returns the definition of the ones it asks for, and `call_tool`, which runs one by name. Permission rules, prompts and hooks apply to the tool being run, exactly as if it had been called directly, and the conversation shows that tool's name. Set `"tool_search": "on"` to always do this, or `"off"` to never (the default is `"auto"`).
- **Resources:** when a server offers resources, the model gets `list_mcp_resources` and `read_mcp_resource`. You can attach one yourself as `@<server>:<uri>`; the `@` picker lists them under "MCP resources". Text is attached as text and images as images. Images a server's tool returns, such as a screenshot, go to the model too.
- **Prompts:** a server's prompts are slash commands named `/mcp__<server>__<prompt>`, listed in the `/` palette with their arguments. Arguments go in order, split on spaces, and the last one takes the rest of the line. They work with `-p` too.
- **OAuth:** an `http` or `sse` server that asks for sign-in shows as "needs sign-in" instead of opening a browser at startup. Run `/mcp login <name>`: Larik opens the server's sign-in page, catches the redirect on `127.0.0.1`, and keeps the tokens in `~/.config/larik/mcp-auth/` (owner-only), refreshing them as needed, so `-p` and `larik serve` use the sign-in too. `/mcp logout <name>` forgets it. By default Larik registers itself with the server's authorization server; for a server that needs a pre-registered client, set `"oauth": {"client_id": "…", "client_secret": "${SECRET}", "callback_port": 8765}`, and `"scopes"` to choose scopes. For token-based servers, use `headers` instead.

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
- **Running a skill yourself:** type `/<skill-name> [args]`, in the TUI or with `-p`. `$ARGUMENTS` in the body is replaced with the args, and `$1` to `$9` with the args one by one (split on spaces). Otherwise the args are appended.
- **Frontmatter flags:** `disable-model-invocation: true` keeps a skill out of the model's index, so it only runs when you invoke it. `user-invocable: false` hides it from `/`.
- Skills are instructions, like `AGENTS.md`. Anything a skill asks the model to run still goes through the normal permission checks.

### Custom commands

Claude Code–style custom commands work too: a Markdown file whose name is the command.

```markdown
---
description: Fix a GitHub issue
argument-hint: [issue-number] [priority]
---

Fix issue #$1 with priority $2. Current branch: !`git branch --show-current`
```

- **Where:** `~/.claude/commands` and `~/.config/larik/commands` for yours; `.claude/commands` and `.larik/commands` from the repo root down to the working directory for the project's. A file in a subdirectory still takes its own name (`frontend/review.md` is `/review`).
- **Running one:** type `/<name> [args]`. The palette lists commands with their `argument-hint`, and `/skills` lists them with the skills. Arguments work as for skills.
- **Frontmatter is optional.** Without a `description`, the first line of the file describes the command in the palette. Only a command with a `description` goes in the model's index, where it can load it with the `skill` tool; `disable-model-invocation: true` keeps a described one out.
- **Skills win:** a skill and a command with the same name resolve to the skill, and built-in commands such as `/model` always win. The two commands Larik ships as command files, `/review` and `/security-review`, are the exception: yours replace them.
- **`` !`command` ``** runs the command when you invoke the command file and puts its output in its place, in the sandbox when there is one. It runs only when your permission rules would allow it without asking (sandboxed commands, the safe list, allow rules); otherwise it is left out with a note, since a command file can come from a repository. Claude Code's `allowed-tools` frontmatter is ignored for the same reason. `model` is ignored too.
- New or edited commands appear after `/clear` or in a new session, like skills.

### Reviewing changes

`/review` and `/security-review` are commands that ship with Larik. Each reviews a set of changes and reports findings, most serious first, with the file and line, how it fails (or is exploited) and a fix. They don't edit anything; ask for the fixes afterwards if you want them.

| You type | What is reviewed |
| --- | --- |
| `/review` | Your uncommitted changes. If the tree is clean: this branch's commits since the default branch, or else the last commit |
| `/review 123` or `/review #123` | That pull request, fetched with the GitHub CLI (`gh`) |
| `/review main` | What the current branch has that `main` doesn't (or, for a branch ahead of yours, what it has that yours doesn't) |
| `/review v0.3.0..HEAD` | That range |
| `/review focus on the error handling` | The default scope, with that focus. A focus can follow a PR, branch or range too |

- **Larik collects the changes itself**, with fixed read-only `git` (and `gh`) commands, and gives them to the model. So a review works in every permission mode, including `plan`, where the model can't run git, and it starts with the diff in hand. The model still reads the surrounding code with its tools.
- A diff over 150 KB is cut, and the model is told to read the rest. New files git doesn't track yet are listed for it to read.
- The diff and a pull request's description are passed as data: text in them is never run as an inline command or treated as instructions.
- `/review` looks for wrong behavior, unhandled cases, broken callers, concurrency and resource problems, data loss and missing tests, and leaves style to your linter. `/security-review` looks for vulnerabilities the change introduces and reports only what it can describe an exploit for.
- They work with `larik -p "/review"` too, for a script or a CI job.
- **To change one,** put your own `review.md` or `security-review.md` in a commands directory; it replaces the built-in. `/skills` shows which is in use.

**On pull requests, in CI.** The repository is also a GitHub Action that runs a review on each pull request and posts it as one comment, updated on every push:

```yaml
name: Review
on: pull_request
permissions:
  contents: read
  pull-requests: write   # to post the comment
jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: vwibowo/larik@v0.4.0
        env:
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}   # or OPENAI_API_KEY, GEMINI_API_KEY
        with:
          command: review          # or security-review
```

| Input | Default | Meaning |
| --- | --- | --- |
| `command` | `review` | `review` or `security-review` |
| `target` | the pull request | A PR number, a branch, or a range such as `origin/main...HEAD` |
| `focus` | | What the review should concentrate on |
| `model` | from the API key | `provider/model` |
| `version` | `latest` | The Larik release to download; its checksum is verified |
| `comment` | `true` | Post (and later update) a pull request comment. The review is always in the job summary and the `review` output |
| `github-token` | the workflow's token | Used to read the pull request and to comment |

- The model runs in `plan` mode: it reads the checked-out code and nothing more. It can't run commands, edit files or reach the token, and a repository's own hooks and MCP servers don't start without an approval that CI doesn't have.
- Pull requests from forks don't get your secrets under `pull_request`, so the review fails there for lack of an API key. Don't switch to `pull_request_target` to work around that: it would hand secrets to a job that reads untrusted code.
- Pass only text you wrote as `focus`; a pull request's title or body doesn't belong there.
- Outside GitHub, the two scripts work by themselves: `scripts/ci-install.sh` downloads and verifies a release, and `scripts/ci-review.sh` runs the review (`REVIEW_TARGET`, `REVIEW_COMMAND`, `POST_COMMENT=false`).

## Memory

Larik remembers things between sessions as short notes: who you are and how you like to work, corrections you've given it, decisions and constraints of the project, and where things live outside the repository. It saves a note when it learns something a later session will need, or when you ask it to ("remember that we deploy on Tuesdays").

```
> /memory
notes for this project  (~/.local/share/larik/memory/myapp-3f9a…)
  use-pnpm                     project   Use pnpm for package commands in this project
  prefers-table-tests          feedback  Write Go tests as table tests
notes for every project  (~/.local/share/larik/memory/_user)
  who                          user      Senior Go developer, new to the frontend
```

- **Two scopes.** Project notes belong to one project (its git root); user notes apply everywhere. Both are kept under `~/.local/share/larik/memory`, outside your repository, so nothing is committed or shared.
- **Notes are Markdown files** with a name, a one-line description and a type (`user`, `feedback`, `project` or `reference`). Edit or delete them by hand if you like; a plain `.md` file you drop in works too.
- **What the model sees.** At the start of each context (a new session, or after `/clear`) the system prompt lists the notes by name and description. It reads a note's content with the `memory` tool when the description is relevant. A note saved during a session joins that list at the next fresh context.
- **`/memory add <text>`** saves a note yourself (`--user` for every project) and tells the model straight away. `/memory show <name>` and `/memory delete <name>` read and remove one.
- **No approval needed.** The `memory` tool writes only to that directory, so it runs without asking in every mode, plan included; each save shows in the conversation. Add `"memory"` to your deny rules to stop the model saving or reading notes.
- **Notes are background, not instructions.** The model is told that they may be out of date and to check what they name before relying on it, and never to save something a web page, a file or a tool result asks it to remember, or any secret. Still, read `/memory` now and then: a note shapes every later session.
- **Off switch:** `"memory": {"enabled": false}`. A shared `.larik/settings.json` can switch memory off for a repository but not on.

Subagents don't get memory; the main agent passes on what they need.

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
tools: Read, Grep, Glob, Bash # optional; omit for all tools. mcp__<server> allows a whole server
model: worker # optional: inherit (default), a role (worker, explore, smart, opus/sonnet/haiku, your own), or provider/model
isolation: worktree # optional: always run in its own git worktree
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
- **Confinement:** file writes outside the worktree ask for permission, as for any path outside the working directory. When the sandbox is on, `bash` may write only to the worktree and to the repository's `.git`, so commits work. `.git/hooks`, `.git/config` and the other git files that choose which code git runs stay read-only.
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

- **Diagnostics after edits:** when `edit`, `multi_edit` or `write` changes a file, Larik syncs it to the file's language server and appends new errors and warnings to the tool result, for example `ERROR 5:19 undefined: gret (compiler)`. Errors the change caused in other files are summarized. Reading a file warms up the server in the background.
- **The `lsp` tool** (read-only) offers `definition`, `references`, `hover`, `symbols`, `workspace_symbols`, `diagnostics` and `code_actions`, using the 1-based line and column that `read` shows.
- **Code actions:** `code_actions` lists a server's quick fixes and refactorings for a line, such as adding a missing import or organizing imports. The model applies one with `apply_code_action` and the action's title. That writes files, so it asks like `edit` (accept-edits mode allows it inside the project), each changed file gets an undo snapshot, and the result shows new diagnostics. Actions that create, rename or delete files aren't applied. A server can only change files through `workspace/applyEdit` while an action you approved is running.
- **Pull diagnostics:** servers that answer `textDocument/diagnostic` (LSP 3.17) are asked for diagnostics directly after an edit instead of Larik waiting for them to publish.
- **Built-in servers** are enabled automatically when their binary is on your `PATH`: gopls, typescript-language-server, pyright-langserver, rust-analyzer and clangd. Each starts on the first matching file, once per project root (found from markers like `go.mod`, `package.json` or `Cargo.toml`). Servers shut down when Larik exits, and their logs go to `~/.local/share/larik/logs/lsp-<name>.log`.
- **Configuration:**

  ```json
  {
    "lsp": {
      "gopls": { "initialization_options": { "staticcheck": true } },
      "clangd": { "disabled": true },
      "zls": {
        "command": ["zls"],
        "extensions": [".zig"],
        "root_markers": ["build.zig"]
      }
    }
  }
  ```

  New server commands are honored only from personal files (`~/.config/larik/config.json`, private project settings). A shared `.larik/settings.json` can only disable servers.

- **Timing:** edits wait for fresh diagnostics for up to 3s (15s while a server is still loading the project). Servers that stay silent on clean files get a shorter wait.
- `/lsp` shows servers and their status.

## Hooks

Hooks run at points in the agent lifecycle: shell commands, or prompts a model answers. The format matches Claude Code's, so existing hooks work. Matchers are case-insensitive, so `Bash` matches Larik's `bash`.

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "bash",
        "hooks": [
          { "type": "command", "command": "./scripts/guard.sh", "timeout": 10 }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "write|edit",
        "hooks": [
          { "type": "command", "command": "gofmt -l . >&2 && exit 2 || true" }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          { "type": "command", "command": "./scripts/require-tests.sh" }
        ]
      }
    ]
  }
}
```

| Event                                      | Fires                                                                    | Can do                                                                                                |
| ------------------------------------------ | ------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------- |
| `SessionStart`                             | first turn of each fresh context (matcher: `startup`, `resume`, `clear`) | Add context. Stdout goes to the model.                                                                |
| `UserPromptSubmit`                         | before a prompt is sent                                                  | Block it, or add context (stdout)                                                                     |
| `PreToolUse`                               | before the permission check (matcher: tool name)                         | `allow` (skips the prompt), `deny`, `ask`, or rewrite the input with `updatedInput`                   |
| `PostToolUse`                              | after a tool runs                                                        | Send feedback to the model                                                                            |
| `Stop` / `SubagentStop`                    | when the agent (or a subagent) would end its turn                        | `decision: "block"` makes it continue with your `reason` (at most 5 times; `stop_hook_active` is set) |
| `PreCompact`, `Notification`, `SessionEnd` | compaction, permission prompts, exit                                     | Observe only                                                                                          |

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

**Prompt hooks** ask a model instead of running a command, for checks that need judgment:

```json
{ "hooks": { "Stop": [ { "hooks": [
  { "type": "prompt", "prompt": "Did the agent run the tests and see them pass? Look at the transcript at transcript_path in: $ARGUMENTS" }
] } ] } }
```

- `$ARGUMENTS` becomes the hook input as JSON; without it, the input is appended.
- The model answers `{"ok": true}` or `{"ok": false, "reason": "…"}`. `ok: false` acts like exit code 2 for that event: a `Stop` hook makes the agent continue with the reason, a `PreToolUse` hook denies the call, a `UserPromptSubmit` hook blocks the prompt.
- It runs on the hook's `model` (a provider/model or a routing role) if set, otherwise on your `explore` role, otherwise on the session's model. Its cost counts toward the session and its budget. The default timeout is 30s.
- An answer that can't be read, a timeout or an error doesn't block; you see a message.

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
internal/browsercdp browser_* tools driving Chrome over the DevTools Protocol
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
