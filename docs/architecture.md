# Architecture

Larik is one Go binary, `cmd/larik`, built from about 20 internal packages. This page covers how those packages depend on each other, the core types, how the process starts, and the rule that keeps the loop independent of the UI.

## Package dependencies

Dependencies point one way: from the entry point, through `app`, into `agent`, and down to the leaf packages. `agent` imports no front end. `llm` imports no vendor SDK; only its subpackages do.

```mermaid
flowchart TD
    main["cmd/larik"] --> tui & headless & server & app & trace
    tui["tui"] --> app & agent & trace & clipboard & audio
    headless["headless"] --> agent
    audio["audio"] --> config-independent local HTTP + OS audio commands
    server["server"] --> app & agent
    app["app"] --> agent & config & providers & subagent & mcp & lsp & skills & sandbox & web & session & checkpoint & hooks & trace
    subagent["subagent"] --> agent & worktree
    agent["agent"] --> llm & tools & permission & hooks & session & checkpoint & skills & lsp & trace
    providers["providers"] --> anthropic & openai & gemini & ollama & openaicompat & chatgpt
    anthropic["llm/anthropic"] --> llm
    openai["llm/openai"] --> llm
    gemini["llm/gemini"] --> llm
    ollama["llm/ollama"] --> llm
    openaicompat["llm/openaicompat"] --> llm
    openai & openaicompat --> openaisdk["llm/openaisdk: shared SDK plumbing"]
    openaisdk --> llm
    mcp["mcp"] --> tools & config
    lsp["lsp"] --> tools
    skills["skills"] --> tools
    web["web"] --> tools
    tools["tools"] --> llm
    session["session"] --> llm
    trace["trace"] --> llm
    config["config"] --> permission & hooks & sandbox & lsp & web
```

## Domain ownership and folder structure

Larik keeps its core independent of the delivery mechanism. The folders below
are package boundaries, not layers that must be copied into every feature. Keep
an abstraction in its owning domain until two real implementations need to share
it; `internal/app` composes the dependencies and is not imported by the core.

```text
cmd/larik/                         CLI entry points; selects a front end
internal/app/                      composition root and session ownership
internal/agent/                    core turns, events, tool authorization
internal/llm/                      provider-neutral messages and contracts
internal/llm/{anthropic,openai,openaicompat,gemini,ollama}/
                                   model protocol adapters
internal/llm/openaisdk/            shared OpenAI SDK error conversion
internal/session/                  append-only transcript and branching
internal/{permission,checkpoint}/ authorization and undo policy
internal/{tools,subagent,worktree}/ execution capabilities
internal/{config,sandbox,mcp,lsp,web,...}/ infrastructure adapters
internal/{tui,headless,server}/    presentation adapters
```

Dependency rule: front ends and `app` may depend on the core; the core may
depend on narrow capability contracts but never on a front end, provider SDK,
or HTTP request type. The neutral `llm` package remains free of vendor SDKs.
`openaisdk` is an adapter-side helper rather than a dependency of `llm`.
Keep provider-specific error heuristics in each adapter. Avoid a new generic
`core` package or a repository-wide rename: moving packages without changing
ownership adds import churn without simplifying dependencies.

Session lookup reads directory entries without parsing transcripts; listing
still extracts titles and fork origins. The pure-Go grep fallback processes
files in bounded, parallel batches in walk order, so it can stop once the
output cap is reached without holding the whole path list. The TUI, headless,
and server front ends retain their existing event, permission and cancellation
contracts.

Two leaf packages sit beside the loop rather than in it:

- `trace` records debug-mode events (requests as sent, raw HTTP, responses, tool calls, permission answers, hooks) to `<session>.trace/events.jsonl`. The agent writes to it only when a tracer is set, and `trace/viewer` serves `larik trace`. See [debug mode](../README.md#debug-mode-and-traces).
- `clipboard` reads images from and writes text to the system clipboard, for `ctrl+v` image paste and `/copy`. Only the TUI uses it.

Two packages close cycles that Go would otherwise forbid, through small interfaces:

- `tools` defines `Sandbox` (`Command(script, dir) *exec.Cmd`), which `sandbox.Sandbox` satisfies. `tools` never imports `sandbox`.
- `subagent` imports `agent`, but `agent` does not import `subagent`. A running tool reaches its agent through the context: `agent.FromContext(ctx)` ([spawn.go:25](../internal/agent/spawn.go:25)).

## Core types

```mermaid
classDiagram
    direction LR
    class Agent {
        -opts Options
        -env *tools.Env
        -messages []llm.Message
        -usage llm.Usage
        -notes []string
        -bg *background
        +Run(ctx, prompt) chan Event
        +Compact(ctx) string
        +Undo() []string
        +Clear()
        +Spawn(SpawnOptions) *Agent
        +StartBackground(label, fn) string
        +RunNotifications(ctx) chan Event
        +Background() chan Event
    }
    class Options {
        Provider llm.Provider
        Model string
        System string
        Tools *tools.Registry
        Perms *permission.Checker
        Session *session.Session
        Checkpoints *checkpoint.Store
        Hooks *hooks.Runner
        LoadTools func
        Sandbox tools.Sandbox
        Subagent string
    }
    class Event {
        Kind EventKind
        Agent string
        Text string
        Message *llm.Message
        ToolID, ToolName string
        Input, Output string
        Reply chan PermissionReply
        Usage *UsageInfo
        StopReason string
    }
    class Provider {
        <<interface>>
        +Name() string
        +Stream(ctx, Request) iter.Seq2
    }
    class Message {
        Role
        Blocks []Block
        Model string
    }
    class Block {
        Type
        Text, Signature
        ID, Name, Input
        Content, IsError
        Provider, Raw
    }
    class Tool {
        <<interface>>
        +Spec() ToolSpec
        +ReadOnly() bool
        +Run(ctx, env, input) Result
    }
    class Env {
        Cwd string
        BeforeWrite func
        Diagnostics func
        Sandbox Sandbox
        -reads map
    }
    class Checker {
        mode Mode
        rules Rules
        sandboxed bool
        +Decide(Call) Decision
        +WithCwd(cwd) *Checker
    }
    class Session {
        ID, Path
        +AppendMessage()
        +AppendCompaction()
    }
    class Store {
        +BeginTurn()
        +Capture(path)
        +Undo()
    }
    Agent *-- Options
    Agent --> Event : emits
    Options --> Provider
    Options --> Checker
    Options --> Session
    Options --> Store
    Agent *-- Env
    Agent o-- "many" Message
    Message *-- "many" Block
    Options --> Tool : via Registry
    Tool ..> Env : runs in
```

Where they live:

| Type | File |
|---|---|
| `Agent`, `Options` | [internal/agent/agent.go](../internal/agent/agent.go) |
| `Event`, `PermissionReply`, `UsageInfo` | [internal/agent/event.go](../internal/agent/event.go) |
| `Provider`, `StreamEvent`, `ModelProber` | [internal/llm/provider.go](../internal/llm/provider.go) |
| `Message`, `Block`, `Request`, `Usage` | [internal/llm/types.go](../internal/llm/types.go) |
| `Tool`, `Env`, `Registry`, `Sandbox` | [internal/tools/tool.go](../internal/tools/tool.go) |
| `Checker`, `Mode`, `Rules` | [internal/permission/permission.go](../internal/permission/permission.go) |
| `Session`, `Entry`, `State` | [internal/session/session.go](../internal/session/session.go) |
| `Store` | [internal/checkpoint/checkpoint.go](../internal/checkpoint/checkpoint.go) |

## Startup

`main.run` ([main.go:37](../cmd/larik/main.go:37)) does four things:

1. Parse flags. `larik serve` branches off to `runServe` before flag parsing.
2. Load config. If nothing is configured and stdin/stdout are terminals, run the first-start wizard (`tui.RunSetup`).
3. `app.Setup(cwd)` builds the process-wide services once.
4. `app.Open(...)` creates or resumes a session and builds its `Agent`. Then hand off to `headless.Run` (for `-p`) or `tui.Run`.

```mermaid
sequenceDiagram
    autonumber
    participant M as main.run
    participant C as config.Load
    participant A as app.Setup
    participant O as app.Open
    participant F as tui.Run / headless.Run
    M->>C: layered settings for cwd
    C-->>M: *Config
    M->>A: Setup(cwd, version)
    Note over A: skills.Discover<br/>tools.Builtin()<br/>sandbox.New<br/>BuildSystemPrompt (+ sandbox summary, skills index)<br/>web tools, lsp.NewManager<br/>subagent.Discover + task/task_wait/task_stop<br/>mcp.NewManager.Start() (connects in background)
    A-->>M: *App
    M->>O: Open(model, effort, mode, resume/continue/fork)
    Note over O: session.Create / Open / Fork<br/>providers.Resolve(model)<br/>hooks.NewRunner, permission.NewChecker<br/>checkpoint.New, agent.New(Options)<br/>Restore(state) when resuming
    O-->>M: *app.Session{Agent}
    M->>F: hand off
```

`App` holds what every session in a directory shares: MCP connections, language servers, the sandbox, skills, agent definitions and the tool list ([app.go:29](../internal/app/app.go:29)). `app.Session` holds what one conversation owns: its `Agent`, its hook runner, and its session file. `larik serve` keeps one `App` and many `Session`s.

The system prompt is built by `App.SystemPrompt` for each fresh context (a new session, or `Agent.Clear` through `Options.BuildSystem`) and never changes within one. It is `basePrompt` + an `<env>` block (cwd, platform, date, git root) + every `AGENTS.md`/`CLAUDE.md` from the repo root down to cwd + the sandbox summary + the skills index, rescanned each time ([prompt.go](../internal/agent/prompt.go)). Keeping it byte-stable is what lets provider prompt caches hit on every request.

## One event stream, three front ends

`Agent.Run(ctx, prompt)` returns `<-chan Event` and runs the turn in a goroutine ([agent.go:243](../internal/agent/agent.go:243)). The caller has exactly two duties:

1. Drain the channel until it closes (after `EvDone`).
2. Answer every `EvPermission` by sending one `PermissionReply` on `event.Reply`.

That contract is the only coupling between the loop and the UI. Each front end fulfils it differently:

| Front end | Renders events as | Answers permissions by |
|---|---|---|
| TUI ([tui/app.go](../internal/tui/app.go)) | Bubble Tea messages: streaming text, tool rows, a status bar | Showing a prompt; several queued when subagents ask at once |
| Headless ([headless/print.go](../internal/headless/print.go)) | Plain text, or one JSON object per line with `--output json` | Denying automatically (use `--mode` or allow rules up front) |
| Server ([server/live.go](../internal/server/live.go)) | SSE messages with a sequence number | Holding the reply channel until a client POSTs an answer |

```mermaid
flowchart LR
    AG["Agent.Run"] -->|"chan Event"| T["TUI"]
    AG -->|"chan Event"| H["headless"]
    AG -->|"chan Event"| S["server.live → bus → SSE"]
    BG["Agent.Background()"] -.->|"long-lived chan Event"| T & H & S
    T -->|"PermissionReply"| AG
    H -->|"auto-deny"| AG
    S -->|"POST /permissions/{id}"| AG
```

There is a second, long-lived channel: `Agent.Background()` ([background.go:66](../internal/agent/background.go:66)). A turn's channel closes when the turn ends, but background subagents keep running, so their events (and their permission requests) go to this channel instead. Every front end reads it for the agent's whole life.

Because the event schema is shared, `-p --output json` and the SSE stream carry the same objects, and a new front end needs no changes to the loop.

## Concurrency

- One turn at a time per `Agent`. The TUI and server refuse a new prompt while one runs (the server returns 409).
- `Agent.mu` guards messages, usage and options. It is never held across a model request or a tool run; `stream` copies the messages under the lock and releases it before streaming ([agent.go:381](../internal/agent/agent.go:381)).
- Tools run in parallel only when they are read-only or declare `ConcurrencySafe` ([tool.go:174](../internal/tools/tool.go:174)). See [tools](tools-and-permissions.md#execution-order).
- Hooks for one event run in parallel; their results are combined.
- Subagents run in their own goroutine inside the `task` tool call. Several `task` calls in one model turn run in parallel because `task` reports itself as read-only.
- Cancellation is a `context.Context` all the way down: Esc cancels the turn's context, which stops the stream, kills bash process groups, and interrupts subagents. Background tasks have their own context and survive Esc.
