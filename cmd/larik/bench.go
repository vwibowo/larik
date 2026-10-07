package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"larik/internal/bench"
	"larik/internal/config"
	"larik/internal/providers"
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
		execFlag   = fs.String("execution", "tools", "comma-separated execution settings to compare: tools, hybrid, code")
		runs       = fs.Int("runs", 1, "how many times to run each task; with more than one, a table of medians and ranges follows")
		keepFailed = fs.Bool("keep-failed", false, "keep each failed run's directory, with transcript.md and calls.jsonl (every tool call, scripts' included)")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: larik bench --models spec1,spec2,... [flags]\n\n"+
			"Runs each task in %s against every model in its own throwaway directory in yolo mode\n"+
			"(the directory is discarded after unless --keep-failed keeps a failure; nothing you have\n"+
			"is touched), then checks the result mechanically (`go test`, exact output, or retained facts).\n"+
			"Use it to compare a cheap model against your main one before trusting it with real work,\n"+
			"to see what a routing preset actually buys you, or to compare execution settings.\n\n", taskNames())
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

	var specs []string
	for _, s := range strings.Split(*modelsFlag, ",") {
		if s = strings.TrimSpace(s); s != "" {
			specs = append(specs, s)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var all []bench.Result
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
		// Runs go round all settings before repeating, so a provider
		// that slows down mid-bench affects every setting alike.
		for run := 1; run <= *runs; run++ {
			for _, exec := range execs {
				row := label
				if len(execs) > 1 || exec != tools.ExecTools {
					row += " [" + string(exec) + "]"
				}
				for _, task := range tasks {
					name := task.Name
					if *runs > 1 {
						name = fmt.Sprintf("%s #%d", task.Name, run)
					}
					fmt.Printf("%-40s %-24s ", row, name)
					if ctx.Err() != nil {
						fmt.Println("interrupted")
						return ctx.Err()
					}
					res := bench.RunWith(ctx, task, resolved.Provider, resolved.Model, bench.Options{Execution: exec, Timeout: *timeout, KeepFailed: *keepFailed})
					res.Model = row
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

// benchModelSpec gives the configured primary model a name alongside routing
// roles such as worker and compact. An empty spec is Resolve's main-model form.
func benchModelSpec(spec string) string {
	if spec == "main" {
		return ""
	}
	return spec
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
	fmt.Println("Per task, median (range):")
	for _, k := range order {
		rs := groups[k]
		pass := 0
		var in, peak, req, ms, retained, malformed []int
		retentionTotal, anyMalformed := 0, false
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
			if r.RetentionTotal > 0 {
				retained = append(retained, r.Retained)
				retentionTotal = r.RetentionTotal
			}
		}
		seconds := func(n int) string { return fmt.Sprintf("%.1fs", float64(n)/1000) }
		fmt.Printf("  %-40s %-20s %d/%d passed  ·  in %s  ·  peak ctx %s  ·  %s req  ·  %s", k.model, k.task, pass, len(rs),
			spread(in, kilo), spread(peak, kilo), spread(req, func(n int) string { return fmt.Sprint(n) }), spread(ms, seconds))
		if anyMalformed {
			fmt.Printf("  ·  malformed %s", spread(malformed, func(n int) string { return fmt.Sprint(n) }))
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
		if r.CompactionMeasured {
			t.saved += r.CompactionSavedTokens
			t.measured = true
		}
	}
	fmt.Println("Summary:")
	for _, model := range order {
		t := byModel[model]
		cost := "$0 (unpriced)"
		if t.anyPriced {
			cost = fmt.Sprintf("$%.4f", t.cost)
		}
		fmt.Printf("  %-40s %d/%d passed  ·  %s total  ·  %.1fs total  ·  %s in, %s out  ·  peak ctx %s", model, t.pass, t.n, cost, t.dur.Seconds(), kilo(t.in), kilo(t.out), kilo(t.peak))
		// The share of calls the model formed unusably: the figure to
		// compare when changing sampling, since a model can fumble many
		// calls and still pass by retrying.
		if t.calls > 0 {
			fmt.Printf("  ·  %d/%d calls malformed (%.0f%%)", t.malformed, t.calls, 100*float64(t.malformed)/float64(t.calls))
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
