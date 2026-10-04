// Package app wires configuration, tools and services into agents. The
// process-wide parts (MCP connections, language servers, sandbox, skills)
// are built once by Setup; each session gets its own Agent from Open.
package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"larik/internal/agent"
	"larik/internal/audio"
	"larik/internal/browsercdp"
	"larik/internal/checkpoint"
	"larik/internal/config"
	"larik/internal/hooks"
	"larik/internal/llm"
	"larik/internal/lsp"
	"larik/internal/mcp"
	"larik/internal/memory"
	"larik/internal/permission"
	"larik/internal/providers"
	"larik/internal/sandbox"
	"larik/internal/session"
	"larik/internal/skills"
	"larik/internal/subagent"
	"larik/internal/tools"
	"larik/internal/trace"
	"larik/internal/web"
)

var catwalkOnce sync.Once

func loadCatwalkCatalog() {
	catwalkOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		catwalkProviders, err := llm.FetchCatwalk(ctx, nil, "")
		if err != nil {
			return // built-in providers and catalog remain available offline
		}
		providers.SetCatwalkProviders(catwalkProviders)
		for id, info := range llm.CatalogEntries(catwalkProviders) {
			if _, exists := llm.Catalog[id]; !exists {
				llm.Catalog[id] = info
			}
		}
	})
}

// App holds everything shared by the sessions of one project directory.
type App struct {
	Cfg        *config.Config
	Cwd        string
	SessionDir string
	Version    string

	Skills    *skills.Set
	AgentDefs *subagent.Set
	MCP       *mcp.Manager
	LSP       *lsp.Manager
	Sandbox   *sandbox.Sandbox // nil when unavailable or disabled
	// Memory is the notes kept across sessions; nil when switched off.
	Memory *memory.Store
	// Browser drives Chrome for the browser_* tools; nil unless enabled.
	Browser *browsercdp.Session
	// Audio is the optional local STT/TTS service used by interactive front ends.
	Audio audio.Service

	// Debug traces every session opened (debug setting, --debug or
	// LARIK_DEBUG); /debug turns it on or off for one session.
	Debug bool

	// Resolve turns a provider/model spec into a provider; tests replace it.
	Resolve func(cfg *config.Config, spec string) (providers.Resolved, error)

	// Warnings for front ends to surface.
	SandboxNote string
	SearchNote  string
	BrowserNote string

	tools     []tools.Tool
	closeOnce sync.Once
}

// Setup loads config for cwd and starts the shared services. Call Close
// when done.
func Setup(cwd, version string) (*App, error) {
	// Catwalk metadata is best-effort and time-bounded. Load it before the
	// user's config so explicit per-model metadata always wins.
	loadCatwalkCatalog()
	cfg, err := config.Load(cwd)
	if err != nil {
		return nil, err
	}
	a := &App{Cfg: cfg, Cwd: cwd, SessionDir: session.Dir(cfg.DataDir, cwd), Version: version, Resolve: providers.Resolve, Debug: cfg.DebugOn()}

	home, _ := os.UserHomeDir()
	gitRoot := agent.GitRoot(cwd)
	projectRoot := gitRoot
	if projectRoot == "" {
		projectRoot = cwd
	}
	if keep := cfg.CheckpointRetention(); keep > 0 {
		_ = checkpoint.Prune(filepath.Join(cfg.DataDir, "checkpoints"), keep)
	}
	if keep := cfg.DebugRetention(); keep > 0 {
		_ = trace.Prune(filepath.Join(cfg.DataDir, "sessions"), keep)
	}
	a.Skills = skills.Discover(skills.Roots(home, cfg.ConfigDir, cwd, gitRoot))
	baseTools := tools.Builtin()
	a.Sandbox, a.SandboxNote = sandbox.New(cfg.Sandbox, projectRoot, home)
	if !cfg.Web.FetchDisabled {
		baseTools = append(baseTools, web.FetchTool{F: web.NewFetcher()})
	}
	searcher, searchErr := web.NewSearcher(cfg.Web.Search)
	if searcher != nil {
		baseTools = append(baseTools, web.SearchTool{S: searcher})
	}
	if searchErr != nil {
		a.SearchNote = searchErr.Error()
	}
	if cfg.MemoryOn() {
		a.Memory = memory.New(memory.Dir(cfg.DataDir, projectRoot), memory.UserDir(cfg.DataDir))
		baseTools = append(baseTools, memory.Tool{S: a.Memory})
	}
	if cfg.Audio.Enabled {
		a.Audio = audio.New(cfg.Audio)
	}
	if cfg.Browser.Enabled {
		browserOpts := browsercdp.Options{
			Headless: cfg.Browser.Headless, ChromePath: cfg.Browser.ChromePath,
			ProfileDir:  filepath.Join(cfg.DataDir, "browser-profile"),
			DownloadDir: filepath.Join(cfg.DataDir, "browser-downloads"),
		}
		// Check launchability before advertising browser tools. The probe is
		// headless and uses a temporary profile, so it does not steal focus or
		// collide with a Chrome window owned by the user or another app.
		if err := browsercdp.CheckAvailable(browserOpts); err != nil {
			a.BrowserNote = "browser tools disabled: " + err.Error()
		} else {
			a.Browser = browsercdp.New(browserOpts)
			baseTools = append(baseTools, browsercdp.Tools(a.Browser)...)
		}
	}

	a.LSP = lsp.NewManager(cfg.LSP, cwd, gitRoot, filepath.Join(cfg.DataDir, "logs"))
	if a.LSP.Enabled() {
		baseTools = append(baseTools, lsp.Tool{M: a.LSP}, lsp.ApplyTool{M: a.LSP})
	}

	a.AgentDefs = subagent.Discover(subagent.Dirs(home, cfg.ConfigDir, cwd, gitRoot))
	baseTools = append(baseTools, agent.ExitPlanTool{}, subagent.WaitTool{}, subagent.StopTool{}, &subagent.Tool{
		Set:                a.AgentDefs,
		ContextFunc:        a.childContext,
		MinimalContextFunc: a.minimalChildContext,
		SkillsIndex:        a.Skills.Index,
		Resolve: func(spec string) (llm.Provider, string, error) {
			r, err := a.Resolve(cfg, spec)
			return r.Provider, r.Model, err
		},
		Roles:        a.roles,
		Policy:       func() string { return string(a.Cfg.Routing().Delegation) },
		Repo:         gitRoot,
		WorktreeRoot: filepath.Join(cfg.DataDir, "worktrees"),
		// A finished subagent's browser tabs are closed.
		OnChildDone: func(owner string) { a.Browser.Release(owner) },
		SandboxFor: func(dir, gitDir string) tools.Sandbox {
			if a.Sandbox == nil {
				return nil // a nil interface, not a typed nil
			}
			return a.Sandbox.ForWorktree(dir, gitDir)
		},
	})
	a.tools = baseTools

	// MCP servers connect in the background; agents wait for them before
	// their first request.
	a.MCP = mcp.NewManager(cfg, version)
	a.MCP.Base = baseTools
	a.MCP.Start()
	return a, nil
}

// SystemPrompt builds the main agent's system prompt from the current
// instruction files and skills. It rescans skills first, so it is called
// once per fresh context (a new session or /clear), never mid-context.
func (a *App) SystemPrompt() string {
	a.Skills.Reload()
	system := agent.BuildSystemPrompt(a.Cwd, a.Cfg.ConfigDir) + a.sandboxSection()
	if idx := a.Skills.Index(); idx != "" {
		system += "\n\n" + idx
	}
	// Read at each fresh context, like the instruction files: notes saved
	// during a session join the prompt at the next one.
	if mem := a.Memory.Prompt(); mem != "" {
		system += "\n\n" + mem
	}
	return system + "\n\n" + agent.DateSection()
}

// sandboxSection describes the bash sandbox, when one is active.
func (a *App) sandboxSection() string {
	if a.Sandbox == nil {
		return ""
	}
	return "\n\n<sandbox>\n" + a.Sandbox.Summary() + "\n</sandbox>"
}

// childContext is what subagent prompts share with the main agent's; the
// task tool adds the skills index for agents that may load skills.
func (a *App) childContext() string {
	return agent.SafetySection + "\n\n" + agent.ContextSections(a.Cwd, a.Cfg.ConfigDir) + a.sandboxSection() + "\n\n" + agent.DateSection()
}

// minimalChildContext is childContext without the user's global
// instructions or the skills index, for roles with `context: "minimal"`.
func (a *App) minimalChildContext() string {
	return agent.SafetySection + "\n\n" + agent.MinimalContextSections(a.Cwd) + a.sandboxSection() + "\n\n" + agent.DateSection()
}

// loadTools is each agent's tool set for a fresh context: the built-in
// tools and MCP servers' tools, plus the skill tool when skills exist.
func (a *App) loadTools(ctx context.Context, notify func(string)) *tools.Registry {
	reg := a.MCP.Registry(ctx, notify)
	if a.Skills.Index() != "" {
		reg = reg.With(skills.Tool{Set: a.Skills})
	}
	return reg
}

// Close shuts down the shared services. It is safe to call more than once.
func (a *App) Close() {
	if a == nil {
		return
	}
	a.closeOnce.Do(func() {
		a.MCP.Close()
		a.LSP.Close()
		a.Browser.Close()
		a.Sandbox.Close()
	})
}

// Options select the session and runtime settings for Open. Empty fields
// fall back to the resumed session, then to config.
type Options struct {
	Model    string // provider/model
	Effort   string
	Mode     string
	ResumeID string // session id or unique prefix
	Continue bool   // resume the most recent session
	// Fork branches the resumed session into a new one instead of
	// appending to it, keeping the first *ForkAt messages (all if nil).
	Fork   bool
	ForkAt *int
}

// Session is an opened agent plus what it owns.
type Session struct {
	ID      string
	Path    string
	ForkOf  string // parent session id, for branches
	Agent   *agent.Agent
	Hooks   *hooks.Runner
	History []llm.Message // prior messages for display when resumed
	// TraceErr is why debug recording couldn't start, when App.Debug is on.
	TraceErr error
	sess     *session.Session
	trace    *trace.Recorder
}

// StartTrace turns on debug recording for the session, continuing its
// trace if it has one.
func (s *Session) StartTrace() (*trace.Recorder, error) {
	if s.trace != nil {
		return s.trace, nil
	}
	rec, err := trace.Open(session.TraceDir(s.Path))
	if err != nil {
		return nil, err
	}
	s.trace = rec
	s.Agent.SetTrace(rec.Tracer())
	return rec, nil
}

// StopTrace turns debug recording off.
func (s *Session) StopTrace() {
	if s.trace == nil {
		return
	}
	s.Agent.SetTrace(nil)
	s.trace.Close()
	s.trace = nil
}

// Trace is the session's recorder while debug recording is on, else nil.
func (s *Session) Trace() *trace.Recorder { return s.trace }

// Close stops background work, fires SessionEnd with reason and closes
// the session file.
func (s *Session) Close(reason string) {
	s.Agent.StopAllBackground()
	s.Agent.End(reason)
	s.StopTrace()
	s.sess.Close()
}

// Open creates or resumes a session and builds its agent.
func (a *App) Open(o Options) (*Session, error) {
	var (
		sess  *session.Session
		state *session.State
		err   error
	)
	resumeID := o.ResumeID
	if o.Fork && o.ResumeID == "" && !o.Continue {
		return nil, errors.New("fork needs a session to branch from")
	}
	if o.Continue {
		infos, err := session.List(a.SessionDir)
		if err != nil {
			return nil, err
		}
		if len(infos) == 0 {
			return nil, errors.New("no previous session in this directory")
		}
		resumeID = infos[0].ID
	}
	modelSpec := o.Model
	if resumeID != "" {
		path, err := session.Find(a.SessionDir, resumeID)
		if err != nil {
			return nil, err
		}
		if o.Fork {
			keep := -1
			if o.ForkAt != nil {
				keep = *o.ForkAt
			}
			sess, state, err = session.Fork(a.SessionDir, path, keep)
		} else {
			sess, state, err = session.Open(path)
		}
		if err != nil {
			return nil, err
		}
		if modelSpec == "" && state.Meta.Model != "" {
			modelSpec = state.Meta.Provider + "/" + state.Meta.Model
		}
	}
	fail := func(err error) (*Session, error) {
		if sess != nil {
			sess.Close()
			if o.Fork { // a branch nobody will ever use
				_ = os.Remove(sess.Path)
				_ = os.RemoveAll(session.RawDir(sess.Path))
			}
		}
		return nil, err
	}

	resolved, err := a.Resolve(a.Cfg, modelSpec)
	if err != nil {
		return fail(err)
	}
	permMode := a.Cfg.Mode
	if o.Mode != "" {
		permMode = permission.Mode(o.Mode)
	}
	if permMode, err = permission.ParseMode(string(permMode)); err != nil {
		return fail(err)
	}
	eff := a.Cfg.Effort
	if o.Effort != "" {
		if eff, err = ParseEffort(o.Effort); err != nil {
			return fail(err)
		}
	}
	if sess == nil {
		if sess, err = session.Create(a.SessionDir, session.Meta{Cwd: a.Cwd, Provider: resolved.Provider.Name(), Model: resolved.Model}); err != nil {
			return nil, err
		}
	}

	hookRunner := hooks.NewRunner(a.Cfg.ActiveHooks(), a.Cwd, sess.ID, sess.Path)
	perms := permission.NewChecker(permMode, a.Cfg.Permissions, a.Cwd)
	perms.SetSandboxed(a.Sandbox != nil)
	var sbTool tools.Sandbox // stays a nil interface when there's no sandbox
	if a.Sandbox != nil {
		sbTool = a.Sandbox
	}
	cwd := a.Cwd
	ag := agent.New(agent.Options{
		Provider:    resolved.Provider,
		Runtime:     resolved.Runtime,
		Model:       resolved.Model,
		Effort:      eff,
		System:      a.SystemPrompt(),
		BuildSystem: a.SystemPrompt,
		Cwd:         cwd,
		MaxTurns:    a.Cfg.MaxTurns,
		Tools:       tools.NewRegistry(a.tools...),
		Execution:   a.Cfg.ExecutionFor(resolved.Provider.Name(), resolved.Model),
		Perms:       perms,
		Session:     sess,
		Checkpoints: checkpoint.New(filepath.Join(a.Cfg.DataDir, "checkpoints", sess.ID), cwd),
		OnAllowRule: func(rule string) error { return config.PersistAllowRule(cwd, rule) },
		LoadTools:   a.loadTools,
		Hooks:       hookRunner,
		Skills:      a.Skills,
		MCP:         a.MCP,
		LSP:         a.LSP,
		Sandbox:     sbTool,

		ExecutionFor:  func(provider, model string) tools.Execution { return a.Cfg.ExecutionFor(provider, model) },
		NoAutoCompact: !a.Cfg.AutoCompactOn(),
		TokenSaver:    a.Cfg.TokenSaverOn(),
		Language:      a.Cfg.Language,
		CompactWith:   a.compactModel,
		Budget: func() (float64, float64) {
			b := a.Cfg.Routing().Budget
			return b.SessionUSD, b.WarnFraction()
		},
	})
	hookRunner.SetEvaluator(a.hookEvaluator(ag))
	ag.SetAutoApprover(a.autoApprover(ag))
	s := &Session{ID: sess.ID, Path: sess.Path, Agent: ag, Hooks: hookRunner, sess: sess}
	if state != nil {
		ag.Restore(state)
		s.History = state.All
		s.ForkOf = state.Meta.ForkOf
	}
	if a.Debug {
		_, s.TraceErr = s.StartTrace()
	}
	return s, nil
}

// ParseEffort validates a reasoning-effort name ("default" clears it).
func ParseEffort(s string) (llm.Effort, error) {
	switch s {
	case "", "default":
		return llm.EffortDefault, nil
	case "low", "medium", "high", "xhigh", "max":
		return llm.Effort(s), nil
	}
	return "", errors.New("unknown effort " + s + " (low, medium, high, xhigh, max, default)")
}

// roles describes the model roles for the task tool, from the current
// config, so /routing changes apply to the next request.
func (a *App) roles() []subagent.Role {
	var out []subagent.Role
	opts := a.Cfg.Routing().Options
	for _, name := range providers.RoleNames(a.Cfg) {
		if name == providers.RoleCompact {
			continue // not something a subagent runs on
		}
		spec, _ := providers.RoleSpec(a.Cfg, name)
		o := opts[name]
		out = append(out, subagent.Role{Name: name, Spec: spec, Hint: providers.RoleHints[name], Price: providers.PriceNote(spec), Isolation: o.Isolation, MaxTurns: o.MaxTurns, Context: o.Context})
	}
	for _, name := range []string{"opus", "sonnet", "haiku"} {
		if providers.IsLegacyAlias(a.Cfg, name) {
			o := opts[name]
			out = append(out, subagent.Role{Name: name, Legacy: true, Isolation: o.Isolation, MaxTurns: o.MaxTurns, Context: o.Context})
		}
	}
	return out
}

// compactModel is the model for summarizing the conversation: the
// compact role when it is set, else nil for the agent's own model.
func (a *App) compactModel() (llm.Provider, string) {
	spec, _ := providers.RoleSpec(a.Cfg, providers.RoleCompact)
	if spec == "" {
		return nil, ""
	}
	r, err := a.Resolve(a.Cfg, providers.RoleCompact)
	if err != nil {
		return nil, ""
	}
	return r.Provider, r.Model
}
