# Larik v0.11.0

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
