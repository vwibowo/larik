# State and persistence

Larik keeps several kinds of state on disk. All of it is under `~/.local/share/larik` (or `$XDG_DATA_HOME/larik`), except config.

```
~/.local/share/larik/
├── sessions/<cwd-slug>/
│   ├── 20260927-101500-a1b2c3.jsonl          one session (or branch)
│   ├── 20260927-101500-a1b2c3-agents/        subagent transcripts for that session
│   │   └── 20260927-101733-d4e5f6.jsonl
│   └── 20260927-101500-a1b2c3.trace/         debug mode only: events.jsonl + http/ bodies
├── checkpoints/<session-id>/<turn>/
│   ├── manifest.json                         which files, whether they existed, mode
│   └── 0.bin, 1.bin, …                       original bytes
├── worktrees/<repo>/task-xxxxxx/             isolated subagent checkouts
├── memory/<root-slug>/<name>.md              notes kept across sessions, per project
├── memory/_user/<name>.md                    notes that apply to every project
├── browser-profile/, browser-downloads/      the browser tools' Chrome profile and downloads
└── logs/                                     mcp-<name>.log, lsp-<name>.log
```

## Sessions: append-only JSONL

[internal/session/session.go](../internal/session/session.go). One file per session, one JSON object per line. Nothing is ever rewritten.

```mermaid
classDiagram
    class Entry {
        ID string
        ParentID string
        Type meta, message, compaction, usage, task, decision, recovery
        Time
        Message *llm.Message
        Usage *llm.Usage
        Summary string
        Model string
        Meta *Meta
    }
    class Meta {
        Cwd, Provider, Model
        ForkOf string
        ForkAt int
    }
    class State {
        Meta
        Messages []Message
        All []Message
        Usage, Cost
    }
    Entry --> Meta
    State <.. Entry : replayed into
```

| Entry type | Written when | Effect on replay |
|---|---|---|
| `meta` | Session created (first line) | Cwd, provider/model, fork origin |
| `message` | Every user or assistant message, with usage on assistant messages | Appended to both `Messages` (the context) and `All` (display history) |
| `compaction` | After a successful compaction | `Messages` is reset to the single summary message, which names this file so the model can grep it; `All` keeps everything; optional input/summary/saved-token metrics restore session compression totals |
| `usage` | Spend made on this session's behalf outside its transcript (subagents) | Added to usage and cost |
| `decision` | A tool call was authorized or refused (rule, hook, auto mode or user), except reads allowed by rules or mode | Ignored by replay, and not copied into a branch (a fork starts with a fresh audit trail) |
| `recovery` | Resuming after a torn final line | Marks the preceding incomplete line as recoverable; has no effect on messages |

Every entry has an `id` and the `parent_id` of the previous entry, so the file is also a linked list. `read` tolerates a torn last line after a crash. Opening it for appending adds a `recovery` entry after the incomplete line; other malformed lines cause loading and forking to fail rather than silently dropping history.

**Why append-only?** Three reasons: a crash can't corrupt earlier history; provider prompt caches and thinking-block rules depend on earlier messages never changing; and forking, resuming and inspecting the transcript all read the same simple file. Operations that feel like edits are expressed as new entries: compaction adds a summary, `/undo` adds a note to the next message, `/clear` just starts a fresh in-memory context while the file continues.

## Branches: fork and rewind

A branch is a new file. `session.Fork(dir, src, keep)` copies the first `keep` messages (and any compaction entries among them) into a new session whose meta records `fork_of` and `fork_at`. Compaction summaries are copied so the active context is identical, but their accounting fields are omitted just like prior usage, so the branch starts with fresh totals. The source is never touched.

`keep` must fall on a **turn boundary**: the end of the transcript, or the index of a prompt (`IsPrompt`: a user message with text and no tool results). That guarantees a branch never ends with a `tool_use` missing its `tool_result`.

```mermaid
flowchart LR
    subgraph A["session A (original, untouched)"]
        a1["P1"] --> a2["R1"] --> a3["P2"] --> a4["R2"] --> a5["P3"] --> a6["R3"]
    end
    subgraph B["session B: /fork (fork_of=A, fork_at=6)"]
        b1["P1…R3 copied"] --> b2["P4"] --> b3["R4"]
    end
    subgraph C["session C: /rewind 2 (fork_of=A, fork_at=2)"]
        c1["P1, R1 copied"] --> c2["P2' (edited)"] --> c3["R2'"]
    end
    a6 -.-> b1
    a2 -.-> c1
```

- `/fork` and `larik -c --fork`: branch at the end and continue there.
- `/rewind n`: branch just before prompt `n` and put that prompt back in the input box to edit.
- Usage is not copied: each branch reports only its own spend.
- Rewinding changes only the conversation. Files are reverted separately with `/undo`.

## Checkpoints and /undo

[internal/checkpoint/checkpoint.go](../internal/checkpoint/checkpoint.go). Each user prompt starts a turn (`BeginTurn`). The first time any tool is about to write a path during that turn, `tools.Env.BeforeWrite` calls `Capture`, which saves the file's bytes and mode (or records that it didn't exist) and writes a manifest. Later writes to the same path in the same turn are not captured again, so the snapshot is always the state before the turn.

```mermaid
stateDiagram-v2
    [*] --> TurnOpen: BeginTurn (prompt sent)
    TurnOpen --> TurnOpen: Capture(path), first write per path saves original
    TurnOpen --> TurnOpen: later writes to same path, no-op
    TurnOpen --> TurnOpen: BeginTurn (next prompt) pushes a new turn
    TurnOpen --> Restoring: /undo
    Restoring --> TurnOpen: pop latest turn with files, restore in reverse order,<br/>delete files that did not exist, drop its snapshot dir
    Restoring --> TurnOpen: agent queues a note: "user reverted … re-read these files"
```

`Undo` skips turns that changed no files, so it always reverts the most recent turn that did something. Restores are confined to the project root; symlink paths and protected project files are rejected. Each restored file replaces its directory entry rather than following a changed symlink, and restores its original permissions. Manifests are saved through a temporary file and atomic rename. Afterwards the agent queues a `<system-note>` for the next message telling the model which files were reverted, so it re-reads them instead of trusting stale content.

**Across runs.** Each turn's manifest and blobs are on disk under `checkpoints/<session-id>/<n>/`, so `checkpoint.New` reloads them when a session is resumed (`larik -c`, `--resume`, `/resume`), and `/undo` keeps working. New turns are numbered after the last saved one. A branch (`/fork`, `/rewind`) is a new session id, so it starts with no undo history.

**Retention.** At startup `app.Setup` calls `checkpoint.Prune`, which deletes turns whose snapshots are older than `checkpoint_retention_days` (default 7; negative keeps them forever), and session directories left empty. The cleanup covers every project, so the setting is honored only from personal files.

Scope: the store covers `write`, `edit` and `multi_edit` (and subagents sharing the store) through `Capture`, and shell commands in a git repository through `Record` (below). Files git ignores can be listed in a `bash` call's `checkpoint_paths` to snapshot them before execution; other changes to ignored files, and changes made by MCP tools, are not captured. Worktree subagents skip checkpoints entirely; their branch is the undo. A failed capture stops the command, and a failed restore keeps the snapshot for another try.

**Shell commands** ([shelltrack.go](../internal/tools/shelltrack.go)). A command doesn't say which files it will change, so they can't be captured before the write. Instead the bash tool asks git before and after:

- Before: `git status --porcelain -z --untracked-files=all` lists every file that differs from `HEAD` or is untracked. Those have no copy in git, so their bytes are read now (up to 2 MB each, 64 MB in all; with more than 2,000 such files tracking is skipped). `HEAD` is noted.
- After: the same listing. A file from the first list whose bytes or existence changed is recorded with the copy taken. A file only in the second list was clean before: if it is untracked it was created (recorded as "didn't exist"), otherwise its original is `git cat-file blob <HEAD before>:<path>`, with the mode from `ls-tree`.
- `Store.Record` takes that original the way `Capture` would have, keeping the first state seen for a path in the turn, so a file the edit tool changed earlier keeps its older original. `restoreFile` recreates a directory the command removed.
- The git commands run directly (no shell) with `GIT_OPTIONAL_LOCKS=0` and `core.fsmonitor=false`: they take no locks, write nothing, and run no program the repository configures. Nothing is staged, stashed or committed.
- Not seen: ignored files; clean files changed by moving `HEAD` (they are clean relative to the new commit); and changes by anything else during the command, which are attributed to it.

## Configuration layers

[internal/config/config.go](../internal/config/config.go). Four files are merged, later winning for scalars, with rules accumulating:

```mermaid
flowchart LR
    U["~/.config/larik/config.json<br/>personal · trusted"] --> M["merged Config"]
    MCP[".mcp.json<br/>shared · untrusted"] --> M
    S[".larik/settings.json<br/>shared · untrusted"] --> M
    L["~/.config/larik/projects/&lt;project-id&gt;.json<br/>personal · trusted"] --> M
```

Each layer is marked **trusted** (personal: only you write it) or not (shared: anyone with commit access to the repo can). The merge treats them differently:

| Setting | From a trusted file | From a shared file |
|---|---|---|
| Permission rules | allow and deny rules accumulate | deny rules only; allow rules ignored |
| Model, provider endpoints, mode and routing | honored | ignored |
| Turn and spend limits | may set any value | may only lower existing limits |
| MCP servers | start automatically | wait for `/mcp approve`, pinned to a hash of the config |
| Hooks | run | wait for `/hooks approve`, pinned to a hash of the hook set |
| Sandbox | may disable, enable network, add writable paths | may only switch it on |
| LSP servers | may add commands | may only disable |
| Web search backend | honored | ignored (it would receive every query); may only disable web tools |
| Debug recording and trace retention | honored | ignored (traces hold prompts and file contents) |
| Status line and sidebar commands, key bindings and editor mode | honored | ignored (the TUI commands execute; the others are how you type) |
| Approvals themselves | read from private project settings only | ignored |

The approvals are pinned to a content hash (`MCPServer.Hash`, `hooks.Config.Hash`), so a later commit that changes the command or URL silently loses its approval instead of inheriting it. See [security](security.md#trust-boundaries).

"Always allow" answers and approvals are written to private project settings under `~/.config/larik/projects/` with `updateJSON`, which edits the file as generic JSON so unknown keys survive. A private `.lock` sidecar serializes read, edit, and atomic write across Larik processes, so concurrent updates do not overwrite each other. `/config` writes user-wide settings to `~/.config/larik/config.json` the same way. Larik does not load the repository's `.larik/settings.local.json`.

## Memory

[internal/memory](../internal/memory/memory.go) keeps notes that carry across sessions. A note is `<name>.md` with YAML frontmatter (`name`, `description`, `type`) and a body of at most 8 KB; a file without frontmatter is a note described by its first line. There is no separate index file: `Store.List` scans the two directories, the project's (`memory.Dir`, keyed on the git root the way `session.Dir` keys on cwd) and the user's, so hand edits can't leave an index stale.

- **Prompt.** `App.SystemPrompt` appends `Store.Prompt()`: the guidance on what to save, then each note's name, type and description. Like the instruction files it is read once per fresh context, so saving a note never changes the cached prefix mid-conversation. Bodies stay out of the prompt; the model loads one with the tool.
- **Tool.** `memory` (save, read, list, delete) writes through `Store.Save`, which validates the name (lowercase, digits, hyphens: also a safe file name), type and size, caps a scope at 150 notes, and replaces the file atomically with mode `0600`. `permission.Decide` allows the tool in every mode after deny rules, since it can't touch the project. Subagents don't get it (`childTools`).
- **`/memory add`** saves directly, with a name slugged from the text, and queues a system note (`Agent.AddNote`) so the model knows before the next fresh context.
- **Trust.** Notes are presented as background that may be stale, inside a `<memory-note>` tag when read. The guidance and the tool description both say to save only what the user said or the model verified, never what tool output asks to be remembered, which is the path a prompt injection would take to persist.

## Instruction files

Not config, but loaded at the same time: `~/.config/larik/AGENTS.md`, then `AGENTS.md` (or `CLAUDE.md` if there's no `AGENTS.md`) in each directory from the git root down to cwd. They go into the system prompt inside `<instructions source="…">` tags, general first ([prompt.go:67](../internal/agent/prompt.go:67)).
