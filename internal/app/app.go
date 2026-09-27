// Package app wires configuration, tools and services into agents. The
// process-wide parts (MCP connections, language servers, sandbox, skills)
// are built once by Setup; each session gets its own Agent from Open.
package app

import (
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

	system string
	tools  []tools.Tool
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
	a.Skills = skills.Discover(skills.Roots(home, cfg.ConfigDir, cwd, gitRoot))
	baseTools := tools.Builtin()
	a.Sandbox, a.SandboxNote = sandbox.New(cfg.Sandbox, projectRoot, home)
	a.system = agent.BuildSystemPrompt(cwd, cfg.ConfigDir)
	if a.Sandbox != nil {
		a.system += "\n\n<sandbox>\n" + a.Sandbox.Summary() + "\n</sandbox>"
	}
	childContext := agent.ContextSections(cwd, cfg.ConfigDir)
	if idx := a.Skills.Index(); idx != "" {
		baseTools = append(baseTools, skills.Tool{Set: a.Skills})
		a.system += "\n\n" + idx
		childContext += "\n\n" + idx
	}
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
		Set:     a.AgentDefs,
		Context: childContext,
		Resolve: func(spec string) (llm.Provider, string, error) {
			r, err := a.Resolve(cfg, spec)
			return r.Provider, r.Model, err
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
}

// Session is an opened agent plus what it owns.
type Session struct {
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
		if sess, state, err = session.Open(path); err != nil {
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
		System:      a.system,
		Cwd:         cwd,
		MaxTurns:    a.Cfg.MaxTurns,
		Tools:       tools.NewRegistry(a.tools...),
		Perms:       perms,
		Session:     sess,
		Checkpoints: checkpoint.New(filepath.Join(a.Cfg.DataDir, "checkpoints", sess.ID)),
		OnAllowRule: func(rule string) { _ = config.PersistAllowRule(cwd, rule) },
		LoadTools:   a.MCP.Registry,
		Hooks:       hookRunner,
		Skills:      a.Skills,
		LSP:         a.LSP,
		Sandbox:     sbTool,
	})
	s := &Session{Agent: ag, Hooks: hookRunner, sess: sess}
	if state != nil {
		ag.Restore(state)
		s.History = state.All
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
