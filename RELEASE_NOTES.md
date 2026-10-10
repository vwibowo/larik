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
