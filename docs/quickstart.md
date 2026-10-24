# Your first ten minutes with Larik

The [README](../README.md) covers every setting. This page is the short path.

## 1. Install and connect a model

```bash
curl -fsSL https://raw.githubusercontent.com/vwibowo/larik/main/scripts/get-larik.sh | sh
cd your-project
larik
```

With nothing configured, a setup wizard finds a running Ollama or LM Studio and any `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` or `GEMINI_API_KEY` in your environment. It tests the connection and saves the model you pick. Run `/connect` later to add another provider, and `/model` (alt+p) to switch.

## 2. Ask for something

Type a request and press enter. Larik reads, edits and runs commands to do it, and shows each step as it goes. While a command runs, its latest output appears under it.

- **Esc** interrupts. A prompt typed while Larik works waits its turn.
- **shift+tab** cycles permission modes: `default` asks before edits and commands, `accept-edits` allows edits, and `plan` only reads and plans. `/mode` lists them all.
- `@path` attaches a file, and `!command` runs a shell command yourself.

## 3. Undo and branch

- `/undo` reverts the file changes of the last turn that made any, including what shell commands changed in a git repository.
- `/rewind` branches off just before an earlier prompt to try it again.
- `/sessions` picks up an earlier session, and `larik -c` continues the last one.

## 4. Make it yours

- `/init` writes an `AGENTS.md` for the project, which Larik reads every session.
- `/config` searches every setting. `/config mcp`, `/config hooks`, `/config lsp` and `/config sandbox` open their editors.
- `/keys` lists shortcuts, and `/keybindings` changes them.

## 5. Let it use a browser

`/browser on` gives the model a headless Chrome. `/browser show` or **F3** opens it in a panel beside the conversation, where you can watch it work and click or type yourself (to sign in, for example); F3 moves the keyboard between the browser and Larik. It needs a terminal that shows kitty graphics (kitty, Ghostty, WezTerm, cmux or Konsole).

## When something is off

- `/cost` shows token use and cost, and `/info` shows the session sidebar.
- `/debug on` records requests and tool calls, and `/trace` opens them in Larik's browser panel, or your browser (`/trace external`).
