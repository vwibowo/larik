# Larik v0.20.0

_October 27, 2026 · Previous release: v0.19.1._

A release about measuring and helping weaker models: `larik bench` can now compare harness variants and run tasks as a role's subagent, and code mode wastes far fewer turns on malformed calls and script mistakes.

### Added

- **Compare harness variants with `larik bench --harness`.** Each variant is a directory laid out like `~/.config/larik` (`AGENTS.md`, `skills/`), so a prompt or skill change can be measured before you adopt it. `none` is the empty default and `personal` your own instructions and skills.
- **Bench a model in the job it will have.** `--as worker` runs each task the way that role's subagent runs it (its prompt, tools, turn cap and context setting), and `--context full,minimal` compares the two context settings. Rows a harness can't reach (minimal-context subagents) are marked `unseen`, with a warning.
- **Three harder bench tasks** with a shortcut that hidden tests reject: `fix-without-overfitting`, `fix-at-the-source` and `follow-conventions`. Bench rows size to the longest label.

### Changed

- **Code mode declares `write` and `write_files` directly**, so whole files are written without escaping them inside a script.
- **Direct calls to script-only tools run.** In code mode, a model that calls `read`, `grep`, `edit` or `bash` directly now gets the call run, with the same permission checks, instead of an "unknown tool" round trip. On the NVIDIA NIM Nemotron model this cut malformed calls from 29% to 5% and bench time by 40%.
- **The prompt asks the model to finish what instruction files ask.** A line before `AGENTS.md`/`CLAUDE.md` asks it to go back over them before finishing, such as a changelog entry. The Nemotron model's `follow-conventions` pass rate went from 14/35 to 23/35.
- **Scripts explain their own mistakes.** `run_code` resolves prefixed tool names (`tools.read`, `functions.read`), reports the real syntax error in scripts that use `await`, shows a script's last value after `await`, and adds notes for template literals broken by backticks, backslashes JavaScript drops, Node.js or Python habits (`require`, `import`, `process`) and `.then` on results.
- **Near-miss paths point at the real one.** A result saying a path doesn't exist notes the correct path when the one called is the working directory with a character or two changed.

### Fixed

- **Runaway tool calls stop.** A response whose tool-call arguments turn into thousands of whitespace characters is stopped and asked again once, instead of streaming until the output limit (one run streamed 33 MB over 13 minutes).

### Compatibility notes

- Code mode now declares `write` and `write_files` alongside `run_code`, and runs direct calls to other core tools. Permission rules, hooks and auto mode apply to them as before.
- System prompts gain one line before the instruction files, so provider prompt caches warm again once after upgrading.

# Larik v0.19.1

_October 25, 2026 · Previous release: v0.19.0._

A patch release with two fixes found reviewing v0.19.0.

### Fixed

- **Code actions stay within the rule that allowed them.** When only a scoped allow rule such as `edit(src/**)` approved `apply_code_action`, a file the language server adds outside that rule is now refused instead of written without asking. Approving the prompt yourself still covers every file the action changes.
- **Worktree subagents work with spaced git config names.** A repository config key whose subsection contains spaces (for example `filter.a b c.clean`) made every git command Larik ran for a worktree fail, reported as a repository without commits.

### Compatibility notes

- Automation relying on a scoped `edit(...)` rule for code actions that touch other files will now see those actions refused; widen the rule or approve the call.

# Larik v0.19.0

_October 25, 2026 · Previous release: v0.18.0._

### Highlights

- **Trace viewer in the terminal.** `/trace` uses Larik's browser panel when available, with owner-isolated tabs and popup chains. `/trace external` opens the system browser; `/trace browser` insists on the panel. Personal `trace.open_in` settings select the default, and automatic mode falls back on setup/startup failure.
- **Visible approval waits.** Permission prompts and live status show elapsed waiting time, including background requests. Auto-mode checks pause script clocks; execution time resumes before the tool runs.
- **More reliable Claude CLI subagents.** Subagents default to tools execution unless explicitly configured otherwise. A stall watchdog pauses for tool and permission waits, warns after a minute of silence, and uses the configured stall timeout. Runtime usage and budgets survive resume.
- **Safer execution and persistence.** User-scope memory changes ask for approval outside yolo or explicit trusted allow rules. Unsandboxed bash asks when a sandbox is available; provider credentials are scrubbed unless explicitly passed through. Editing aliases, saved screenshots, LSP code actions, worktree metadata and case-variant protected paths have stronger permission/sandbox coverage.
- **Compaction and undo.** Oversized conversations can be summarized in parts. In-place subagent edits have their own undo units, and undo/rewind refuse unsafe restores while tasks are writing or changes overlap.
- **Server reliability.** Shutdown has one five-second budget and promptly disconnects SSE clients while preserving transcript locks until writers finish. This release also fixes wait-response races and background permission cleanup, rejects malformed event cursors with HTTP 400, and returns HTTP 409 during conflicting session-close operations.
- **Provider and tool fixes.** Unmatched tool results no longer break subsequent provider requests, and script grep works on a single file.
- **Distribution and website.** Larik is MIT licensed; archives include the license and third-party notices. Notice ordering is locale-independent, and tar archives omit macOS AppleDouble metadata files. The website has a new dark design and install tabs, with accurate default-sandbox wording.

### Compatibility notes

- Server event consumers should tolerate optional `requested_at` and `task_id` fields, HTTP 400 for malformed event cursors, and HTTP 409 during session closure.
- Existing `edit(...)` rules now cover `multi_edit` and `apply_code_action`; `write(...)` rules cover saved browser screenshots. Automation may see additional approval prompts for unsandboxed commands and user-scope memory mutations.

### Publication and verification limits

- Published October 25, 2026 as tag `v0.19.0`, with five archives and `SHA256SUMS`; the website version and downloads point at it.
- Interactive checks passed for trace-tab isolation, approval wait timing, an Auto-mode user-memory prompt, and server shutdown with an active SSE client. The temporary memory marker was removed.
- The live Claude CLI Opus subagent retry passed: Larik's `read` tool returned the first three lines of `go.mod`, and sandboxed `bash` completed a 65.082-second wait and returned the expected marker, with no tool errors, stall warnings, retries or model substitutions. This verifies tool use and watchdog behavior during a long-running tool call. Deliberately stalled CLI and Claude-specific human-approval waits were not exercised live; automated tests cover those paths.

# Larik v0.14.0

_October 10, 2026_

### Added

- **`skills.hide` setting.** `"skills": {"hide": ["cmux-*"]}` takes matching skills out of the model's index and the `skill` tool, so skills that don't apply to a project don't take up prompt tokens. You can still run them as `/name`, and `/skills` marks them as hidden. A shared `.larik/settings.json` may set it too. In the TUI it's the "Hidden skills" row of `/config`, which saves to your private settings for the current project.

- **Offline-safe provider catalog.** Larik uses its private cached provider and model metadata at startup when available, then refreshes it in the background for the next launch.

### Changed

- **`larik bench` now measures Larik's system prompt.** Runs use the base prompt and the fixture's project instructions, without personal instructions, skills or memory. Previously bench sent no system prompt; agent runtimes such as `claude-code-cli` could substitute their own, and prompt changes weren't measured. Results from before this change aren't directly comparable. Bench also passes whole-turn runtimes through, so `claude-code-cli/*` models work instead of failing before a request.
- **A leaner system prompt.**
  - The base prompt drops guidance that the `edit`, `write` and `todo_write` descriptions already give.
  - The memory section is about a fifth shorter.
  - Skill descriptions in the index are clipped at 160 characters instead of 250.
  - The plan-mode note no longer mentions graphify, and it names `run_code` only when the model has it.
  - The safety rules now say not to commit, push or create branches unless asked or allowed to.
  - Shorter tool descriptions for `task`, `bash`, `todo_write`, `exit_plan_mode`, `memory`, `multi_edit` and `lsp`: the built-in tool definitions are 7% smaller, and `task` with model roles and a delegation policy is 15% smaller.
- **The sandbox summary is the same in every run.** The system prompt calls the sandbox's temp directory `$TMPDIR` instead of giving its per-run path, so the parts that follow (skills, memory) stay in the provider's prompt cache from one session to the next.
- **`run_code` state no longer grows the transcript.** Values from `store` now live in a state file beside the session, rewritten on each change and so never larger than the 1 MB limit. The transcript records only which keys a script stored or deleted, however often large values are rewritten. Sessions from v0.13.1 still load their logged values. A branch now starts with the session's current state wherever it is cut, rather than the state at the cut.
- **`/clear` also clears `run_code` state.** A cleared context now starts scripts fresh too, and a resume doesn't bring the old values back. `/reload` and setting changes that need a fresh context still keep them.
- **`larik bench` shows why a run errored.** A run that ended in an error now prints the agent's error message, such as the provider's rate-limit or connection error, under its line, and `--keep-failed` adds it to the end of `transcript.md`. Before, it said only `ended: error`.

### Fixed

- **Tool time counts overlapping calls once.** The turn's tool time no longer adds up calls that ran at the same time (parallel calls, a script's calls, a subagent's calls under its task); it measures how long any tool was running.
- **A slow browser start says so.** When Chrome doesn't start in time, the browser tool now reports `no response within 12s` instead of `context canceled`.

### Performance

- **Bounded local work and caches.** Fallback search reuses scanner buffers, long live-thought output renders only its visible tail, rendered Markdown is cached within a memory limit, and the web fetch cache expires and evicts entries within entry and text limits.
- **Faster TUI startup.** The loading screen appears before setup finishes, the ready screen receives the terminal's actual size, and Larik avoids duplicate Git root lookups during TUI and prompt initialization.

### Benchmark results

- **Prompt-trim smoke test.** The build before the prompt changes (`40ea63e`, with the two benchmark-method fixes applied) and the current build (`c6f682a`) each passed 6/6 runs with `codex/gpt-5.6-sol` in `hybrid` mode: three runs each of `fix-off-by-one` and `implement-validation`, with no malformed calls. Results were mixed: total input was 93.3k before and 95.2k after; `implement-validation` improved from median 15.4k input and 6 requests to 11.8k and 5, while `fix-off-by-one` moved from 16.5k and 7 to 23.7k and 9. Six runs are a functional smoke test, not a performance conclusion. Opus was not measured because the Claude subscription session limit was reached; its zero-request failures are not benchmark results.

# Larik v0.13.1

_October 9, 2026_

### Added

- **Server-free local speech commands.** Speech-to-text and text-to-speech can run local commands instead of requiring an OpenAI-compatible server. This supports tools such as `whisper-cli`; macOS falls back to the built-in `say` voice when no TTS endpoint or command is configured. Existing HTTP endpoints remain supported, and audio commands stay restricted to personal settings.
- **More capable `run_code` scripts.** `tools.parallel` batches calls while preserving permissions and ordering; script `bash` calls return structured output and exit codes; long script output is recoverable with `raw_output`; MCP names are valid JavaScript identifiers; and `tools.search` finds callable tools by keyword.
- **Persistent `run_code` state.** Scripts can use `store(key, value)` and `load(key)` for small JSON values that survive later scripts, `/clear`, compaction, resume and branching. Changes are transactional—failed or interrupted scripts commit nothing—and append to the session transcript rather than rewriting it. Values are capped at 256 KB each and 1 MB together.

### Changed

- **Persistent-state benchmark.** `larik bench` includes `persistent-script-state`, which mechanically requires `store` and `load` in separate scripts under `hybrid` and `code`, while retaining a normal tool-chain fallback for `tools`.
- **`/stt-language auto`.** Clears a saved speech-to-text language so it is detected per recording again; `id` and `en` work as before.

### Fixed

- **Changing the STT language during a transcription is safe.** `/stt-language` no longer races with a recording being transcribed.

### Benchmark results

- **Persistent-state smoke test.** After clarifying the benchmark's numeric output fields, `persistent-script-state` passed its first live run with `codex/gpt-6-sol` in both `hybrid` (89.2s, 15.4k input tokens, 7 requests) and `code` (33.1s, 12.3k input tokens, 8 requests). One run is a functional smoke test, not a performance conclusion.
- **`run_code` execution comparison.** Against the pre-`run_code` enhancement build (`dd05591`), the current build passed all 24 benchmark runs using `codex/gpt-6-sol` across `hybrid` and `code` execution. The benchmark covered `fix-off-by-one`, `implement-validation`, `rename-across-files`, and `find-undocumented`, with three runs per task and mode. `code` mode reduced total input from 130.4k to 117.3k tokens (10.0%); `hybrid` increased from 166.0k to 172.8k. The strongest per-task result was `code`/`find-undocumented`, where median requests fell from 9 to 7 and input from 27.5k to 25.2k. Pass rate remained 12/12 in each build and mode. Results are model- and service-dependent; no promise-based async implementation is planned based on this run.
