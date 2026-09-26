// Command larik is a terminal coding agent.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/term"

	"larik/internal/agent"
	"larik/internal/checkpoint"
	"larik/internal/config"
	"larik/internal/headless"
	"larik/internal/hooks"
	"larik/internal/llm"
	"larik/internal/mcp"
	"larik/internal/permission"
	"larik/internal/providers"
	"larik/internal/session"
	"larik/internal/skills"
	"larik/internal/tools"
	"larik/internal/tui"
)

var version = "0.1.0-dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "larik:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		print   = flag.Bool("p", false, "print mode: run the prompt non-interactively and exit")
		output  = flag.String("output", "text", "print-mode output: text or json (one event per line)")
		model   = flag.String("model", "", "provider/model, e.g. anthropic/claude-opus-5, openai/gpt-5.5, gemini/gemini-3.8-flash, ollama/qwen3-coder")
		effort  = flag.String("effort", "", "reasoning effort: low, medium, high, xhigh, max")
		mode    = flag.String("mode", "", "permission mode: default, accept-edits, plan, yolo")
		resume  = flag.String("resume", "", "resume a session by id (or unique prefix)")
		cont    = flag.Bool("c", false, "continue the most recent session in this directory")
		list    = flag.Bool("sessions", false, "list sessions for this directory and exit")
		showVer = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: larik [flags] [prompt]\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVer {
		fmt.Println("larik", version)
		return nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return err
	}
	sessDir := session.Dir(cfg.DataDir, cwd)

	if *list {
		infos, err := session.List(sessDir)
		if err != nil {
			return err
		}
		for _, in := range infos {
			fmt.Printf("%s  %s  %s\n", in.ID, in.Modified.Format("2006-01-02 15:04"), in.Title)
		}
		return nil
	}

	prompt := strings.Join(flag.Args(), " ")
	if *print && prompt == "" && !term.IsTerminal(int(os.Stdin.Fd())) {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		prompt = strings.TrimSpace(string(data))
	}
	if *print && prompt == "" {
		return errors.New("-p needs a prompt argument or piped stdin")
	}

	// Session: resume or create.
	var (
		sess  *session.Session
		state *session.State
	)
	resumeID := *resume
	if *cont {
		infos, err := session.List(sessDir)
		if err != nil {
			return err
		}
		if len(infos) == 0 {
			return errors.New("no previous session in this directory")
		}
		resumeID = infos[0].ID
	}
	modelSpec := *model
	if resumeID != "" {
		path, err := session.Find(sessDir, resumeID)
		if err != nil {
			return err
		}
		if sess, state, err = session.Open(path); err != nil {
			return err
		}
		if modelSpec == "" && state.Meta.Model != "" {
			modelSpec = state.Meta.Provider + "/" + state.Meta.Model
		}
	}

	resolved, err := providers.Resolve(cfg, modelSpec)
	if err != nil {
		return err
	}
	if sess == nil {
		if sess, err = session.Create(sessDir, session.Meta{Cwd: cwd, Provider: resolved.Provider.Name(), Model: resolved.Model}); err != nil {
			return err
		}
	}
	defer sess.Close()

	permMode := cfg.Mode
	if *mode != "" {
		permMode = permission.Mode(*mode)
	}
	if permMode, err = permission.ParseMode(string(permMode)); err != nil {
		return err
	}
	eff := cfg.Effort
	if *effort != "" {
		eff = llm.Effort(*effort)
	}

	// MCP servers connect in the background; the agent waits for them before
	// its first request.
	home, _ := os.UserHomeDir()
	skillSet := skills.Discover(skills.Roots(home, cfg.ConfigDir, cwd, agent.GitRoot(cwd)))
	baseTools := tools.Builtin()
	system := agent.BuildSystemPrompt(cwd, cfg.ConfigDir)
	if idx := skillSet.Index(); idx != "" {
		baseTools = append(baseTools, skills.Tool{Set: skillSet})
		system += "\n\n" + idx
	}

	mcpMgr := mcp.NewManager(cfg, version)
	mcpMgr.Base = baseTools
	mcpMgr.Start()
	defer mcpMgr.Close()

	hookRunner := hooks.NewRunner(cfg.ActiveHooks(), cwd, sess.ID, sess.Path)

	a := agent.New(agent.Options{
		Provider:    resolved.Provider,
		Model:       resolved.Model,
		Effort:      eff,
		System:      system,
		Cwd:         cwd,
		MaxTurns:    cfg.MaxTurns,
		Tools:       tools.NewRegistry(baseTools...),
		Perms:       permission.NewChecker(permMode, cfg.Permissions, cwd),
		Session:     sess,
		Checkpoints: checkpoint.New(filepath.Join(cfg.DataDir, "checkpoints", sess.ID)),
		OnAllowRule: func(rule string) { _ = config.PersistAllowRule(cwd, rule) },
		LoadTools:   mcpMgr.Registry,
		Hooks:       hookRunner,
		Skills:      skillSet,
	})
	if state != nil {
		a.Restore(state)
	}

	if *print {
		defer a.End("other")
		if !cfg.ProjectHooksApproved() {
			fmt.Fprintln(os.Stderr, "! project hooks in .larik/settings.json are not approved and will not run; approve them with /hooks approve in interactive mode")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return headless.Run(ctx, a, prompt, headless.Format(*output), os.Stdout, os.Stderr)
	}

	var history []llm.Message
	if state != nil {
		history = state.All
	}
	defer a.End("prompt_input_exit")
	return tui.Run(tui.Options{
		Agent:         a,
		Config:        cfg,
		InitialPrompt: prompt,
		History:       history,
		SessionDir:    sessDir,
		MCP:           mcpMgr,
		Hooks:         hookRunner,
		Skills:        skillSet,
		Version:       version,
	})
}
