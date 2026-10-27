package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"larik/internal/agent"
	"larik/internal/bench"
	"larik/internal/config"
	"larik/internal/providers"
	"larik/internal/skills"
	"larik/internal/subagent"
	"larik/internal/tools"
)

// runBench implements `larik bench`: small, self-checking coding and
// context-retention tasks run against one or more models, so a routing choice
// can be judged by pass rate, cost and time instead of guesswork.
func runBench(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	var (
		modelsFlag = fs.String("models", "", "comma-separated provider/model specs or config roles to compare (required); main means the configured primary model, e.g. \"main,worker,compact\"")
		tasksFlag  = fs.String("tasks", "", "comma-separated task names to run (default: all)")
		timeout    = fs.Duration("timeout", 3*time.Minute, "per task, per model")
		execFlag   = fs.String("execution", string(tools.DefaultExecution), "comma-separated execution settings to compare: tools, hybrid, code")
		runs       = fs.Int("runs", 1, "how many times to run each task; with more than one, a table of medians and ranges follows")
		keepFailed = fs.Bool("keep-failed", false, "keep each failed run's directory, with transcript.md and calls.jsonl (every tool call, scripts' included)")
		harness    = fs.String("harness", "", "comma-separated harness directories to compare, each laid out like a config directory (AGENTS.md, skills/); \"none\" is the default empty one, \"personal\" your own instructions and skills")
		asFlag     = fs.String("as", "", "run each task as a subagent on this role or agent (worker, explore, or an agent definition's name), with its prompt, tools and role_options turn cap")
		ctxFlag    = fs.String("context", "", "with --as, comma-separated context settings to compare: full, minimal (default: the role's role_options context)")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: larik bench --models spec1,spec2,... [flags]\n\n"+
			"Runs each task in %s against every model in its own throwaway directory in yolo mode\n"+
			"(the directory is discarded after unless --keep-failed keeps a failure; nothing you have\n"+
			"is touched), then checks the result mechanically (`go test`, exact output, or retained facts).\n"+
			"Use it to compare a cheap model against your main one before trusting it with real work,\n"+
			"to see what a routing preset actually buys you, or to compare execution settings and\n"+
			"harness variants (instructions and skills). With --as, each task runs the way a subagent\n"+
			"on that role runs it, to see how a model does in the job routing gives it.\n\n", taskNames())
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if strings.TrimSpace(*modelsFlag) == "" {
		fs.Usage()
		return fmt.Errorf("bench: --models is required")
	}
	tasks, err := selectTasks(*tasksFlag)
	if err != nil {
		return err
	}
	if *runs < 1 {
		return fmt.Errorf("bench: --runs must be at least 1")
	}
	var execs []tools.Execution
	for _, e := range strings.Split(*execFlag, ",") {
		x, err := tools.ParseExecution(strings.TrimSpace(e))
		if err != nil {
			return fmt.Errorf("bench: %w", err)
		}
		execs = append(execs, x)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	harnesses, err := benchHarnesses(*harness, home, cfg.ConfigDir)
	if err != nil {
		return err
	}
	role, err := benchRole(*asFlag, *ctxFlag, cfg, home, cwd)
	if err != nil {
		return err
	}
	if role.def != nil && strings.TrimSpace(*tasksFlag) == "" {
		tasks = role.tasks(tasks)
	}
	if w := harnessWarning(harnesses, role); w != "" {
		fmt.Fprintln(os.Stderr, w)
	}

	var specs []string
	for _, s := range strings.Split(*modelsFlag, ",") {
		if s = strings.TrimSpace(s); s != "" {
			specs = append(specs, s)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Resolve every model first: the rows' labels set the column width
	// before the first one prints.
	type benchModel struct {
		resolved providers.Resolved
		settings []benchSetting
	}
	var models []benchModel
	var rows []string
	for _, spec := range specs {
		resolved, err := providers.Resolve(cfg, benchModelSpec(spec))
		if err != nil {
			fmt.Fprintf(os.Stderr, "bench: %s: %v\n", spec, err)
			continue
		}
		label := spec
		if spec != resolved.String() {
			label = fmt.Sprintf("%s (%s)", spec, resolved.String())
		}
		m := benchModel{resolved, benchSettings(label, execs, harnesses, role)}
		for _, st := range m.settings {
			rows = append(rows, st.row)
		}
		models = append(models, m)
	}
	width := labelWidth(rows)

	var all []bench.Result
	for _, m := range models {
		resolved := m.resolved
		// Runs go round all settings before repeating, so a provider
		// that slows down mid-bench affects every setting alike.
		for run := 1; run <= *runs; run++ {
			for _, st := range m.settings {
				for _, task := range tasks {
					name := task.Name
					if *runs > 1 {
						name = fmt.Sprintf("%s #%d", task.Name, run)
					}
					fmt.Printf("%-*s %-24s ", width, st.row, name)
					if ctx.Err() != nil {
						fmt.Println("interrupted")
						return ctx.Err()
					}
					res := bench.RunWith(ctx, task, resolved.Provider, resolved.Model, bench.Options{Execution: st.exec, Timeout: *timeout, KeepFailed: *keepFailed, Runtime: resolved.Runtime, ConfigDir: st.harness.dir, SkillRoots: st.harness.skillRoots, Agent: role.def, MinimalContext: st.minimal, MaxTurns: role.maxTurns})
					res.Model = st.row
					all = append(all, res)
					printResult(res)
				}
			}
		}
	}
	if *runs > 1 {
		fmt.Println()
		printSpread(all)
	}
	fmt.Println()
	printSummary(all)
	if len(all) == 0 {
		return fmt.Errorf("bench: no model ran any task")
	}
	return nil
}

// benchSetting is one row of results: a model under one harness,
// execution and context setting.
type benchSetting struct {
	row     string
	exec    tools.Execution
	harness benchHarness
	minimal bool
}

// benchSettings lists a model's rows in run order. A row's label names
// only what varies or differs from the default, so a plain run keeps
// the model's own label.
func benchSettings(label string, execs []tools.Execution, harnesses []benchHarness, role benchRunRole) []benchSetting {
	var out []benchSetting
	for _, h := range harnesses {
		for _, exec := range execs {
			row := label
			if len(execs) > 1 || exec != tools.DefaultExecution {
				row += " [" + string(exec) + "]"
			}
			showHarness := len(harnesses) > 1 || h.dir != ""
			for _, minimal := range role.contexts {
				row := row
				if showHarness {
					if harnessUnseen(h, role, minimal) {
						row += " {" + h.name + ", unseen}"
					} else {
						row += " {" + h.name + "}"
					}
				}
				if role.def != nil {
					row += " as " + role.name
				}
				if len(role.contexts) > 1 || role.explicitContext {
					setting := "full"
					if minimal {
						setting = "minimal"
					}
					row += " <" + setting + ">"
				}
				out = append(out, benchSetting{row: row, exec: exec, harness: h, minimal: minimal})
			}
		}
	}
	return out
}

// harnessUnseen reports whether a row runs without its harness: a
// subagent with minimal context gets neither the harness's AGENTS.md nor
// its skills index (agent.MinimalContextSections), so such rows run the
// same prompt under every harness.
func harnessUnseen(h benchHarness, role benchRunRole, minimal bool) bool {
	return role.def != nil && minimal && h.dir != ""
}

// harnessWarning explains rows marked unseen, when there are any.
func harnessWarning(harnesses []benchHarness, role benchRunRole) string {
	for _, h := range harnesses {
		for _, minimal := range role.contexts {
			if harnessUnseen(h, role, minimal) {
				return "bench: with --as and minimal context, a subagent's prompt leaves out the harness's AGENTS.md and skills index, " +
					"so the <minimal> rows run the same prompt under every harness (marked \"unseen\"). " +
					"Compare harnesses on the full-context rows; the minimal rows show how much runs vary on their own."
			}
		}
	}
	return ""
}

// labelWidth is the row-label column: wide enough for the longest label,
// and never narrower than the 40 columns plain model names use.
func labelWidth(labels []string) int {
	width := 40
	for _, l := range labels {
		width = max(width, utf8.RuneCountInString(l))
	}
	return width
}

// resultLabels are the row labels of results, for sizing a table.
func resultLabels(results []bench.Result) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.Model
	}
	return out
}

// benchModelSpec gives the configured primary model a name alongside routing
// roles such as worker and compact. An empty spec is Resolve's main-model form.
func benchModelSpec(spec string) string {
	if spec == "main" {
		return ""
	}
	return spec
}

// benchHarness is one harness variant: a config-like directory, or "" for
// the default empty one, and the name its rows carry. skillRoots, when
// set, replace the directory's skills/ as where skills come from.
type benchHarness struct {
	name, dir  string
	skillRoots []skills.Root
}

// benchHarnesses parses --harness. With none given there is one variant,
// the default, and rows are labeled as before. "personal" is the user's
// own config directory with every user-wide skill directory, as a session
// outside any project sees them.
func benchHarnesses(flagValue, home, configDir string) ([]benchHarness, error) {
	if strings.TrimSpace(flagValue) == "" {
		return []benchHarness{{name: "none"}}, nil
	}
	var out []benchHarness
	seen := map[string]bool{}
	for _, d := range strings.Split(flagValue, ",") {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		h := benchHarness{name: "none"}
		if d == "personal" {
			h = benchHarness{name: "personal", dir: configDir}
			for _, r := range skills.Roots(home, configDir, home, home) {
				if r.Scope == "user" {
					h.skillRoots = append(h.skillRoots, r)
				}
			}
		} else if d != "none" {
			abs, err := filepath.Abs(d)
			if err != nil {
				return nil, fmt.Errorf("bench: harness %q: %w", d, err)
			}
			if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
				return nil, fmt.Errorf("bench: harness %q is not a directory", d)
			}
			h = benchHarness{name: filepath.Base(abs), dir: abs}
		}
		// Rows are keyed by label, so two variants must not share a name.
		if seen[h.name] {
			return nil, fmt.Errorf("bench: two harnesses are named %q; give the directories different names", h.name)
		}
		seen[h.name] = true
		out = append(out, h)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("bench: --harness names no directory")
	}
	return out, nil
}

// benchRunRole is how --as runs tasks: as a subagent of def on its role,
// once per context setting. A nil def means as the main agent.
type benchRunRole struct {
	name            string
	def             *subagent.Definition
	maxTurns        int
	contexts        []bool // minimal or not, one run each
	explicitContext bool
	canWrite        bool
}

// benchRole resolves --as and --context. Roles map to the built-in agent
// that runs on them by default; any other name is an agent definition,
// which runs with the options of the role it names as its model.
func benchRole(as, contexts string, cfg *config.Config, home, cwd string) (benchRunRole, error) {
	as = strings.TrimSpace(as)
	if as == "" {
		if strings.TrimSpace(contexts) != "" {
			return benchRunRole{}, fmt.Errorf("bench: --context needs --as")
		}
		return benchRunRole{contexts: []bool{false}}, nil
	}
	name := as
	switch as {
	case providers.RoleWorker:
		name = "general-purpose"
	case providers.RoleExplore:
		name = "explore"
	}
	defs := subagent.Discover(subagent.Dirs(home, cfg.ConfigDir, cwd, agent.GitRoot(cwd)))
	def, ok := defs.Get(name)
	if !ok {
		var names []string
		for _, d := range defs.List() {
			names = append(names, d.Name)
		}
		return benchRunRole{}, fmt.Errorf("bench: --as %q is neither worker, explore nor an agent (available: %s)", as, strings.Join(names, ", "))
	}
	opt := cfg.Routing().Options[def.Model]
	r := benchRunRole{name: as, def: &def, maxTurns: subagent.DefaultMaxTurns}
	if opt.MaxTurns > 0 {
		r.maxTurns = opt.MaxTurns
	}
	_, r.canWrite = subagent.Tools(tools.Default(), def).Get("write")
	if strings.TrimSpace(contexts) == "" {
		r.contexts = []bool{opt.Context == "minimal"}
		return r, nil
	}
	r.explicitContext = true
	for _, c := range strings.Split(contexts, ",") {
		switch strings.TrimSpace(c) {
		case "full":
			r.contexts = append(r.contexts, false)
		case "minimal":
			r.contexts = append(r.contexts, true)
		default:
			return benchRunRole{}, fmt.Errorf("bench: --context %q: want full or minimal", c)
		}
	}
	return r, nil
}

// tasks narrows the default task list to what a subagent can be scored
// on: never the main-agent-only ones, and for an agent that can't write
// files, only tasks it can answer in its reply.
func (r benchRunRole) tasks(all []bench.Task) []bench.Task {
	var out []bench.Task
	for _, t := range all {
		if t.MainOnly() || (!r.canWrite && t.VerifyReply == nil) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func selectTasks(namesFlag string) ([]bench.Task, error) {
	all := bench.Tasks()
	if strings.TrimSpace(namesFlag) == "" {
		return all, nil
	}
	byName := map[string]bench.Task{}
	for _, t := range all {
		byName[t.Name] = t
	}
	var out []bench.Task
	for _, n := range strings.Split(namesFlag, ",") {
		n = strings.TrimSpace(n)
		t, ok := byName[n]
		if !ok {
			return nil, fmt.Errorf("bench: unknown task %q (available: %s)", n, taskNames())
		}
		out = append(out, t)
	}
	return out, nil
}

func taskNames() string {
	var names []string
	for _, t := range bench.Tasks() {
		names = append(names, t.Name)
	}
	return strings.Join(names, ", ")
}

func printResult(r bench.Result) {
	status := "FAIL"
	if r.Pass {
		status = "pass"
	}
	fmt.Printf("%-4s  %6.1fs  %-10s  %3d tools  %2d req  %s in  %s out  %s peak ctx", status, r.Duration.Seconds(), costLabel(r.CostUSD), r.ToolCalls, r.Requests,
		kilo(r.Usage.ContextTokens()), kilo(r.Usage.Output), kilo(r.PeakContext))
	if r.FaultCalls > 0 {
		fmt.Printf("  ·  %d malformed (%s)", r.FaultCalls, r.FaultBreakdown())
	}
	switch {
	case r.NeverActed():
		fmt.Print("  ·  answered without calling a tool")
	case r.StoppedWithoutActing():
		fmt.Print("  ·  stopped talking, work unfinished")
	case !r.Pass && r.Stop != "":
		fmt.Printf("  ·  ended: %s", r.Stop)
	}
	if r.RetentionTotal > 0 {
		fmt.Printf("  ·  %d/%d facts retained", r.Retained, r.RetentionTotal)
	}
	if r.Compactions > 0 {
		if r.CompactionMeasured {
			fmt.Print("  ·  " + compactionChange(r.CompactionSavedTokens))
		} else {
			fmt.Print("  ·  compaction size unavailable")
		}
	}
	fmt.Println()
	if !r.Pass && r.Error != "" {
		fmt.Println(indent("error: " + truncate(r.Error, 300)))
	}
	if !r.Pass && r.Detail != "" {
		fmt.Println(indent(truncate(r.Detail, 400)))
	}
	if r.Kept != "" {
		fmt.Println(indent("kept: " + r.Kept))
	}
}

// kilo shortens a token count: 950, 12.3k.
func kilo(n int) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

func compactionChange(saved int) string {
	if saved < 0 {
		return "~" + kilo(-saved) + " context added"
	}
	return "~" + kilo(saved) + " context freed"
}

func costLabel(usd float64) string {
	if usd <= 0 {
		return "$0 (unpriced)"
	}
	return fmt.Sprintf("$%.4f", usd)
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "      " + l
	}
	return strings.Join(lines, "\n")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// printSpread prints, for each model and task run more than once, the
// pass count and the median and range of tokens, context, requests and
// time, so one lucky or unlucky run doesn't decide a comparison.
func printSpread(results []bench.Result) {
	type key struct{ model, task string }
	var order []key
	groups := map[key][]bench.Result{}
	for _, r := range results {
		k := key{r.Model, r.Task}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	width := labelWidth(resultLabels(results))
	fmt.Println("Per task, median (range):")
	for _, k := range order {
		rs := groups[k]
		pass := 0
		var in, peak, req, ms, retained, malformed []int
		retentionTotal, anyMalformed, noAct := 0, false, 0
		for _, r := range rs {
			if r.Pass {
				pass++
			}
			in = append(in, r.Usage.ContextTokens())
			peak = append(peak, r.PeakContext)
			req = append(req, r.Requests)
			ms = append(ms, int(r.Duration.Milliseconds()))
			malformed = append(malformed, r.FaultCalls)
			anyMalformed = anyMalformed || r.FaultCalls > 0
			if r.StoppedWithoutActing() {
				noAct++
			}
			if r.RetentionTotal > 0 {
				retained = append(retained, r.Retained)
				retentionTotal = r.RetentionTotal
			}
		}
		seconds := func(n int) string { return fmt.Sprintf("%.1fs", float64(n)/1000) }
		fmt.Printf("  %-*s %-20s %d/%d passed  ·  in %s  ·  peak ctx %s  ·  %s req  ·  %s", width, k.model, k.task, pass, len(rs),
			spread(in, kilo), spread(peak, kilo), spread(req, func(n int) string { return fmt.Sprint(n) }), spread(ms, seconds))
		if anyMalformed {
			fmt.Printf("  ·  malformed %s", spread(malformed, func(n int) string { return fmt.Sprint(n) }))
		}
		if noAct > 0 {
			fmt.Printf("  ·  %d/%d stopped without finishing", noAct, len(rs))
		}
		if len(retained) > 0 {
			fmt.Printf("  ·  retained %s/%d", spread(retained, func(n int) string { return fmt.Sprint(n) }), retentionTotal)
		}
		fmt.Println()
	}
}

// spread renders the median of xs and, when they differ, their range.
func spread(xs []int, show func(int) string) string {
	s := slices.Clone(xs)
	slices.Sort(s)
	med := s[len(s)/2]
	if len(s)%2 == 0 {
		med = (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	if s[0] == s[len(s)-1] {
		return show(med)
	}
	return fmt.Sprintf("%s (%s–%s)", show(med), show(s[0]), show(s[len(s)-1]))
}

// printSummary prints one line per model: pass rate, total cost, total time.
func printSummary(results []bench.Result) {
	type totals struct {
		pass, n   int
		cost      float64
		dur       time.Duration
		anyPriced bool
		in, out   int
		peak      int
		retained  int
		retention int
		compacted int
		saved     int
		measured  bool
		calls     int
		malformed int
		noAct     int
	}
	order := []string{}
	byModel := map[string]*totals{}
	for _, r := range results {
		t, ok := byModel[r.Model]
		if !ok {
			t = &totals{}
			byModel[r.Model] = t
			order = append(order, r.Model)
		}
		t.n++
		if r.Pass {
			t.pass++
		}
		t.cost += r.CostUSD
		t.dur += r.Duration
		t.anyPriced = t.anyPriced || r.CostUSD > 0
		t.in += r.Usage.ContextTokens()
		t.out += r.Usage.Output
		t.peak = max(t.peak, r.PeakContext)
		t.retained += r.Retained
		t.retention += r.RetentionTotal
		t.compacted += r.Compactions
		t.calls += r.ToolCalls
		t.malformed += r.FaultCalls
		if r.StoppedWithoutActing() {
			t.noAct++
		}
		if r.CompactionMeasured {
			t.saved += r.CompactionSavedTokens
			t.measured = true
		}
	}
	width := labelWidth(order)
	fmt.Println("Summary:")
	for _, model := range order {
		t := byModel[model]
		cost := "$0 (unpriced)"
		if t.anyPriced {
			cost = fmt.Sprintf("$%.4f", t.cost)
		}
		fmt.Printf("  %-*s %d/%d passed  ·  %s total  ·  %.1fs total  ·  %s in, %s out  ·  peak ctx %s", width, model, t.pass, t.n, cost, t.dur.Seconds(), kilo(t.in), kilo(t.out), kilo(t.peak))
		// The share of calls the model formed unusably: the figure to
		// compare when changing sampling, since a model can fumble many
		// calls and still pass by retrying.
		if t.calls > 0 {
			fmt.Printf("  ·  %d/%d calls malformed (%.0f%%)", t.malformed, t.calls, 100*float64(t.malformed)/float64(t.calls))
		}
		// Runs the model ended by talking instead of acting: the failure
		// bad sampling produces, which no malformed-call count can show.
		if t.noAct > 0 {
			fmt.Printf("  ·  %d/%d stopped without finishing", t.noAct, t.n)
		}
		if t.retention > 0 {
			fmt.Printf("  ·  %d/%d facts retained", t.retained, t.retention)
		}
		if t.compacted > 0 && t.measured {
			fmt.Print("  ·  " + compactionChange(t.saved))
		}
		fmt.Println()
	}
}
