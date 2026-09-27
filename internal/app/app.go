// Package app wires configuration, tools and services into agents. The
// process-wide parts (MCP connections, language servers, sandbox, skills)
// are built once by Setup; each session gets its own Agent from Open.
package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"larik/internal/agent"
	"larik/internal/checkpoint"
	"larik/internal/config"
	"larik/internal/hooks"
	"larik/internal/llm"
	"larik/internal/lsp"
	"larik/internal/mcp"
	"larik/internal/permission"
	"larik/internal/providers"
	"larik/internal/sandbox"
	"larik/internal/session"
	"larik/internal/skills"
	"larik/internal/subagent"
	"larik/internal/tools"
	"larik/internal/web"
)

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

	// Resolve turns a provider/model spec into a provider; tests replace it.
	Resolve func(cfg *config.Config, spec string) (providers.Resolved, error)

	// Warnings for front ends to surface.
	SandboxNote string
	SearchNote  string

	tools []tools.Tool
}

// Setup loads config for cwd and starts the shared services. Call Close
// when done.
func Setup(cwd, version string) (*App, error) {
	cfg, err := config.Load(cwd)
	if err != nil {
		return nil, err
	}
	a := &App{Cfg: cfg, Cwd: cwd, SessionDir: session.Dir(cfg.DataDir, cwd), Version: version, Resolve: providers.Resolve}

	home, _ := os.UserHomeDir()
	gitRoot := agent.GitRoot(cwd)
	projectRoot := gitRoot
	if projectRoot == "" {
		projectRoot = cwd
	}
	if keep := cfg.CheckpointRetention(); keep > 0 {
		_ = checkpoint.Prune(filepath.Join(cfg.DataDir, "checkpoints"), keep)
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

	a.LSP = lsp.NewManager(cfg.LSP, cwd, gitRoot, filepath.Join(cfg.DataDir, "logs"))
	if a.LSP.Enabled() {
		baseTools = append(baseTools, lsp.Tool{M: a.LSP})
	}

	a.AgentDefs = subagent.Discover(subagent.Dirs(home, cfg.ConfigDir, cwd, gitRoot))
	baseTools = append(baseTools, subagent.WaitTool{}, subagent.StopTool{}, &subagent.Tool{
		Set:         a.AgentDefs,
		ContextFunc: a.childContext,
		Resolve: func(spec string) (llm.Provider, string, error) {
			r, err := a.Resolve(cfg, spec)
			return r.Provider, r.Model, err
		},
		Repo:         gitRoot,
		WorktreeRoot: filepath.Join(cfg.DataDir, "worktrees"),
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
	system := agent.BuildSystemPrompt(a.Cwd, a.Cfg.ConfigDir)
	if a.Sandbox != nil {
		system += "\n\n<sandbox>\n" + a.Sandbox.Summary() + "\n</sandbox>"
	}
	if idx := a.Skills.Index(); idx != "" {
		system += "\n\n" + idx
	}
	return system
}

// childContext is what subagent prompts share with the main agent's.
func (a *App) childContext() string {
	ctx := agent.ContextSections(a.Cwd, a.Cfg.ConfigDir)
	if idx := a.Skills.Index(); idx != "" {
		ctx += "\n\n" + idx
	}
	return ctx
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

// Close shuts down the shared services.
func (a *App) Close() {
	a.MCP.Close()
	a.LSP.Close()
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
	sess    *session.Session
}

// Close stops background work, fires SessionEnd with reason and closes
// the session file.
func (s *Session) Close(reason string) {
	s.Agent.StopAllBackground()
	s.Agent.End(reason)
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
		Model:       resolved.Model,
		Effort:      eff,
		System:      a.SystemPrompt(),
		BuildSystem: a.SystemPrompt,
		Cwd:         cwd,
		MaxTurns:    a.Cfg.MaxTurns,
		Tools:       tools.NewRegistry(a.tools...),
		Perms:       perms,
		Session:     sess,
		Checkpoints: checkpoint.New(filepath.Join(a.Cfg.DataDir, "checkpoints", sess.ID)),
		OnAllowRule: func(rule string) { _ = config.PersistAllowRule(cwd, rule) },
		LoadTools:   a.loadTools,
		Hooks:       hookRunner,
		Skills:      a.Skills,
		LSP:         a.LSP,
		Sandbox:     sbTool,

		NoAutoCompact: !a.Cfg.AutoCompactOn(),
		Language:      a.Cfg.Language,
	})
	s := &Session{ID: sess.ID, Path: sess.Path, Agent: ag, Hooks: hookRunner, sess: sess}
	if state != nil {
		ag.Restore(state)
		s.History = state.All
		s.ForkOf = state.Meta.ForkOf
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
