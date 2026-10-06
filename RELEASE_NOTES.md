# Larik v0.12.0

_October 6, 2026_

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

- **A "tests must pass" Stop hook.** A ready-to-use recipe ([docs/examples/require-tests.sh](https://github.com/vwibowo/larik/blob/main/docs/examples/require-tests.sh)) shows how to end a turn only when the test suite is green.
