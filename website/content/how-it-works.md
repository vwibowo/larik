# How Larik works

Larik helps you work in a project from your terminal. You describe a task, and Larik asks your chosen model what to do next. The model can read files, suggest edits, run commands, or answer directly. Larik shows each step as it happens.

## You stay in control

Before a tool acts, Larik checks your [permission settings](/docs/permissions/). Commands normally run inside an [OS sandbox](/docs/sandbox/) that limits what they can access. You can review changes and use [undo](/docs/commands/#undo) to restore files changed in the last turn.

## Your work stays available

Larik saves conversations locally so you can resume them later. You can switch models, branch a conversation, or use a subagent for a separate task. The same core agent powers the terminal interface, one-shot commands, and the local server.

For setup and everyday use, start with [Getting started](/docs/getting-started/). Developers who want the package design and implementation details can read the [contributor docs](https://github.com/vwibowo/larik/blob/main/docs/README.md).
