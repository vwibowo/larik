# State and persistence

Larik keeps four kinds of state on disk. All of it is under `~/.local/share/larik` (or `$XDG_DATA_HOME/larik`), except config.

```
~/.local/share/larik/
├── sessions/<cwd-slug>/
│   ├── 20260927-101500-a1b2c3.jsonl          one session (or branch)
│   └── 20260927-101500-a1b2c3-agents/        subagent transcripts for that session
│       └── 20260927-101733-d4e5f6.jsonl
├── checkpoints/<session-id>/<turn>/
│   ├── manifest.json                         which files, whether they existed, mode
│   └── 0.bin, 1.bin, …                       original bytes
├── worktrees/<repo>/task-xxxxxx/             isolated subagent checkouts
└── logs/                                     mcp-<name>.log, lsp-<name>.log
```

## Sessions: append-only JSONL

[internal/session/session.go](../internal/session/session.go). One file per session, one JSON object per line. Nothing is ever rewritten.

```mermaid
classDiagram
    class Entry {
        ID string
        ParentID string
        Type meta, message, compaction, usage
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
| `compaction` | After a successful compaction | `Messages` is reset to the single summary message; `All` keeps everything |
| `usage` | Spend made on this session's behalf outside its transcript (subagents) | Added to usage and cost |

Every entry has an `id` and the `parent_id` of the previous entry, so the file is also a linked list. `read` tolerates a torn last line after a crash.

**Why append-only?** Three reasons: a crash can't corrupt earlier history; provider prompt caches and thinking-block rules depend on earlier messages never changing; and forking, resuming and inspecting the transcript all read the same simple file. Operations that feel like edits are expressed as new entries: compaction adds a summary, `/undo` adds a note to the next message, `/clear` just starts a fresh in-memory context while the file continues.

## Branches: fork and rewind

A branch is a new file. `session.Fork(dir, src, keep)` copies the first `keep` messages (and any compaction entries among them) into a new session whose meta records `fork_of` and `fork_at`. The source is never touched.

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

`Undo` skips turns that changed no files, so it always reverts the most recent turn that did something. Afterwards the agent queues a `<system-note>` for the next message telling the model which files were reverted, so it re-reads them instead of trusting stale content.

Scope: the store covers `write` and `edit` (and subagents sharing the store). It does not capture changes made by `bash` commands. Worktree subagents skip checkpoints entirely; their branch is the undo.

## Configuration layers

[internal/config/config.go](../internal/config/config.go). Four files are merged, later winning for scalars, with rules accumulating:

```mermaid
flowchart LR
    U["~/.config/larik/config.json<br/>personal · trusted"] --> M["merged Config"]
    MCP[".mcp.json<br/>shared · untrusted"] --> M
    S[".larik/settings.json<br/>shared · untrusted"] --> M
    L[".larik/settings.local.json<br/>personal · trusted"] --> M
```

Each layer is marked **trusted** (personal: only you write it) or not (shared: anyone with commit access to the repo can). The merge treats them differently:

| Setting | From a trusted file | From a shared file |
|---|---|---|
| Permission rules | accumulate | accumulate |
| MCP servers | start automatically | wait for `/mcp approve`, pinned to a hash of the config |
| Hooks | run | wait for `/hooks approve`, pinned to a hash of the hook set |
| Sandbox | may disable, enable network, add writable paths | may only switch it on |
| LSP servers | may add commands | may only disable |
| Web search backend | honored | ignored (it would receive every query); may only disable web tools |
| Approvals themselves | read from `settings.local.json` only | ignored |

The approvals are pinned to a content hash (`MCPServer.Hash`, `hooks.Config.Hash`), so a later commit that changes the command or URL silently loses its approval instead of inheriting it. See [security](security.md#trust-boundaries).

"Always allow" answers and approvals are written to `.larik/settings.local.json` with `updateJSON`, which edits the file as generic JSON so unknown keys survive. `/config` writes personal settings to `~/.config/larik/config.json` the same way, changing only the key you edited.

## Instruction files

Not config, but loaded at the same time: `~/.config/larik/AGENTS.md`, then `AGENTS.md` (or `CLAUDE.md` if there's no `AGENTS.md`) in each directory from the git root down to cwd. They go into the system prompt inside `<instructions source="…">` tags, general first ([prompt.go:67](../internal/agent/prompt.go:67)).
