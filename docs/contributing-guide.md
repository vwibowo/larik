# Contributing guide

Recipes for the most common changes, and how the code is tested. Read [architecture](architecture.md) and [the agent loop](agent-loop.md) first.

## Build and test

```bash
go build -o larik ./cmd/larik
```

```bash
go test ./...
```

## Prepare a GitHub release

The release script packages binaries locally. It does not create a tag, push code, or publish a GitHub Release. The latest published version is `v0.13.1`; `v0.14.0` is prepared locally and remains unpublished until its assets are available. Keep website downloads on the latest published version until then.

1. Review and commit the changes intended for the release; the script refuses a dirty checkout. This repository keeps remotes and publishing under explicit manual control.
2. Run `scripts/package-release.sh vX.Y.Z`. It runs `go test ./...`, builds the website to check links, and creates five archives plus `SHA256SUMS` in `dist/release/vX.Y.Z/`. The archives cover macOS and Linux on arm64 and amd64, plus Windows on amd64. Check the archives and checksums before publishing.
3. After explicit confirmation, create and push the tag: `git tag -a vX.Y.Z -m "Larik vX.Y.Z"`, then push the intended branch and tag. Upload the files in `dist/release/vX.Y.Z/` to a GitHub Release (for example, with `gh release create vX.Y.Z dist/release/vX.Y.Z/* --verify-tag --generate-notes`).
4. After every asset is available, change `website/site.json` to `vX.Y.Z` and fill each download URL with `https://github.com/<owner>/<repo>/releases/download/vX.Y.Z/<file>`, using the owner and repository from the configured GitHub remote. Rebuild the website and commit the published download metadata. Until then, keep URLs pointed at the latest published version so visitors do not get broken downloads.

The script embeds `X.Y.Z` into each binary; a release binary prints `larik X.Y.Z` with `--version`.

No test needs an API key or network access:

- **The agent loop** is tested with `fakeProvider` in [agent_test.go](../internal/agent/agent_test.go), which replays scripted assistant messages and records every request, so tests can assert on exactly what was sent.
- **Provider adapters** are tested against canned SSE from [llmtest.NewServer](../internal/llm/llmtest/llmtest.go).
- **MCP and LSP** have tiny fake servers under `testdata/` ([mcp/testdata/server](../internal/mcp/testdata/server/main.go), [lsp/testdata/fakels](../internal/lsp/testdata/fakels/main.go)).
- **Server mode** tests drive the real HTTP handler with a scripted provider ([server_test.go](../internal/server/server_test.go)).
- **Subagents and worktrees** create real git repositories in temp dirs.

After changing code, refresh the knowledge graph used by the project's agent instructions:

```bash
graphify update .
```

## Add a built-in tool

1. Implement `tools.Tool` in `internal/tools` (or in its own package if it has dependencies, like `web` or `lsp`):

   ```go
   type Count struct{}

   func (Count) ReadOnly() bool { return true }

   func (Count) Spec() llm.ToolSpec {
       return llm.ToolSpec{
           Name:        "count",
           Description: "Count lines in a file.",
           Schema: schema(`{"type":"object","properties":{
               "path":{"type":"string"}},"required":["path"]}`),
       }
   }

   func (Count) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
       in, err := decode[struct{ Path string `json:"path"` }](input)
       if err != nil {
           return errorf("%v", err)
       }
       data, err := os.ReadFile(env.Abs(in.Path))
       if err != nil {
           return errorf("%v", err)
       }
       return Result{Content: strconv.Itoa(bytes.Count(data, []byte("\n")))}
   }
   ```

2. Register it: add it to `tools.Builtin()` ([tool.go:136](../internal/tools/tool.go:136)), or append it to `baseTools` in `app.Setup` ([app.go:54](../internal/app/app.go:54)) if it needs setup.

3. Decide its safety properties:
   - `ReadOnly() == true` only if it never changes anything. Read-only tools skip prompts and run in plan mode.
   - If it changes nothing locally but still needs approval (network), return `false` and implement `ConcurrencySafe() bool` to allow parallel runs.
   - If it takes a path or a command, make sure `permission.Subject` ([permission.go:132](../internal/permission/permission.go:132)) extracts it (field names `path`, `command` or `url`), so rules can match it.
   - If it writes files, call `env.checkFresh(path)` and `env.beforeWrite(path)` first, and return `env.afterWrite(ctx, path, res)` so undo and diagnostics work, as `Write` and `Edit` do in [fs.go](../internal/tools/fs.go).

4. Keep the description and schema stable. They're part of the cached prompt prefix.

5. Add a test in [tools_test.go](../internal/tools/tools_test.go).

## Add a provider

**If it speaks OpenAI Chat Completions,** you probably need no code: users can configure `"type": "openai-compatible"`, or you add a preset to `openaicompat.Presets` ([compat.go](../internal/llm/openaicompat/compat.go)) with its base URL and key variable.

**Otherwise, write an adapter:**

1. Create `internal/llm/<name>/` with a `Provider` implementing `Name()` and `Stream(ctx, llm.Request) iter.Seq2[llm.StreamEvent, error]`.
2. In `Stream`:
   - Build the vendor request from `req.System`, `req.Tools` and, for each message, `llm.ReplayableBlocks(m, Name, req.Model)`. Never send another provider's thinking or opaque blocks.
   - Yield `EventTextDelta`, `EventThinkingDelta` and `EventToolUseStart` for display as they arrive.
   - Accumulate the full response, convert it to an `llm.Message` with `Model: req.Model`, set `Provider: Name` (and `Signature` / `Raw`) on any block the vendor needs back, and yield exactly one `EventDone` with `Usage` and a `StopReason`.
   - Map errors: HTTP status → `llm.ClassifyStatus`; prompt-too-long → wrap `llm.ErrContextOverflow`.
   - Stop promptly when `yield` returns false or `ctx` is cancelled.
3. If the API has no built-in retries, wrap it with `llm.WithRetry(p, 4)` where it's built.
4. Wire it into `providers.build` and `isBuiltin` ([providers.go](../internal/providers/providers.go)), and add catalog entries in [catalog.go](../internal/llm/catalog.go) for its models (context window, max output, prices).
5. Optionally implement `llm.ModelProber` if the server can report its real context window or tool support.
6. Test against `llmtest.NewServer` with recorded SSE: streaming text, a tool call, thinking replay, an error status. Assert on `LastBody()` to check what was sent.

## Add a slash command

1. Add an entry to `commands` in [palette.go](../internal/tui/palette.go) (name, args, description, section). This feeds `/help` and the palette.
2. Handle it in `model.command` in [commands.go](../internal/tui/commands.go). If it would race with a running turn (it changes the model, the context or files), add it to the "unavailable while a turn is running" list.
3. Put the logic on `Agent` or `App` if the server should offer it too, then add a route in `Server.Handler` ([server.go](../internal/server/server.go)) that runs it through `live.idleDo`.
4. Document it in the README's command table.

## Add a hook event

1. Add the constant to [hooks.go](../internal/hooks/hooks.go) and to `hooks.Events` (lifecycle order).
2. Add any payload fields to `hooks.Input`.
3. Fire it from the agent with `a.runHook(ctx, emit, hooks.Input{HookEventName: …}, target)` at the right point in [agent.go](../internal/agent/agent.go) or [runtools.go](../internal/agent/runtools.go), and decide what `Block`, `Halt`, `Context` and `Permission` mean for it. Keep the Claude Code semantics if the event exists there.
4. Test it in [hooks_test.go](../internal/agent/hooks_test.go) with a small shell script.

## Add a front end

Consume `Agent.Run` and `Agent.Background()` and answer every `EvPermission` exactly once. Nothing in the loop needs to change. [headless/print.go](../internal/headless/print.go) is the smallest complete example, including how to wait for background tasks with `RunNotifications`.

## Invariants to keep

These are easy to break by accident and expensive to debug:

- **Don't change the prompt prefix mid-context.** System prompt, tool list and earlier messages must stay byte-identical between requests. New tools, prompt text or settings that affect the prompt apply from the next fresh context: build them in `App.SystemPrompt` or `App.loadTools`, which run at session start and on `Clear`.
- **Keep fixed prompt text within its budget.** The base prompt, the plan-mode note, the memory guidance and the built-in tool definitions are sent with every request, so `TestPromptSizeBudget`, `TestGuidanceSizeBudget`, `TestToolDefinitionBudget` and `TestTaskDescriptionBudget` cap their size. Say each thing once: in the system prompt or in a tool's description, not both. If new text needs the room, raise the limit in the same commit and say why. Text that changes from run to run (paths, IDs) doesn't belong in the system prompt: it ends the cached prefix at that point.
- **Don't rewrite the transcript.** Append entries; express corrections as notes on the next user message.
- **Every `tool_use` gets exactly one `tool_result`**, in order, even on error or interrupt.
- **Branch only at turn boundaries** (`session.IsPrompt`).
- **Shared config may only tighten.** Anything that widens access must be read only from trusted files in `config.merge`.
- **The loop stays UI-agnostic.** No front-end imports in `internal/agent`; new information travels as an `Event`.
- **Never hold `Agent.mu` across I/O** (model requests, tool runs, hooks).
