# Larik v0.10.0

### Added

- **Flexible model routing.** Choose `manual`, `balanced`, or `aggressive` delegation; route routine subagent work to cheaper models, configure fallbacks and a session budget, and see delegated usage and estimated savings in `/cost`.
- **Discover more models.** Catwalk catalog integration adds provider and model metadata to the connection flow; review an endpoint and supply credentials before connecting. Claude Code can list account-available models and supported effort levels when its CLI provides them.
- **Control browser tools in the TUI.** `/browser on`, `/browser off`, and `/browser` show or change browser-tool availability. `/reload` rebuilds app services without discarding the saved transcript; tool-list changes start in a fresh model context.

### Changed

- **More useful recording feedback.** The composer gives way to an animated, decorative equalizer and stop-key hint while recording, then shows transcription progress before restoring the preserved draft. The bars are not a microphone-level meter.
- **Smoother TUI navigation.** The conversation ignores horizontal wheel scrolling, the context-usage indicator stays within 100%, and configuration changes that rebuild the prompt or services ask before clearing the active model context.
- **Less lookup overhead.** File search and session lookup avoid redundant work.

### Fixed

- Chrome stays open after its initial browser startup check rather than closing when the probe context ends.
- The compact tool summary no longer prints each tool count twice.
- Server operation lifecycle, session usage reconstruction, and OpenAI-compatible provider error handling are more reliable.
