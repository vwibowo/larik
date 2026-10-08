# Unreleased

### Added

- **Persistent `run_code` state.** Scripts can use `store(key, value)` and `load(key)` for small JSON values that survive later scripts, `/clear`, compaction, resume and branching. Changes are transactional—failed or interrupted scripts commit nothing—and append to the session transcript rather than rewriting it. Values are capped at 256 KB each and 1 MB together.

### Benchmark results

- **`run_code` execution comparison.** Against the pre-`run_code` enhancement build (`dd05591`), the current build passed all 24 benchmark runs using `codex/gpt-6-sol` across `hybrid` and `code` execution. The benchmark covered `fix-off-by-one`, `implement-validation`, `rename-across-files`, and `find-undocumented`, with three runs per task and mode. `code` mode reduced total input from 130.4k to 117.3k tokens (10.0%); `hybrid` increased from 166.0k to 172.8k. The strongest per-task result was `code`/`find-undocumented`, where median requests fell from 9 to 7 and input from 27.5k to 25.2k. Pass rate remained 12/12 in each build and mode. Results are model- and service-dependent; no promise-based async implementation is planned based on this run.

# Larik v0.13.0

_October 7, 2026_

### Added

- **Prompt suggestions.** When a turn ends normally, Larik suggests a likely next prompt as dim text in the empty input; `tab` puts it in the input to edit or send, and typing anything else ignores it. The request reuses the conversation's cached prompt, and its spend counts toward `/cost` and the session budget. It is skipped after an interrupted or failed turn, while prompts are queued, once a budget is spent, and on the Claude Code CLI runtime. Turn it off with the `prompt_suggestions` setting; a shared `.larik/settings.json` can turn it off but not on.
- **PDF `@` mentions.** `@spec.pdf` (up to 32 MB) attaches the whole document, so the model reads its layout, tables and figures rather than extracted text. Works on Anthropic, OpenAI and Gemini; any other provider tells you instead of sending a request it would reject. Switching to such a model mid-session keeps the conversation working: the PDF is dropped from later requests and the model keeps a note that it was there.
- **Per-model sampling.** `/sampling` shows the temperature, `top_p` and `top_k` in force for the current model and where they came from, and saves new values for that model (`default` removes them, `none` sends nothing). Personal config takes `sampling` and `model_sampling`. Larik ships no built-in values, so each server's defaults stand until you set something, and parameters a provider rejects are dropped rather than sent. A shared `.larik/settings.json` can't set it.
- **Keyless DuckDuckGo search, opt-in.** Set the search provider to `ddg` (or pick DuckDuckGo in `/web-search-config`) for a `web_search` backend that needs no account. It is never selected automatically: its keyless API answers entity lookups but not specific questions, so an empty answer is reported as an error that names the indexed backends.
- **Sharper `larik bench` metrics.** Each run now reports tool calls the model formed so badly they never reached a tool (unparseable or truncated arguments, schema rejections, unknown tools), broken down by kind and as a share of all calls, and separately counts runs the model ended by talking instead of acting ("stopped without finishing", or "answered without calling a tool").

### Changed

- **Every tool's arguments are checked against its schema.** Built-in tools are now held to the schema the model was shown, as MCP tools already were, so a missing or mistyped argument comes back to the model as a clear `INVALID_ARGUMENTS` error before the tool runs. A `null` for an unused argument is ignored, and a schema that can't be compiled isn't enforced.

### Fixed

- **Tool calls written as text are recovered.** When an Ollama or OpenAI-compatible server returns a call as `<tool_call>{…}` text instead of a tool call (usually a server tool-template setting), Larik runs the call instead of ending the turn, never displays the markup, and names the server fix (llama.cpp's `--jinja`, vLLM's `--enable-auto-tool-choice`). Only an explicitly delimited call to a tool that was actually offered is recovered, and it goes through the usual permission checks.
