package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/session"
	"larik/internal/skills"
	"larik/internal/tools"
	"larik/internal/worktree"
)

// ToolName is the name the model uses to delegate.
const ToolName = "task"

const childMaxTurns = 100

// Resolver turns a model spec ("provider/model" or bare id) into a provider.
type Resolver func(spec string) (llm.Provider, string, error)

// Role is a named model choice from the config ("worker", "explore", …).
type Role struct {
	Name string
	Spec string // provider/model; empty when the role inherits the main model
	Hint string // what it's for
	// Legacy marks an unmapped Claude alias (opus, sonnet, haiku), which
	// only makes sense when the main agent runs on Anthropic.
	Legacy bool
	// Price is a short price note such as "$1/$5 per M", if known.
	Price string
	// Isolation "worktree" runs subagents on this role in a git worktree
	// unless the task or definition says otherwise; MaxTurns caps them.
	Isolation string
	MaxTurns  int
	// Context "minimal" gives subagents on this role the smaller prompt
	// from MinimalContextFunc instead of ContextFunc: no global
	// instructions, no skills index. A small model is less likely to
	// wander off into something unrelated with less to read.
	Context string
}

// Tool is the task tool. It is ReadOnly because the call itself changes
// nothing: every tool call the child makes is permission-checked on its
// own. That also lets independent tasks run in parallel.
type Tool struct {
	Set     *Set
	Resolve Resolver
	// Roles lists the model roles. Specs are snapshotted when a fresh tool
	// registry is built, while execution resolves the current role mapping.
	// Roles with a Spec are offered to the model as the task's model input.
	Roles func() []Role
	// Policy is "manual", "balanced", or "aggressive". Like Roles, its
	// model-facing instructions are snapshotted at a fresh context boundary.
	Policy func() string
	// Context is appended to every child system prompt (env block and
	// project instructions, plus the skills index when available).
	// ContextFunc, if set, is used instead and read for each child, so
	// children see the current instruction files.
	Context     string
	ContextFunc func() string
	// MinimalContextFunc, if set, is used instead of ContextFunc for
	// roles with Context "minimal". Falls back to ContextFunc/Context
	// when unset.
	MinimalContextFunc func() string
	// SkillsIndex, if set, is appended to the full context of children
	// whose definition allows the skill tool; the others couldn't use it.
	SkillsIndex func() string

	// Repo is the git repository root; worktree isolation is offered only
	// when it is set. Worktrees are created under WorktreeRoot.
	Repo         string
	WorktreeRoot string
	// SandboxFor confines a worktree child's bash commands; nil (or a nil
	// result) when there is no sandbox.
	SandboxFor func(dir, gitDir string) tools.Sandbox
	// OnChildDone, if set, is told when a subagent has finished, with its
	// agent.Owner, so per-agent state (its browser tabs) can be released.
	OnChildDone func(owner string)
}

func (t *Tool) ReadOnly() bool { return true }

func (t *Tool) Spec() llm.ToolSpec {
	var names []string
	var b strings.Builder
	b.WriteString("Delegate a task to a subagent with its own fresh context window: broad searches, multi-step research, " +
		"or self-contained changes whose details you don't need to keep in your context. The subagent can't see this conversation: " +
		"give it a complete prompt with all needed context and say exactly what to report back; its final message is returned to you. " +
		"For independent work, call task several times in one turn to run subagents in parallel, but not for tasks that edit the same files. " +
		"With run_in_background the call returns a task id at once and the result arrives in a later message (task_wait blocks on it, task_stop cancels).")
	if t.Repo != "" {
		b.WriteString(" Set isolation to \"worktree\" to run it in its own git worktree, on a new branch from HEAD (uncommitted changes aren't included): " +
			"for parallel tasks that edit code, or to try a change without touching your working tree. Its changes come back as a branch to review and merge.")
	}
	offered := t.offeredRoles()
	if len(offered) > 0 {
		b.WriteString("\n\nSet model to run a subagent on another model, by role. Give well-specified, mechanical work " +
			"(searches, routine edits, tests, boilerplate) to a cheap role with a precise prompt; keep design decisions, " +
			"ambiguous debugging and final review for yourself or the smart role. Without model, the agent's own default applies.")
		switch t.policy() {
		case "balanced":
			b.WriteString("\n\nDelegation policy: balanced. Delegate broad exploration and well-specified multi-step mechanical work to a cheap role " +
				"instead of doing it yourself. Delegate early, before duplicating the investigation in your own context. Keep one-read/one-edit tasks local, " +
				"and verify important results yourself.")
		case "aggressive":
			b.WriteString("\n\nDelegation policy: aggressive. Proactively delegate separable searches, routine implementation, tests and boilerplate " +
				"to a cheap or local role, in parallel where you can, and keep integration yourself. Skip delegation only for trivial one-step work " +
				"or when the handoff would duplicate work already done.")
		}
		b.WriteString("\nModel roles:\n")
		for _, r := range offered {
			fmt.Fprintf(&b, "- %s: %s", r.Name, r.Spec)
			if r.Price != "" {
				fmt.Fprintf(&b, " (%s)", r.Price)
			}
			if r.Hint != "" {
				fmt.Fprintf(&b, " — %s", r.Hint)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n\nAvailable agents:\n")
	for _, d := range t.Set.List() {
		names = append(names, d.Name)
		fmt.Fprintf(&b, "- %s: %s", d.Name, d.Description)
		if d.Tools != nil {
			fmt.Fprintf(&b, " (tools: %s)", strings.Join(d.Tools, ", "))
		}
		b.WriteString("\n")
	}
	enum, _ := json.Marshal(names)
	isolation := ""
	if t.Repo != "" {
		isolation = `,
			"isolation":{"type":"string","enum":["worktree"],"description":"Run in an isolated git worktree"}`
	}
	model := ""
	if len(offered) > 0 {
		names := []string{"inherit"}
		for _, r := range offered {
			names = append(names, r.Name)
		}
		roles, _ := json.Marshal(names)
		model = `,
			"model":{"type":"string","enum":` + string(roles) + `,"description":"Model role for this subagent; inherit uses your own model"}`
	}
	return llm.ToolSpec{
		Name:        ToolName,
		Description: strings.TrimSpace(b.String()),
		Schema: json.RawMessage(`{"type":"object","properties":{
			"description":{"type":"string","description":"Short (3-5 word) label for the task"},
			"prompt":{"type":"string","description":"Complete instructions for the subagent"},
			"subagent_type":{"type":"string","enum":` + string(enum) + `,"description":"Which agent to use"},
			"run_in_background":{"type":"boolean","description":"Return immediately and deliver the result later"}` + isolation + model + `},
			"required":["description","prompt","subagent_type"]}`),
	}
}

func (t *Tool) Run(ctx context.Context, _ *tools.Env, input json.RawMessage) tools.Result {
	var in struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
		Type        string `json:"subagent_type"`
		Background  bool   `json:"run_in_background"`
		Isolation   string `json:"isolation"`
		Model       string `json:"model"`
	}
	if len(input) == 0 || json.Unmarshal(input, &in) != nil || strings.TrimSpace(in.Prompt) == "" {
		return tools.Result{Content: "INVALID_JSON: expected description, prompt and subagent_type", IsError: true}
	}
	parent, emit, ok := agent.FromContext(ctx)
	if !ok {
		return tools.Result{Content: "task tool: no parent agent in context", IsError: true}
	}
	def, ok := t.Set.Get(in.Type)
	if !ok {
		return tools.Result{Content: fmt.Sprintf("unknown subagent_type %q", in.Type), IsError: true}
	}
	label := def.Name
	if d := strings.TrimSpace(in.Description); d != "" {
		label += ": " + d
	}
	role, _ := t.role(in.Model, def)
	isolation := in.Isolation
	if isolation == "" {
		isolation = def.Isolation
	}
	if isolation == "" && role.Isolation == "worktree" {
		// The role asks for it; without a repository it runs in place.
		if t.Repo != "" {
			isolation = "worktree"
		} else {
			emit(agent.Event{Kind: agent.EvNotice, Text: fmt.Sprintf("role %s runs in a worktree, but this isn't a git repository; the subagent works in place", role.Name)})
		}
	}
	switch isolation {
	case "", "none":
		isolation = ""
	case "worktree":
		if t.Repo == "" {
			return tools.Result{Content: "worktree isolation needs a git repository; run the task without isolation", IsError: true}
		}
	default:
		return tools.Result{Content: fmt.Sprintf("unknown isolation %q (only \"worktree\")", isolation), IsError: true}
	}
	if isolation == "worktree" && parent.Perms().Mode() == permission.ModePlan {
		return tools.Result{Content: "plan mode cannot create a worktree", IsError: true}
	}
	c := childRun{def: def, label: label, callID: tools.CallID(ctx), prompt: in.Prompt, worktree: isolation == "worktree", model: strings.TrimSpace(in.Model), maxTurns: childMaxTurns, minimalContext: role.Context == "minimal"}
	if role.MaxTurns > 0 {
		c.maxTurns = role.MaxTurns
	}
	if in.Background {
		id, err := parent.StartBackground(label, func(bctx context.Context, bemit func(agent.Event)) (string, bool) {
			res := t.runChild(bctx, parent, bemit, c)
			return res.Content, res.IsError
		})
		if err != nil {
			return tools.Result{Content: err.Error(), IsError: true}
		}
		parent.RecordDelegation()
		return tools.Result{Content: fmt.Sprintf("Started background task %s (%s). Its result will be delivered to you automatically when it finishes. "+
			"Keep working on other things meanwhile; call task_wait if you need the result before continuing, or task_stop to cancel it.", id, label)}
	}
	parent.RecordDelegation()
	return t.runChild(ctx, parent, emit, c)
}

type childRun struct {
	def            Definition
	label          string
	callID         string // the task call, for a debug trace
	prompt         string
	worktree       bool
	model          string // role or spec asked for by the caller; overrides def.Model
	maxTurns       int
	minimalContext bool
}

// runChild runs a subagent to completion, forwarding its activity to emit.
func (t *Tool) runChild(ctx context.Context, parent *agent.Agent, emit func(agent.Event), c childRun) tools.Result {
	def, label := c.def, c.label
	provider, model, notice := t.model(def, c.model, parent)
	if notice != "" {
		emit(agent.Event{Kind: agent.EvNotice, Text: notice})
	}
	if model != parent.Model() || provider.Name() != parent.ProviderName() {
		label += " · " + model // show which tier runs it
	}

	shared := t.Context
	if t.ContextFunc != nil {
		shared = t.ContextFunc()
	}
	if c.minimalContext && t.MinimalContextFunc != nil {
		shared = t.MinimalContextFunc()
	} else if t.SkillsIndex != nil && def.toolAllowed(skills.ToolName) {
		if idx := t.SkillsIndex(); idx != "" {
			shared = idx + "\n\n" + shared // shared ends with the date, which changes daily
		}
	}
	spawn := agent.SpawnOptions{
		Type:     def.Name,
		Provider: provider,
		Model:    model,
		System:   def.Prompt + "\n\n" + footer + "\n\n" + shared,
		MaxTurns: c.maxTurns,

		Label:       label,
		TraceParent: c.callID,
	}
	cwd := parent.Cwd()
	var wt *worktree.Worktree
	if c.worktree {
		if parent.Perms().Mode() == permission.ModePlan {
			return tools.Result{Content: "plan mode cannot create a worktree", IsError: true}
		}
		var err error
		if wt, err = worktree.Create(ctx, t.WorktreeRoot, t.Repo); err != nil {
			return tools.Result{Content: "could not create a worktree: " + err.Error(), IsError: true}
		}
		// Keep the parent's position inside the repository.
		cwd = wt.Path
		if rel, err := filepath.Rel(t.Repo, parent.Cwd()); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			cwd = filepath.Join(wt.Path, rel)
		}
		spawn.Cwd = cwd
		if t.SandboxFor != nil {
			spawn.Sandbox = t.SandboxFor(wt.Path, wt.CommonDir(ctx))
		}
		spawn.System += "\n\n<worktree>\nYou are working in an isolated git worktree at " + wt.Path + " on branch " + wt.Branch +
			", checked out from commit " + wt.Base + ". Your working directory is " + cwd + " (this replaces the working directory mentioned above). " +
			"Read and edit files only inside the worktree; never touch the original checkout at " + t.Repo + ". " +
			"You may commit your work on this branch; anything left uncommitted is committed for you when you finish.\n</worktree>"
		emit(agent.Event{Kind: agent.EvNotice, Agent: label, Model: model, Text: "working in worktree " + wt.Path + " (branch " + wt.Branch + ")"})
	}
	spawn.Tools = childTools(parent.Tools(), def, c.worktree)

	if p := parent.SessionPath(); p != "" {
		dir := filepath.Join(filepath.Dir(p), strings.TrimSuffix(filepath.Base(p), ".jsonl")+"-agents")
		sess, _ := session.Create(dir, session.Meta{Cwd: cwd, Provider: provider.Name(), Model: model})
		if sess != nil {
			defer sess.Close()
			spawn.Session = sess
		}
	}
	child := parent.Spawn(spawn)
	if t.OnChildDone != nil {
		defer t.OnChildDone(child.Owner())
	}

	var final, failure, stop string
	calls := 0
	for e := range child.Run(ctx, c.prompt) {
		switch e.Kind {
		// EvCompacted is forwarded for the same reason its failure notice
		// already was: a child that filled its context is worth seeing.
		// The label keeps it from reading as the parent's own compaction.
		case agent.EvToolStart, agent.EvToolEnd, agent.EvPermission, agent.EvNotice, agent.EvCompacted:
			if e.Kind == agent.EvToolEnd {
				calls++
			}
			e.Agent, e.Model = label, model
			emit(e)
		case agent.EvError:
			failure = e.Text
			e.Agent, e.Model = label, model
			emit(e)
		case agent.EvUsage:
			used := e.Usage.Model
			if used == "" {
				used = model
			}
			parent.AddSubagentUsage(used, e.Usage.Turn)
			st := parent.Stats()
			emit(agent.Event{Kind: agent.EvUsage, Usage: &st})
		case agent.EvAssistant:
			if text := strings.TrimSpace(e.Message.Text()); text != "" {
				final = text
			}
		case agent.EvDone:
			stop = e.StopReason
		}
	}

	var res tools.Result
	switch {
	case ctx.Err() != nil:
		res = tools.Result{Content: "subagent interrupted before finishing", IsError: true}
	case stop == "loop":
		res = tools.Result{Content: "subagent " + strings.TrimPrefix(failure, "stopped: the subagent ") + ". Check what it changed before relying on it." + lastWords(final), IsError: true}
	case stop == "max_turns":
		res = tools.Result{Content: fmt.Sprintf("subagent ran out of turns (%d) before finishing. Check what it changed before relying on it.", c.maxTurns) + lastWords(final), IsError: true}
	case failure != "" && final == "":
		res = tools.Result{Content: "subagent failed: " + failure, IsError: true}
	case final == "":
		res = tools.Result{Content: fmt.Sprintf("subagent finished (%s) without a final message after %d tool calls", stop, calls), IsError: true}
	default:
		res = tools.Result{Content: tools.Truncate(final, tools.MaxOutputBytes)}
	}
	if wt != nil {
		// Even a failed or interrupted task may have useful partial work.
		out, err := wt.Finish("larik: " + label)
		report := wt.Report(out)
		if err != nil {
			report = fmt.Sprintf("[worktree] Finishing the worktree at %s (branch %s) failed: %v. Inspect it with git.", wt.Path, wt.Branch, err)
		}
		res.Content += "\n\n" + report
	}
	return res
}

// model picks the child's provider and model: the task's model input,
// else the definition's, else the parent's.
func (t *Tool) model(def Definition, asked string, parent *agent.Agent) (llm.Provider, string, string) {
	spec, who := def.Model, "agent "+def.Name
	if asked != "" {
		spec, who = asked, "task"
	}
	if spec == "" || spec == "inherit" || t.Resolve == nil {
		return parent.Provider(), parent.Model(), ""
	}
	for _, r := range t.roles() {
		if r.Name != spec {
			continue
		}
		switch {
		case r.Legacy && parent.ProviderName() != "anthropic":
			return parent.Provider(), parent.Model(), fmt.Sprintf("%s asks for model %q, a Claude alias; using %s (map it under roles in config to pick another model)", who, spec, parent.Model())
		case r.Spec == "" && !r.Legacy:
			return parent.Provider(), parent.Model(), "" // an unset role inherits
		}
		break
	}
	p, m, err := t.Resolve(spec)
	if err != nil {
		return parent.Provider(), parent.Model(), fmt.Sprintf("%s: model %q unavailable (%v); using %s", who, spec, err, parent.Model())
	}
	return p, m, ""
}

// role is the role a task runs on: the task's model input, else the
// definition's model, when either names a role.
func (t *Tool) role(asked string, def Definition) (Role, bool) {
	name := strings.TrimSpace(asked)
	if name == "" {
		name = def.Model
	}
	for _, r := range t.roles() {
		if r.Name == name {
			return r, true
		}
	}
	return Role{}, false
}

func (t *Tool) policy() string {
	if t.Policy == nil {
		return "manual"
	}
	return t.Policy()
}

func (t *Tool) roles() []Role {
	if t.Roles == nil {
		return nil
	}
	return t.Roles()
}

// offeredRoles are the roles mapped to a model of their own.
func (t *Tool) offeredRoles() []Role {
	var out []Role
	for _, r := range t.roles() {
		if r.Spec != "" && !r.Legacy {
			out = append(out, r)
		}
	}
	return out
}

// browserPrefix starts the browser tools' names (browsercdp.Prefix); a
// definition can allow them all with "browser".
const browserPrefix = "browser_"

// childTools is the parent's registry filtered by the definition, minus
// the delegation tools (no nested or background delegation).
// In a worktree the lsp tool is left out: the language servers index the
// parent's tree, not the worktree.
func childTools(parent *tools.Registry, def Definition, inWorktree bool) *tools.Registry {
	var out []tools.Tool
	for _, spec := range parent.Specs() {
		// The task list is the main agent's, shown to the user; a child
		// reports back in its final message instead. Memory is the main
		// agent's too: a child has no memory section in its prompt.
		if spec.Name == ToolName || spec.Name == WaitToolName || spec.Name == StopToolName || spec.Name == tools.TodoToolName || spec.Name == permission.MemoryTool || spec.Name == permission.ExitPlanTool || !def.toolAllowed(spec.Name) {
			continue
		}
		if inWorktree && (spec.Name == "lsp" || spec.Name == "apply_code_action") {
			continue
		}
		// Added below, when the child may use any deferred tool.
		if spec.Name == tools.ToolSearchName || spec.Name == tools.CallToolName {
			continue
		}
		if tl, ok := parent.Get(spec.Name); ok {
			out = append(out, tl)
		}
	}
	// Deferred tools (a large MCP set) follow the definition too; with any
	// allowed, the child gets tool_search and call_tool over just those.
	var deferred []tools.Tool
	for _, tl := range parent.Deferred() {
		if def.toolAllowed(tl.Spec().Name) {
			deferred = append(deferred, tl)
		}
	}
	return tools.NewRegistry(out...).Defer(deferred...)
}

// lastWords quotes a stopped subagent's last message, if it had one.
func lastWords(final string) string {
	if final == "" {
		return ""
	}
	return "\n\nIts last message:\n" + tools.Truncate(final, 4000)
}
