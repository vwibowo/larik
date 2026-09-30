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
	"strings"
	"syscall"

	"golang.org/x/term"

	"larik/internal/app"
	"larik/internal/config"
	"larik/internal/headless"
	"larik/internal/providers"
	"larik/internal/sandbox"
	"larik/internal/session"
	"larik/internal/tui"
)

var version = "0.1.0-dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == sandbox.BridgeCommand {
		// Runs inside a Linux sandbox, before the command; see sandbox.BridgeMain.
		os.Exit(sandbox.BridgeMain(os.Args[2:]))
	}
	if err := run(); err != nil {
		var reported headless.ErrReported
		if !errors.As(err, &reported) {
			fmt.Fprintln(os.Stderr, "larik:", err)
		}
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		return runServe(os.Args[2:])
	}
	if len(os.Args) > 1 && os.Args[1] == "bench" {
		return runBench(os.Args[2:])
	}
	if len(os.Args) > 1 && os.Args[1] == "trace" {
		return runTrace(os.Args[2:])
	}
	var (
		print   = flag.Bool("p", false, "print mode: run the prompt non-interactively and exit")
		output  = flag.String("output", "text", "print-mode output: text or json (one event per line)")
		model   = flag.String("model", "", "provider/model, e.g. anthropic/claude-opus-5, openai/gpt-5.5, gemini/gemini-3.8-flash, ollama/qwen3-coder")
		effort  = flag.String("effort", "", "reasoning effort: low, medium, high, xhigh, max")
		mode    = flag.String("mode", "", "permission mode: default, accept-edits, plan, yolo")
		resume  = flag.String("resume", "", "resume a session by id (or unique prefix)")
		cont    = flag.Bool("c", false, "continue the most recent session in this directory")
		fork    = flag.Bool("fork", false, "with -c or --resume: branch into a new session, leaving the original untouched")
		list    = flag.Bool("sessions", false, "list sessions for this directory and exit")
		showVer = flag.Bool("version", false, "print version and exit")
		debug   = flag.Bool("debug", false, "record this session's requests, responses and tool calls for `larik trace` (also $LARIK_DEBUG=1)")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: larik [flags] [prompt]\n       larik serve [flags]   (HTTP + SSE API; see larik serve -h)\n"+
			"       larik bench --models spec1,spec2,...  (compare models on small self-checking tasks; see larik bench -h)\n"+
			"       larik trace [session]   (review a debug trace in the browser; see larik trace -h)\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVer {
		fmt.Println("larik", version)
		return nil
	}
	if *output != "text" && *output != "json" {
		return fmt.Errorf("--output must be text or json, not %q", *output)
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

	// First start with nothing configured: walk through connecting a
	// provider instead of failing.
	interactive := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	if !*print && interactive && *model == "" && *resume == "" && !*cont && !providers.HasDefault(cfg) {
		spec, err := tui.RunSetup(cfg)
		if err != nil {
			return err
		}
		if spec == "" {
			fmt.Println("No model set up. Run larik again to connect one, or edit " + cfg.UserConfigPath())
			return nil
		}
		*model = spec
	}

	a, err := app.Setup(cwd, version)
	if err != nil {
		return err
	}
	defer a.Close()
	if *debug || envDebug() {
		a.Debug = true
	}
	s, err := a.Open(app.Options{Model: *model, Effort: *effort, Mode: *mode, ResumeID: *resume, Continue: *cont, Fork: *fork})
	if err != nil {
		return err
	}

	if *print {
		defer s.Close("other")
		if a.SearchNote != "" {
			fmt.Fprintln(os.Stderr, "! web_search disabled: "+a.SearchNote)
		}
		if a.SandboxNote != "" {
			fmt.Fprintln(os.Stderr, "! "+a.SandboxNote)
		}
		if !cfg.ProjectHooksApproved() {
			fmt.Fprintln(os.Stderr, "! project hooks in .larik/settings.json are not approved and will not run; approve them with /hooks approve in interactive mode")
		}
		if s.TraceErr != nil {
			fmt.Fprintln(os.Stderr, "! debug trace: "+s.TraceErr.Error())
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err := headless.Run(ctx, s.Agent, prompt, headless.Format(*output), os.Stdout, os.Stderr)
		if rec := s.Trace(); rec != nil {
			fmt.Fprintf(os.Stderr, "trace: %s (view with: larik trace %s)\n", rec.Dir(), s.ID)
		}
		return err
	}

	// The TUI owns the session from here: it can switch to others and
	// closes whichever is current when it exits.
	return tui.Run(tui.Options{
		App:           a,
		Session:       s,
		Config:        a.Cfg,
		InitialPrompt: prompt,
		SessionDir:    a.SessionDir,
		MCP:           a.MCP,
		Skills:        a.Skills,
		Agents:        a.AgentDefs,
		LSP:           a.LSP,
		Sandbox:       a.Sandbox,
		SandboxNote:   a.SandboxNote,
		SearchNote:    a.SearchNote,
		Version:       version,
	})
}
