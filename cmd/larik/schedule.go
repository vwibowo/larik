package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"larik/internal/config"
	"larik/internal/permission"
	"larik/internal/schedule"
)

const scheduleUsage = `Usage:
  larik schedule add [flags] "<cron>" <prompt>   run a prompt on a schedule, in this directory
  larik schedule list                            show schedules and their last runs
  larik schedule rm <id>                         delete a schedule
  larik schedule run                             run due schedules until stopped

Schedules run only while "larik schedule run" is running: keep it in a
terminal, tmux, or a login item. Each run is "larik -p" in the directory the
schedule was added from, with its output in a log under ~/.local/share/larik/schedules.

Cron: minute hour day month weekday, in local time, e.g. "0 9 * * 1-5"
(weekdays at 9:00), "*/30 * * * *", or @hourly, @daily, @weekly.
`

// runSchedule implements `larik schedule`.
func runSchedule(args []string) error {
	store := schedule.Store{Path: config.ScheduleFile()}
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, scheduleUsage)
		return errors.New("larik schedule needs a command")
	}
	switch args[0] {
	case "add":
		return scheduleAdd(store, args[1:])
	case "list", "ls":
		return scheduleList(store, os.Stdout)
	case "rm", "remove":
		if len(args) != 2 {
			return errors.New("usage: larik schedule rm <id>")
		}
		e, err := store.Remove(args[1])
		if err == nil {
			fmt.Printf("removed %s (%s)\n", e.ID, e.Cron)
		}
		return err
	case "run":
		return scheduleRun(store)
	case "-h", "--help", "help":
		fmt.Print(scheduleUsage)
		return nil
	}
	fmt.Fprint(os.Stderr, scheduleUsage)
	return fmt.Errorf("unknown schedule command %q", args[0])
}

func scheduleAdd(store schedule.Store, args []string) error {
	fs := flag.NewFlagSet("schedule add", flag.ContinueOnError)
	var (
		model   = fs.String("model", "", "provider/model for the runs (default: your default model)")
		mode    = fs.String("mode", "", "permission mode for the runs: default (anything that asks is denied), accept-edits, auto or yolo")
		timeout = fs.Duration("timeout", 30*time.Minute, "stop a run after this long (0 means no limit)")
	)
	fs.Usage = func() { fmt.Fprint(os.Stderr, scheduleUsage+"\nFlags for add:\n"); fs.PrintDefaults() }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		fs.Usage()
		return errors.New(`usage: larik schedule add [flags] "<cron>" <prompt>`)
	}
	if *mode != "" {
		if _, err := permission.ParseMode(*mode); err != nil {
			return err
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	e := schedule.Entry{Cron: fs.Arg(0), Prompt: strings.Join(fs.Args()[1:], " "), Cwd: cwd, Model: *model, Mode: *mode}
	if *timeout > 0 {
		e.Timeout = timeout.String()
	}
	if e, err = store.Add(e); err != nil {
		return err
	}
	fmt.Printf("added %s: next run %s\n", e.ID, e.Next().Format("Mon Jan 2 15:04"))
	fmt.Println(`runs happen while "larik schedule run" is running`)
	return nil
}

func scheduleList(store schedule.Store, w io.Writer) error {
	entries, err := store.List()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintln(w, `no schedules; add one with larik schedule add "<cron>" <prompt>`)
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tCRON\tNEXT\tLAST\tDIRECTORY\tPROMPT")
	for _, e := range entries {
		last := "never"
		if !e.LastRun.IsZero() {
			last = e.LastRun.Format("Jan 2 15:04") + " " + e.LastStatus
		}
		next := "never"
		if n := e.Next(); !n.IsZero() {
			next = n.Format("Mon Jan 2 15:04")
		}
		prompt := e.Prompt
		if len([]rune(prompt)) > 40 {
			prompt = string([]rune(prompt)[:39]) + "…"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", e.ID, e.Cron, next, last, e.Cwd, prompt)
	}
	return tw.Flush()
}

func scheduleRun(store schedule.Store) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := log.New(os.Stderr, "", log.LstdFlags)
	logger.Printf("larik schedule: running due schedules from %s (ctrl+c to stop)", store.Path)
	d := &schedule.Daemon{Store: store, Exe: exe, LogDir: config.ScheduleLogDir(), Logf: logger.Printf}
	return d.Run(ctx)
}
