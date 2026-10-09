# Changelog

## v0.13.1

<p class="release-meta"><time datetime="2026-10-09">October 9, 2026</time></p>

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

## v0.13.0

<p class="release-meta"><time datetime="2026-10-07">October 7, 2026</time></p>

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

## v0.12.0

<p class="release-meta"><time datetime="2026-10-06">October 6, 2026</time></p>

### Added

- **iTerm2 color schemes as themes.** Install a downloaded `.itermcolors` scheme into `~/.config/larik/themes/` and choose it as a theme in `/config` or `/theme <name>`. Its palette recolors the text, accents, syntax highlighting and Markdown, while the background stays transparent so your terminal's own background shows through.
- **A menu on every prompt.** Click a prompt to open a menu: **Copy** puts it on the clipboard, **Fork** branches into a new session that keeps the turn and its reply, and **Rewind** branches off just before it. `/prompt [n]` opens the same menu from the keyboard.
- **OpenTelemetry export.** Send each session's traces to an OTLP/HTTP collector (Grafana, Jaeger, Honeycomb, …) from personal settings (`telemetry.otlp_endpoint`) or the `LARIK_OTLP_ENDPOINT` environment variable. Metadata only — prompts, replies, tool input, file contents and paths are never exported. Off until an endpoint is given; Larik writes OTLP/JSON itself with no SDK in the binary, and exporting is best-effort so it never slows the agent.

### Changed

- **`/rewind` can restore files.** When files changed since the prompt you rewind to, it lists them and asks whether to put them back before branching; `files` restores without asking, `keep` leaves them untouched.
- **Prompts stand out more.** Your own prompts now sit in a rounded frame, and system notes are hidden from the history view.
- **MCP tools validate their input.** A tool call whose arguments don't match the server's schema is reported with a clear error instead of failing later.

### Hardened

- **More robust sessions and sandbox.** A loop guard and decision log catch repeated actions, provider secrets stay protected in the sandbox, and a per-turn token budget bounds a single request, so a wayward turn degrades visibly rather than stalling.

### Documentation

- **A "tests must pass" Stop hook.** A ready-to-use recipe (`docs/examples/require-tests.sh`) shows how to end a turn only when the test suite is green.

## v0.11.0

<p class="release-meta"><time datetime="2026-10-06">October 6, 2026</time></p>

### Added

- **Configure Larik without editing JSON.** `/config` now searches every setting and opens native editors for display commands, keybindings, permission rules, sandbox access, provider endpoints, model overrides, LSP servers, lifecycle hooks and MCP servers. Editors validate entries, distinguish personal from shared settings, preview or test integrations where appropriate, and apply the same safety rule as config loading: shared project files may tighten access but never widen it.
- **Compaction telemetry and a retention benchmark.** Successful compactions report provider-grounded prompt size, estimated summary size and context saved in the TUI, headless output and server API. Session totals survive resume, and `larik bench --tasks compaction-retention` mechanically checks whether important facts remain usable after a forced compaction.

### Changed

- **Conversation layout follows the space it actually has.** Finished Markdown is re-rendered when the information sidebar changes the conversation width, prompts keep a consistent gutter and spacing, and Markdown tables now wrap inside a complete border instead of leaving open vertical rails.
- **Credential removal is deliberate.** Leaving a masked credential field empty preserves the existing secret; choosing the explicit clear action removes it, so editing another endpoint or MCP field cannot erase credentials accidentally.
- **Compaction anticipates the next request.** Larik estimates an upcoming prompt or tool result before sending it, caps summaries to the context they are meant to free, and keeps displayed token counts grounded in provider usage.

### Fixed

- **Malformed tool history no longer bricks a session.** Before any provider serializes history, Larik repairs a missing `tool_result` with an ordered synthetic error result. A rejected or interrupted call can therefore degrade visibly instead of causing every later request to fail with a structural 400.
- **Compaction recovery remains armed after failure.** Failed threshold compaction, tiny context windows, malformed summary tags and subagent attribution are handled without losing the turn or inflating the parent session's totals.
- **Configuration changes report and apply consistently.** Native editors use the same save/reload feedback, narrow terminals expose editor-backed settings through the command palette, and credential changes take effect without an unrelated model switch.

## v0.10.0

<p class="release-meta"><time datetime="2026-10-05">October 5, 2026</time></p>

### Added

- **Flexible model routing.** Choose `manual`, `balanced`, or `aggressive` delegation; route routine subagent work to cheaper models, configure fallbacks and a session budget, and see delegated usage and estimated savings in `/cost`.
- **Discover more models.** Catwalk catalog integration adds provider and model metadata to the connection flow; review an endpoint and supply credentials before connecting. Claude Code can list account-available models and supported effort levels when its CLI provides them.
- **Control browser tools in the TUI.** `/browser on`, `/browser off`, and `/browser` show or change browser-tool availability. `/reload` rebuilds app services without discarding the saved transcript; tool-list changes start in a fresh model context.

### Changed

- **Compaction says what it saved.** A compaction now reports its size, for example `26k prompt → ~10k summary · ~16k saved`, in the TUI, in `-p` output and over the API. The information sidebar keeps a running count and total for the session and the current turn, and both survive resuming. The summary figure is an estimate — the exact size of the rebuilt context is known only once the next request is measured. The self-checking benchmark now includes `compaction-retention`, which measures whether ten important facts survive a forced compaction and remain usable.
- **The context is sized before the request, not only after it.** Automatic compaction used to go by the provider's count for the *previous* request, so a very large pasted prompt, a long tool result or a freshly resumed session could overflow the window before anything noticed. Larik now also estimates what is about to be sent. Estimates decide only whether to compact; every token count shown still comes from the provider.
- **A summary can no longer outgrow the context it frees.** Its length is capped by the writing model's own output limit and by a share of the window being freed, not just a fixed 32k.
- **More useful recording feedback.** The composer gives way to an animated, decorative equalizer and stop-key hint while recording, then shows transcription progress before restoring the preserved draft. The bars are not a microphone-level meter.
- **Smoother TUI navigation.** The conversation ignores horizontal wheel scrolling, the context-usage indicator stays within 100%, and configuration changes that rebuild the prompt or services ask before clearing the active model context.
- **Less lookup overhead.** File search and session lookup avoid redundant work.

### Fixed

- **A failed compaction no longer costs you the turn.** When summarizing failed partway through a long turn, the context-overflow recovery that should have caught the oversized request was skipped and the turn ended with a provider error. The two are now tracked apart: a failed threshold compaction is reported once and recovery stays armed.
- **Compaction totals stay attributable.** `/cost` now includes estimated context freed by compaction. A subagent's compaction and failure messages are labelled in the conversation but do not inflate the main context's totals.
- **Compaction on a tiny context window.** When the system prompt and tool definitions fill the window on their own, Larik no longer summarizes repeatedly to no effect.
- **Malformed summaries.** An unclosed or repeated `<summary>` tag in the model's reply is handled deliberately instead of carrying the model's framing into the next context.
- **Usable compaction benchmarks.** `larik bench` accepts `main` for the configured primary model, and recovery grading accepts natural wording when all required facts and corrected numbers are retained.
- Chrome stays open after its initial browser startup check rather than closing when the probe context ends.
- The compact tool summary no longer prints each tool count twice.
- Server operation lifecycle, session usage reconstruction, and OpenAI-compatible provider error handling are more reliable.

## v0.9.0

<p class="release-meta"><time datetime="2026-10-03">October 3, 2026</time></p>

### Added

- **Local speech input and output.** Configure any OpenAI-compatible STT/TTS server for microphone transcription and assistant speech, with Qwen3-ASR and Qwen3-TTS documented as local options.
- **Indonesian and English STT selection.** Use `/stt-language id` or `/stt-language en` to choose the transcription language and save it as a personal default.
- **Interactive recording feedback.** `Ctrl+Space` starts and stops recording with an animated microphone indicator, a visible transcription hint, and a dedicated transcription status.

### Changed

- **STT responses are normalized.** OpenAI-compatible servers that return a JSON transcription object now contribute only its `text` field to the composer, while plain-text responses remain supported.
- **Audio configuration is personal-only.** Microphone recording, local playback, and audio endpoints are kept out of shared project configuration.

### Fixed

- **`Ctrl+Space` recording dispatch.** The configured `record_audio` key now reaches the recording handler instead of being silently ignored.

## v0.8.0

<p class="release-meta"><time datetime="2026-10-03">October 3, 2026</time></p>

### Added

- **Customize the TUI footer and sidebar.** Use personal `status_line` and `sidebar` commands to show project-specific information, Git state, or activity-aware text; the sidebar script receives session/activity data and refreshes while the panel is open. See [Custom sidebar](/#custom-sidebar) and [Extensibility](/docs/extensibility/#custom-tui-panels).

### Changed

- **The startup banner checks for new releases.** When a newer version is available, Larik shows a non-blocking update notice; offline or failed checks are ignored.
- **Long streaming responses render more efficiently.** The TUI wraps only the visible tail of a large in-flight response instead of re-wrapping the full response on every frame.
- **Unavailable browser environments fail cleanly.** Larik preflights Chrome or Chromium before registering browser tools, disables them with a warning when startup is unavailable, and browser integration tests skip unusable environments.
- **The `/info` sidebar now shows project and turn health.** See the current Git branch and changed-file summary, session tokens/cost/context, and per-turn model time, tool time, average TTFT and request steps. Account-level provider quota/reset balances are not yet exposed by the provider adapters.
- **The footer says how big the context window is.** `ctx` now reads `ctx ▰▰▰▱▱▱▱▱▱▱ 31% · 62k/200k`: the share of the model's context window in use, the tokens in it and the window's size. It shows from the start of a session instead of only after the first response, and the counts are the first thing dropped on a narrow terminal. See [Footer colors](/docs/commands/#footer-colors).
- **Your messages stand out from the model's.** A prompt of yours carries a teal bar down its left edge, on every line of a long one, so the conversation is easy to read back.
- **You can see which model each parallel agent runs on.** A running subagent's live row names its model, and the `/info` sidebar has an **Agents** section listing the subagents at work with their models (amber while one waits for your answer), or, when none is running, the routing a delegated task would take. The finished `task` card names the model too.

### Fixed

- **Sidebar rows no longer wrap.** A row whose label had to be shortened (a long MCP server name, a model spec in the new **Agents** section) was pushed onto a second line, which silently cost the sidebar one of its rows. Rows now always fit on one line, and a model spec too long for the column loses its provider prefix before its model name.

## v0.7.0

<p class="release-meta"><time datetime="2026-10-03">October 3, 2026</time></p>

### Added

- **`write_files`.** The model can create several independent files (2 to 16) in one call instead of one turn per file. Each file still goes through `write`'s permission check, hooks, checkpoint and diagnostics; if one fails, the tool stops and reports which files were already written.

### Changed

- **Faster tool calls under Claude Code.** With `claude-code-cli`, tools that are safe to run together now run in parallel. A call that writes still runs alone, and permission prompts still come one at a time in order. See [Providers](/docs/providers/#claude-code-cli-runtime).
- **Better prompt caching.** Requests through a ChatGPT sign-in now send a per-session cache key, so later requests read most of the prompt from cache instead of none of it. Anthropic requests add a cache breakpoint where the previous request ended, so turns with many tool calls stay cached.
- **Leaner tools.** `grep` and `glob` return paths relative to the working directory, stop at the result cap, and `glob` follows `.gitignore`. `read` returns a short reply when a file is read again unchanged. `bash` strips colour codes and progress redraws and turns pagers off. `todo_write` no longer echoes the list back. All of this means less context per turn.
- Auto mode checks a batch of tool calls concurrently; prompts still appear in order. Retries honour `Retry-After`. A long turn can compact more than once.
- The model keeps a task list only for work with distinct phases or several deliverables, not for a single fix.

### Fixed

- **Claude Code runtime had no tools on newer CLIs.** Claude Code 2.1.274's `--safe-mode` turns off the MCP server that serves Larik's tools. Larik now uses `--restricted` and falls back to `--safe-mode` on older CLIs.

### Security

- Updated grpc and x/crypto to fix reachable vulnerabilities.

## v0.6.0

<p class="release-meta"><time datetime="2026-10-02">October 2, 2026</time></p>

### Added

- **Scripts that call tools.** A new execution setting decides how the model carries out its work. `tools` is one tool call per step, as before and still the default. `hybrid` adds `run_code`, and the model picks per step: a short JavaScript script for loops, chained lookups and filtering large output, a direct call otherwise. `code` gives the model `run_code` only. A script calls tools (`tools.read({path})`, `tools.grep({...})`) and only what it prints returns to the model, so reading forty files to count something costs the context one summary. Every call a script makes goes through the same permission rules, mode, hooks, auto mode and `/undo` checkpoints as a direct call; in plan mode a script can read but not write. See [Execution](/docs/execution/).
- **Execution per model.** A strong model writes reliable scripts and a small one may not, so `"model_execution": {"codex/gpt-6-luna": "code"}` sets it per model, with `"execution"` as the default for the rest. `/execution` opens a picker for the model you're using; `/execution default` removes its setting. Switching models switches the setting, and subagents use their own model's.
- **Benchmarking execution settings.** `larik bench --execution tools,hybrid,code` runs each setting side by side, `--runs 3` repeats each task and reports medians and ranges, and `--keep-failed` keeps a failed run's directory with its transcript and every tool call, scripts' included. A new task (finding undocumented functions across packages) is the kind of work where scripts should help.

### Changed

- **Larik checks that the sandbox can start.** At startup Larik runs a command in the sandbox. If that fails (most often bubblewrap inside a Docker container), Larik runs without the sandbox, so commands ask for approval, and says why and how to fix it at startup and in `/sandbox`. Before, every sandboxed command failed. See [Sandbox](/docs/sandbox/) and [Running Larik in a container](https://github.com/vwibowo/larik/blob/main/docs/security.md#running-larik-in-a-container).

### Security

- Scripts run in a separate process started from the Larik binary, with no file, network or process access of their own. With the sandbox on, that process is confined more strictly than bash commands: no writes, no network (not even localhost), no API keys in its environment, and no reading home or temp directories.
- Each script is limited to 200 tool calls, 256 MB of memory, a call depth of 10,000 and a timeout (120 seconds by default, up to 600). A script that runs out of memory is stopped and the model is told why; Larik keeps running.

## v0.5.0

<p class="release-meta"><time datetime="2026-10-01">October 1, 2026</time></p>

### Added

- **Claude Code CLI provider.** Use a Claude subscription through the official `claude` CLI with `/connect claude-code-cli` or `claude-code-cli/<model>`. Claude Code runs the whole agent turn while Larik keeps tool permissions, hooks and execution in its own pipeline. Rolling model aliases and effort levels are offered in the picker; full model IDs are accepted too. See [Providers](/docs/providers/#claude-code-cli-runtime).

## v0.4.0

<p class="release-meta"><time datetime="2026-09-30">September 30, 2026</time></p>

### Added

- **Browser: frames, long pages and the network.** Snapshots now include what is inside same-site iframes, and you can click and type there. A long page is read in parts, or one form, table or frame at a time. `browser_network` lists the requests a page made, with status and failures, and returns a response's body.
- **A GitHub Action for reviews.** `uses: vwibowo/larik@v0.4.0` runs `/review` or `/security-review` on each pull request in plan mode and posts the findings as one comment, updated on every push. See [Reviewing changes](/docs/skills/#reviewing-changes).
- **Tools on demand.** With many MCP tools connected (over 30, or about 6,000 tokens of definitions), Larik no longer sends every definition with every request. The model finds a tool with `tool_search` and runs it with `call_tool`; permission rules, prompts and hooks apply to the real tool. The request's tool list stays fixed, so prompt caching is unaffected. `"tool_search": "on"` or `"off"` overrides the automatic choice. See [MCP](/docs/mcp/).
- **Memory.** Larik now remembers things between sessions as short notes: your preferences, corrections you've given, project decisions, and where things live. It saves one when it learns something a later session needs, or when you say "remember…". Notes are Markdown files outside your repository, per project or for every project; `/memory` lists, shows, adds and deletes them. Switch it off with `"memory": {"enabled": false}`. See [Memory](/docs/memory/).
- **`/review` and `/security-review`.** Built-in commands that review your uncommitted changes, the current branch, a pull request (`/review 123`) or a range, and report findings with file, line, failure scenario and fix, without editing anything. Larik gathers the diff itself, so they work in every mode, including plan, and with `larik -p`. Your own `review.md` replaces the built-in. See [Reviewing changes](/docs/skills/#reviewing-changes).
- **Auto mode.** A new permission mode between `accept-edits` and `yolo`: project edits run freely, and every other call that would ask is first checked by a model against your recent prompts. Routine work (builds, tests, package installs, local git) runs; destructive or outward-facing actions (`git push`, deleting data, publishing, `sudo`, handling secrets) still ask, with the reason shown, unless your prompt explicitly asked for them. Pick it with `/mode`, `shift+tab` or `--mode auto`. See [Permissions](/docs/permissions/).

### Changed

- **A model server that never answers no longer hangs the session.** If a provider sends nothing for 5 minutes (15 for local servers), Larik stops the request, says so, and switches to the fallback model when one is configured. Before, `larik -p` could wait forever, which would hang a CI job. Change the time with `stall_timeout`, globally or per provider.
- **`/undo` covers shell commands.** In a git repository, files a `bash` command modified, deleted or created are restored by `/undo`, along with the file tools' edits. A file you had already changed goes back to how you had it. Files git ignores aren't covered. See [Undo](/docs/commands/#undo).
- **Subagents browse in their own tabs.** Each agent has its own browser tabs in the one window, so subagents can use the browser at the same time without navigating each other's pages; their tabs close when they finish. Subagents get the browser tools by default again.

## v0.3.1

<p class="release-meta"><time datetime="2026-09-30">September 30, 2026</time></p>

### Added

- **Sandbox network allowlist.** `"sandbox": {"allowed_domains": ["golang.org", "npmjs.org"]}` in your personal config lets sandboxed commands reach just those domains (and their subdomains) through a local proxy, so `go get`, `npm install` or `pip install` can run in the sandbox without opening the whole network. Refused hosts are named in the command's result. See [Sandbox](/docs/sandbox/).
- **Browser uploads, downloads and waiting.** `browser_upload` chooses files for a file input or the chooser a button opens (file dialogs never open on screen); downloads are saved to `~/.local/share/larik/browser-downloads` and the model is told where; `browser_wait_for` waits for text to appear or disappear. See [Browser](/docs/web/#browser).

### Changed

- The browser checks every request a page makes, not just the URL it opens, so a redirect, an image or a clicked link can't reach a cloud metadata address.
- After a click or keypress the browser waits for the page to stop changing before reading it, so results from single-page apps and slow scripts show up.
- Subagents get the browser only when their definition lists `browser`, since all agents share one window.

### Fixed

- **Linux sandbox:** a sandboxed command could create protected project files that didn't exist yet (`.mcp.json`, `.claude/`, `.larik/`, `.git/commondir`, `.git/config.worktree`, `.git/modules`), which on macOS was already blocked. A planted `.git/commondir` could point git at another directory's hooks. These paths are now read-only in the sandbox whether or not they exist.
- A click on an element covered by a cookie banner or dialog now fails and names what's in the way, instead of clicking the banner.
- Clicks on pages with smooth scrolling landed in the wrong place.
- Leaving a page that asks "leave this page?" hung until the timeout.
- Pages that never finish loading (a hanging request) no longer make `browser_navigate` fail; it returns what has loaded.
- A second larik session couldn't start the browser while the first had it open; it now uses a temporary profile and says so.
- Console messages showed JSON escapes (`\n`, `\"`) instead of the text.

## v0.3.0

<p class="release-meta"><time datetime="2026-09-30">September 30, 2026</time></p>

### Added

- **Browser control.** With `"browser": {"enabled": true}` in your personal config, the model can drive a real Chrome window: open pages, read a snapshot of their text, links and fields, click, type, pick options, press keys, switch tabs, run JavaScript and read the console. Opening a page asks per domain, like `web_fetch`. Chrome keeps its own profile, so sign-ins last between sessions. `browser_screenshot` lets the model see the page (the viewport, the full page or one element), optionally with each element's ref drawn on it. See [Browser](/docs/web/#browser).
- **Images from tools.** Tool results can now carry images to the model, on every provider: browser screenshots, and images returned by MCP tools (such as Playwright MCP's screenshots), which used to be replaced by a note.
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
- **Token saver** filters recognized successful shell output before it enters model context. `/config token_saver=true` enables it; `raw_output` retrieves the exact private session copy without rerunning a command. See [Tools and permissions](https://github.com/vwibowo/larik/blob/main/docs/tools-and-permissions.md).

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
- **Internals docs** with diagrams, for contributors. See [Contributor docs](https://github.com/vwibowo/larik/blob/main/docs/README.md).

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
