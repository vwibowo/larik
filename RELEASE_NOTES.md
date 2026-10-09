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
