package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"larik/internal/bench"
	"larik/internal/config"
	"larik/internal/providers"
)

// runBench implements `larik bench`: a handful of small, self-checking
// coding tasks run against one or more models, so a routing choice (or a
// preset) can be judged by pass rate, cost and time instead of guesswork.
func runBench(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	var (
		modelsFlag = fs.String("models", "", "comma-separated provider/model specs or config roles to compare (required), e.g. \"anthropic/claude-opus-5,worker,explore\"")
		tasksFlag  = fs.String("tasks", "", "comma-separated task names to run (default: all)")
		timeout    = fs.Duration("timeout", 3*time.Minute, "per task, per model")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: larik bench --models spec1,spec2,... [flags]\n\n"+
			"Runs each task in %s against every model in its own throwaway directory in yolo mode\n"+
			"(the directory is discarded after; nothing you have is touched), then checks the result\n"+
			"with `go test`. Use it to compare a cheap model against your main one before trusting it\n"+
			"with real work, or to see what a routing preset actually buys you.\n\n", taskNames())
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
		resolved, err := providers.Resolve(cfg, spec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bench: %s: %v\n", spec, err)
			continue
		}
		label := spec
		if spec != resolved.String() {
			label = fmt.Sprintf("%s (%s)", spec, resolved.String())
		}
		for _, task := range tasks {
			fmt.Printf("%-40s %-24s ", label, task.Name)
			if ctx.Err() != nil {
				fmt.Println("interrupted")
				return ctx.Err()
			}
			res := bench.Run(ctx, task, resolved.Provider, resolved.Model, *timeout)
			res.Model = label
			all = append(all, res)
			printResult(res)
		}
	}
	fmt.Println()
	printSummary(all)
	if len(all) == 0 {
		return fmt.Errorf("bench: no model ran any task")
	}
	return nil
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
	fmt.Printf("%-4s  %6.1fs  %-10s  %d tools\n", status, r.Duration.Seconds(), costLabel(r.CostUSD), r.ToolCalls)
	if !r.Pass && r.Detail != "" {
		fmt.Println(indent(truncate(r.Detail, 400)))
	}
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

// printSummary prints one line per model: pass rate, total cost, total time.
func printSummary(results []bench.Result) {
	type totals struct {
		pass, n   int
		cost      float64
		dur       time.Duration
		anyPriced bool
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
	}
	fmt.Println("Summary:")
	for _, model := range order {
		t := byModel[model]
		cost := "$0 (unpriced)"
		if t.anyPriced {
			cost = fmt.Sprintf("$%.4f", t.cost)
		}
		fmt.Printf("  %-40s %d/%d passed  ·  %s total  ·  %.1fs total\n", model, t.pass, t.n, cost, t.dur.Seconds())
	}
}
